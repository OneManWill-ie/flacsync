// Package config loads, validates and persists the on-disk configuration
// (config.json) that describes the four library roots and the tuning knobs
// for the sync engine.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrIncomplete is returned when one of the four required directories has not
// been chosen yet.
var ErrIncomplete = errors.New("configuration incomplete: all four library folders must be set")

// Config mirrors config.json one-to-one.
type Config struct {
	LosslessDir        string `json:"lossless_dir"`
	LossyDir           string `json:"lossy_dir"`
	PlaylistsDir       string `json:"playlists_dir"`
	MobilePlaylistsDir string `json:"mobile_playlists_dir"`

	Bitrate    string `json:"bitrate"`
	Workers    int    `json:"workers"`
	DebounceMS int    `json:"debounce_ms"`

	// Encoder is "auto", "ffmpeg" or "opusenc". Only opusenc carries embedded
	// cover art into the Opus file; "auto" prefers it when it is installed.
	Encoder     string `json:"encoder"`
	FFmpegPath  string `json:"ffmpeg_path"`
	OpusencPath string `json:"opusenc_path"`

	// CoverArt is "auto", "folder" or "none" and controls the cover.jpg
	// sidecar. "auto" writes one only when the encoder cannot embed artwork.
	CoverArt string `json:"cover_art"`
}

// Default returns tuning defaults with empty paths.
func Default() Config {
	return Config{
		Bitrate:     "192k",
		Workers:     defaultWorkers(),
		DebounceMS:  2000,
		Encoder:     "auto",
		FFmpegPath:  "ffmpeg",
		OpusencPath: "opusenc",
		CoverArt:    "auto",
	}
}

// defaultWorkers leaves half the CPUs free for the rest of the system.
func defaultWorkers() int {
	n := runtime.NumCPU() / 2
	if n < 1 {
		n = 1
	}
	return n
}

// DefaultPath is the per-user location of config.json, e.g.
// ~/.config/flacsync/config.json on Linux.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(dir, "flacsync", "config.json"), nil
}

// Load reads path. A missing file is not an error; defaults are returned so
// that a first run can go straight to the Settings dialog.
func Load(path string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Default(), fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.normalise()
	return cfg, nil
}

// Save writes the config atomically (temp file + rename) so a crash mid-write
// cannot leave a truncated config.json behind.
func (c Config) Save(path string) error {
	c.normalise()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// Dirs returns the four library roots in a stable order.
func (c Config) Dirs() []string {
	return []string{c.LosslessDir, c.LossyDir, c.PlaylistsDir, c.MobilePlaylistsDir}
}

// Complete reports whether every directory has been chosen.
func (c Config) Complete() bool {
	for _, d := range c.Dirs() {
		if d == "" {
			return false
		}
	}
	return true
}

// Debounce is the fsnotify quiet period.
func (c Config) Debounce() time.Duration {
	return time.Duration(c.DebounceMS) * time.Millisecond
}

// Validate checks that the four roots are set, distinct, and actually exist.
func (c Config) Validate() error {
	if !c.Complete() {
		return ErrIncomplete
	}
	seen := make(map[string]bool, 4)
	for _, d := range c.Dirs() {
		if seen[d] {
			return fmt.Errorf("directory %q is configured more than once", d)
		}
		seen[d] = true

		info, err := os.Stat(d)
		if err != nil {
			return fmt.Errorf("library folder %s: %w", d, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("library folder %s is not a directory", d)
		}
	}
	return nil
}

// normalise fills in blanks and makes every path absolute and clean, which the
// watcher relies on when it matches an event path back to its root.
func (c *Config) normalise() {
	if c.Bitrate == "" {
		c.Bitrate = "192k"
	}
	if c.Workers < 1 {
		c.Workers = defaultWorkers()
	}
	if c.DebounceMS < 100 {
		c.DebounceMS = 2000
	}
	if c.FFmpegPath == "" {
		c.FFmpegPath = "ffmpeg"
	}
	if c.OpusencPath == "" {
		c.OpusencPath = "opusenc"
	}
	switch strings.ToLower(c.Encoder) {
	case "ffmpeg", "opusenc", "auto":
		c.Encoder = strings.ToLower(c.Encoder)
	default:
		c.Encoder = "auto"
	}
	switch strings.ToLower(c.CoverArt) {
	case "folder", "none", "auto":
		c.CoverArt = strings.ToLower(c.CoverArt)
	default:
		c.CoverArt = "auto"
	}
	for _, p := range []*string{&c.LosslessDir, &c.LossyDir, &c.PlaylistsDir, &c.MobilePlaylistsDir} {
		if *p == "" {
			continue
		}
		if abs, err := filepath.Abs(*p); err == nil {
			*p = filepath.Clean(abs)
		}
	}
}

// Normalise is the exported form used after the settings dialog hands back
// raw, user-picked paths.
func (c *Config) Normalise() { c.normalise() }
