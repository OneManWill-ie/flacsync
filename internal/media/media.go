// Package media defines the source audio formats the engine treats as
// lossless and how their files map onto the Opus mirror.
package media

import (
	"os"
	"path/filepath"
	"strings"

	"flacsync/internal/paths"
)

// OpusExt is the extension of every mirrored file.
const OpusExt = ".opus"

// Lossless lists the source extensions that are converted into Opus, most
// preferred first. ffmpeg must be able to decode an extension before it can
// be added here, because opusenc cannot read any of them directly.
//
// When several files own the same Opus twin (song.flac and song.m4a in one
// folder), the first entry in this list wins, so the FLAC drives the mirror.
var Lossless = []string{".flac", ".m4a", ".aiff", ".aif", ".wav", ".ape", ".wv", ".tta"}

// IsLossless reports whether path has one of the source audio extensions.
func IsLossless(p string) bool { return hasExt(p, Lossless) }

// IsOpus reports whether path is a mirrored Opus file.
func IsOpus(p string) bool { return strings.EqualFold(filepath.Ext(p), OpusExt) }

// IsFLAC reports whether path is a native FLAC file, the one input format
// opusenc reads directly. Everything else goes through a decode step first.
func IsFLAC(p string) bool { return strings.EqualFold(filepath.Ext(p), ".flac") }

// OpusPath maps a lossless file under srcRoot to its Opus twin under dstRoot.
func OpusPath(srcRoot, dstRoot, src string) (string, error) {
	rel, err := paths.Rel(srcRoot, src)
	if err != nil {
		return "", err
	}
	return filepath.Join(dstRoot, replaceExt(rel, OpusExt)), nil
}

// SourcePath returns the lossless file an Opus twin was made from: the first
// existing candidate in preference order. ok is false when none of them is
// there, which is how the scanner spots orphaned mirror files.
func SourcePath(losslessRoot, lossyRoot, opus string) (string, bool) {
	rel, err := paths.Rel(lossyRoot, opus)
	if err != nil {
		return "", false
	}
	stem := strings.TrimSuffix(rel, filepath.Ext(rel))
	for _, ext := range Lossless {
		candidate := filepath.Join(losslessRoot, stem+ext)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

// PreferredSource returns the best existing source sharing p's Opus twin and
// is what keeps two source formats from fighting over one mirror file. It
// returns p itself unless a more preferred twin exists, so callers can
// ignore events for the losing format.
func PreferredSource(root, p string) string {
	ext := filepath.Ext(p)
	for _, candidate := range Lossless {
		if strings.EqualFold(candidate, ext) {
			return p
		}
		twin := replaceExt(p, candidate)
		if _, err := os.Stat(twin); err == nil {
			return twin
		}
	}
	return p
}

func hasExt(p string, exts []string) bool {
	ext := filepath.Ext(p)
	for _, e := range exts {
		if strings.EqualFold(ext, e) {
			return true
		}
	}
	return false
}

func replaceExt(p, ext string) string {
	old := filepath.Ext(p)
	if old == "" {
		return p + ext
	}
	return strings.TrimSuffix(p, old) + ext
}
