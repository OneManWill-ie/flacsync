// Package transcode wraps the external encoder. Nothing else in the program
// knows what the command lines look like.
//
// opusenc (from opus-tools) is the required encoder. ffmpeg is universally
// available and preserves tags, but silently discards embedded cover art: the
// Ogg Opus muxer cannot carry an attached picture stream, and ffmpeg will not
// write the METADATA_BLOCK_PICTURE comment Opus files actually use. The
// default "auto" therefore refuses to fall back to ffmpeg rather than quietly
// building an art-less mirror; "ffmpeg" remains an explicit opt-in.
//
// opusenc reads FLAC directly and carries both tags and artwork across,
// because it speaks Vorbis comments natively. It also resamples hi-res
// sources to 48 kHz, which Opus requires anyway. Sources opusenc cannot read
// (m4a/ALAC and the other formats listed in internal/media) are decoded to a
// temporary FLAC with ffmpeg first, tags and attached picture included, so
// the rest of this package only ever sees FLAC.
//
// When ffmpeg is doing the encoding, ExtractCover can drop a cover.jpg beside
// the tracks instead, which every mainstream mobile player falls back to.
package transcode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/image/webp"

	"flacsync/internal/media"
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
// "opusenc") against what is actually installed.
//
// "auto" (the default) requires opusenc instead of falling back to ffmpeg:
// silently losing every embedded cover is worse than an explicit error
// telling the user to install opus-tools. "ffmpeg" stays available as an
// explicit choice for libraries that carry no artwork.
func Select(pref, ffmpegPath, opusencPath string) (Tool, error) {
	switch strings.ToLower(strings.TrimSpace(pref)) {
	case "ffmpeg":
		if !available(ffmpegPath, "ffmpeg") {
			return FFmpeg, fmt.Errorf("encoder ffmpeg is not installed")
		}
		return FFmpeg, nil

	default: // "opusenc", "auto" or empty
		if !available(opusencPath, "opusenc") {
			return Opusenc, fmt.Errorf("encoder opusenc is not installed: install opus-tools (https://opus-codec.org/downloads/) so cover art survives")
		}
		return Opusenc, nil
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
		// opusenc does not read m4a/ALAC and friends. Decode those to a
		// temporary FLAC first; ffmpeg carries the tags and the attached
		// picture across, which lets the rest of this branch keep treating
		// its input as a FLAC.
		input := o.Input
		if !media.IsFLAC(input) {
			flac, cleanup, err := decodeToFLAC(ctx, o, input)
			if err != nil {
				return err
			}
			defer cleanup()
			input = flac
		}

		args := []string{"--quiet", "--bitrate", kbits(o.Bitrate), "--vbr"}

		// opusenc auto-imports the FLAC's embedded picture, but only if it
		// recognises the format (JPEG/PNG/GIF); a WebP cover, for instance,
		// is silently skipped even though opusenc still exits 0. When that's
		// what we're about to hit, convert the picture to a JPEG ourselves
		// (in-process for WebP) and hand it to opusenc explicitly via
		// --picture, which takes priority and gives the track its artwork
		// back. A picture opusenc handles natively is left untouched, so it
		// isn't embedded twice. A picture that cannot be converted is an
		// error, not a quiet omission: dropping artwork silently is exactly
		// what this program refuses to do.
		if pic, err := embeddedPicture(input); err == nil && len(pic) > 0 && !opusencAccepts(pic) {
			jpg, err := reencodeToJPEG(ctx, o.FFmpegPath, pic)
			if err != nil {
				return fmt.Errorf("cover art for %s: %w", filepath.Base(o.Input), err)
			}
			picFile, err := os.CreateTemp("", "flacsync-cover-*.jpg")
			if err != nil {
				return fmt.Errorf("stage cover art: %w", err)
			}
			if _, werr := picFile.Write(jpg); werr != nil {
				picFile.Close()
				os.Remove(picFile.Name())
				return fmt.Errorf("stage cover art: %w", werr)
			}
			picFile.Close()
			defer os.Remove(picFile.Name())
			args = append(args, "--picture", "3||||"+picFile.Name())
		}

		args = append(args, input, tmp)
		cmd = exec.CommandContext(ctx, binOr(o.OpusencPath, "opusenc"), args...)
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
	hideWindow(cmd)

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

// decodeToFLAC converts a lossless source opusenc cannot read - m4a/ALAC and
// the other ffmpeg-decodable formats - into a temporary FLAC, tags and
// attached picture included. The caller removes the file via cleanup.
func decodeToFLAC(ctx context.Context, o Options, input string) (string, func(), error) {
	ffmpeg := binOr(o.FFmpegPath, "ffmpeg")
	if _, err := exec.LookPath(ffmpeg); err != nil {
		return "", nil, fmt.Errorf("cannot read %s files: decoding them needs ffmpeg on PATH (install ffmpeg, or keep the library in FLAC)", strings.TrimPrefix(strings.ToLower(filepath.Ext(input)), "."))
	}

	f, err := os.CreateTemp("", "flacsync-decode-*.flac")
	if err != nil {
		return "", nil, fmt.Errorf("stage decoded input: %w", err)
	}
	tmp := f.Name()
	f.Close()
	cleanup := func() { os.Remove(tmp) }

	// -c:v copy keeps the embedded cover byte-for-byte; -map_metadata carries
	// the tags. The video stream is optional, so sources without artwork are
	// not an error.
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", input,
		"-map", "0:a:0",
		"-map", "0:v:0?",
		"-map_metadata", "0",
		"-c:a", "flac",
		"-c:v", "copy",
		"-disposition:v", "attached_pic",
		"-f", "flac",
		tmp,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	hideWindow(cmd)

	if err := cmd.Run(); err != nil {
		cleanup()
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		return "", nil, fmt.Errorf("decode %s for opusenc: %w: %s", filepath.Base(input), err, lastLines(stderr.String(), 3))
	}

	// ffmpeg marks every picture it muxes as type 0 ("other"), which players
	// like foobar2000 refuse to show as album art. Fix the temporary copy up
	// before opusenc reads it; the source file is never modified.
	if err := promoteFrontCover(tmp); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("fix cover art type for %s: %w", filepath.Base(input), err)
	}
	return tmp, cleanup, nil
}

// ExtractCover writes the artwork embedded in input to cover.jpg in dir, and
// reports whether it created the file. An existing cover is left alone, and a
// source with no artwork is not an error.
//
// The picture is read out of the FLAC file directly (see embeddedPicture)
// rather than left for ffmpeg's own FLAC demuxer to find, because that
// demuxer only exposes an attached picture whose declared MIME type is on a
// short hardcoded allowlist (image/jpeg, image/png, ...). A WebP cover -
// among others - fails that check and is dropped with just a log warning,
// which previously meant a real cover was silently treated as "no artwork".
// Piping the raw bytes through image2pipe instead lets ffmpeg sniff the
// actual format from the content.
//
// The write goes through a unique temporary name because several workers can
// be encoding tracks from the same album at once.
func ExtractCover(ctx context.Context, ffmpegPath, input, dir string) (bool, error) {
	final := filepath.Join(dir, CoverFile)
	if _, err := os.Stat(final); err == nil {
		return false, nil
	}

	if !media.IsFLAC(input) {
		return extractCoverWithFFmpeg(ctx, ffmpegPath, input, final)
	}

	pic, err := embeddedPicture(input)
	if err != nil || len(pic) == 0 {
		// No attached picture, or the file couldn't be parsed: nothing to
		// do, and nothing worth logging.
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
		"-f", "image2pipe", "-i", "-",
		"-frames:v", "1",
		"-update", "1",
		"-f", "image2",
		"-c:v", "mjpeg",
		tmpName,
	)
	cmd.Stdin = bytes.NewReader(pic)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	hideWindow(cmd)

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		// A picture block was present but ffmpeg couldn't decode it (an
		// exotic or corrupt format): worth logging, unlike "no artwork".
		return false, fmt.Errorf("decode cover art: %w: %s", err, lastLines(stderr.String(), 3))
	}

	if info, err := os.Stat(tmpName); err != nil || info.Size() == 0 {
		return false, nil
	}
	if err := os.Rename(tmpName, final); err != nil {
		return false, fmt.Errorf("write %s: %w", final, err)
	}
	return true, nil
}

