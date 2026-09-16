// Package transcode wraps the external encoder. Nothing else in the program
// knows what the command lines look like.
//
// Two encoders are supported, and the difference matters:
//
//   - ffmpeg is universally available, preserves tags, and silently discards
//     embedded cover art. The Ogg Opus muxer cannot carry an attached picture
//     stream, and ffmpeg will not write the METADATA_BLOCK_PICTURE comment
//     that Opus files actually use for artwork.
//   - opusenc (from opus-tools) reads FLAC directly and carries both the tags
//     and the embedded artwork across, because it speaks Vorbis comments
//     natively. It also resamples hi-res sources to 48 kHz, which Opus
//     requires anyway.
//
// When ffmpeg is doing the encoding, ExtractCover can drop a cover.jpg beside
// the tracks instead, which every mainstream mobile player falls back to.
package transcode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Tool identifies an encoder backend.
type Tool int

const (
	// FFmpeg encodes with ffmpeg. Tags survive; artwork does not.
	FFmpeg Tool = iota
	// Opusenc encodes with opusenc. Tags and artwork both survive.
	Opusenc
)

func (t Tool) String() string {
	if t == Opusenc {
		return "opusenc"
	}
	return "ffmpeg"
}

// EmbedsCoverArt reports whether the backend keeps artwork inside the file.
func (t Tool) EmbedsCoverArt() bool { return t == Opusenc }

// CoverFile is the sidecar written when the encoder cannot embed artwork.
const CoverFile = "cover.jpg"

// Options describes one transcode.
type Options struct {
	Tool        Tool
	FFmpegPath  string // "ffmpeg" or an absolute path
	OpusencPath string // "opusenc" or an absolute path
	Input       string // source .flac
	Output      string // destination .opus
	Bitrate     string // "192k" or "192"
}

// Select resolves the configured encoder preference ("auto", "ffmpeg" or
// "opusenc") against what is actually installed. "auto" prefers opusenc,
// because keeping the artwork is worth more than the marginal convenience of
// having one fewer binary to install.
func Select(pref, ffmpegPath, opusencPath string) (Tool, error) {
	ffmpegOK := available(ffmpegPath, "ffmpeg")
	opusencOK := available(opusencPath, "opusenc")

	switch strings.ToLower(strings.TrimSpace(pref)) {
	case "ffmpeg":
		if !ffmpegOK {
			return FFmpeg, fmt.Errorf("encoder ffmpeg is not installed")
		}
		return FFmpeg, nil

	case "opusenc":
		if !opusencOK {
			return Opusenc, fmt.Errorf("encoder opusenc is not installed (try the opus-tools package)")
		}
		return Opusenc, nil

	default: // "auto" or empty
		switch {
		case opusencOK:
			return Opusenc, nil
		case ffmpegOK:
			return FFmpeg, nil
		default:
			return FFmpeg, errors.New("no encoder found: install ffmpeg, or opus-tools for embedded cover art")
		}
	}
}

func available(bin, fallback string) bool {
	if bin == "" {
		bin = fallback
	}
	_, err := exec.LookPath(bin)
	return err == nil
}

// Convert transcodes Input to Output.
//
// The encode is staged into "<output>.tmp" and renamed on success, for two
// reasons: a cancelled or crashed run never leaves a truncated .opus behind
// that the scanner would mistake for a finished file, and the watcher's ignore
// rules skip .tmp so the destination tree only ever fires one event per track.
func Convert(ctx context.Context, o Options) error {
	if o.Bitrate == "" {
		o.Bitrate = "192k"
	}
	if err := os.MkdirAll(filepath.Dir(o.Output), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	tmp := o.Output + ".tmp"
	var cmd *exec.Cmd

	if o.Tool == Opusenc {
		cmd = exec.CommandContext(ctx, binOr(o.OpusencPath, "opusenc"),
			"--quiet",
			"--bitrate", kbits(o.Bitrate),
			"--vbr",
			o.Input, tmp,
		)
	} else {
		// -vn is explicit about what ffmpeg would do anyway: drop the attached
		// picture. Leaving it implicit makes the stream mapping noisier and
		// changes behaviour between ffmpeg builds.
		cmd = exec.CommandContext(ctx, binOr(o.FFmpegPath, "ffmpeg"),
			"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
			"-i", o.Input,
			"-vn",
			"-map_metadata", "0",
			"-c:a", "libopus",
			"-b:a", o.Bitrate,
			"-vbr", "on",
			"-application", "audio",
			"-f", "opus",
			tmp,
		)
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		os.Remove(tmp)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s %s: %w: %s", o.Tool, filepath.Base(o.Input), err, lastLines(stderr.String(), 3))
	}

	if err := os.Rename(tmp, o.Output); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("finalise %s: %w", o.Output, err)
	}
	return nil
}

// ExtractCover writes the artwork embedded in input to cover.jpg in dir, and
// reports whether it created the file. An existing cover is left alone, and a
// source with no artwork is not an error.
//
// The write goes through a unique temporary name because several workers can
// be encoding tracks from the same album at once.
func ExtractCover(ctx context.Context, ffmpegPath, input, dir string) (bool, error) {
	final := filepath.Join(dir, CoverFile)
	if _, err := os.Stat(final); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}

	tmp, err := os.CreateTemp(dir, ".cover-*.tmp")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)

	cmd := exec.CommandContext(ctx, binOr(ffmpegPath, "ffmpeg"),
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", input,
		"-an",
		"-frames:v", "1",
		"-update", "1",
		"-f", "image2",
		"-c:v", "mjpeg",
		tmpName,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		// No attached picture: ffmpeg exits non-zero with "does not contain
		// any stream". Nothing to do, and nothing worth logging.
		return false, nil
	}

	if info, err := os.Stat(tmpName); err != nil || info.Size() == 0 {
		return false, nil
	}
	if err := os.Rename(tmpName, final); err != nil {
		return false, fmt.Errorf("write %s: %w", final, err)
	}
	return true, nil
}

func binOr(bin, fallback string) string {
	if bin == "" {
		return fallback
	}
	return bin
}

// kbits turns "192k" into "192", which is what opusenc's --bitrate expects.
func kbits(bitrate string) string {
	s := strings.TrimSpace(strings.ToLower(bitrate))
	s = strings.TrimSuffix(s, "bps")
	s = strings.TrimSuffix(s, "k")

	if n, err := strconv.ParseFloat(s, 64); err == nil && n > 0 {
		if n > 1000 { // someone wrote "192000"
			n /= 1000
		}
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	return "192"
}

// lastLines keeps encoder error output short enough for a log line.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}