// extractCoverWithFFmpeg handles sources whose artwork lives in a container
// the FLAC parser cannot read (m4a/ALAC, WAV, ...): ffmpeg exposes the
// attached picture as a video stream, which is converted to the sidecar JPEG.
// A source without artwork is not an error.
func extractCoverWithFFmpeg(ctx context.Context, ffmpegPath, input, final string) (bool, error) {
	ffmpeg := binOr(ffmpegPath, "ffmpeg")
	if _, err := exec.LookPath(ffmpeg); err != nil {
		return false, nil
	}

	tmp, err := os.CreateTemp(filepath.Dir(final), ".cover-*.tmp")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)

	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", input,
		"-map", "0:v:0",
		"-frames:v", "1",
		"-update", "1",
		"-c:v", "mjpeg",
		"-f", "image2",
		tmpName,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	hideWindow(cmd)

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil // no attached picture
	}
	if info, err := os.Stat(tmpName); err != nil || info.Size() == 0 {
		return false, nil
	}
	if err := os.Rename(tmpName, final); err != nil {
		return false, fmt.Errorf("write %s: %w", final, err)
	}
	return true, nil
}

// reencodeToJPEG converts an embedded picture of some format opusenc won't
// accept natively (WebP, chiefly) into a JPEG. WebP is decoded in-process, so
// the format that actually shows up in real libraries needs no external tool;
// anything else is handed to ffmpeg when it is installed. As in ExtractCover,
// the ffmpeg bytes go in via image2pipe so it sniffs the real format from the
// content rather than trusting a declared MIME type.
func reencodeToJPEG(ctx context.Context, ffmpegPath string, pic []byte) ([]byte, error) {
	if isWebP(pic) {
		img, err := webp.Decode(bytes.NewReader(pic))
		if err != nil {
			return nil, fmt.Errorf("decode WebP cover art: %w", err)
		}
		var out bytes.Buffer
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 90}); err != nil {
			return nil, fmt.Errorf("encode cover art: %w", err)
		}
		return out.Bytes(), nil
	}

	if _, err := exec.LookPath(binOr(ffmpegPath, "ffmpeg")); err != nil {
		return nil, errors.New("cover art is not JPEG, PNG or GIF and ffmpeg is not installed to convert it")
	}

	cmd := exec.CommandContext(ctx, binOr(ffmpegPath, "ffmpeg"),
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "image2pipe", "-i", "-",
		"-frames:v", "1",
		"-update", "1",
		"-f", "mjpeg",
		"-",
	)
	cmd.Stdin = bytes.NewReader(pic)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	hideWindow(cmd)

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("reencode cover art: %w: %s", err, lastLines(stderr.String(), 3))
	}
	return out.Bytes(), nil
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
