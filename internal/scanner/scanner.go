// Package scanner implements the boot-time diff between the two libraries.
// It is also re-run on demand from the tray, and after a settings change.
package scanner

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"flacsync/internal/config"
	"flacsync/internal/job"
	"flacsync/internal/paths"
	"flacsync/internal/playlist"
)

// skew is the slack allowed when comparing modification times across the two
// playlist folders. Syncthing does not preserve sub-second timestamps on every
// filesystem, so an exact comparison would flap.
const skew = 2 * time.Second

// Stats summarises what a scan found.
type Stats struct {
	Converts  int
	Deletes   int
	Playlists int
}

// Result is the work a scan produced.
type Result struct {
	Jobs  []job.Job
	Stats Stats
}

// Scan walks all four roots and returns the jobs needed to bring the mirror
// back in line with the source.
func Scan(cfg config.Config) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}

	var res Result

	audio, err := scanAudio(cfg, &res.Stats)
	if err != nil {
		return res, err
	}
	res.Jobs = append(res.Jobs, audio...)

	lists, err := scanPlaylists(cfg, &res.Stats)
	if err != nil {
		return res, err
	}
	res.Jobs = append(res.Jobs, lists...)

	return res, nil
}

// scanAudio compares _lossless against _lossy_opus in both directions:
// missing or stale .opus files are queued for conversion, and orphaned .opus
// files whose .flac source is gone are queued for pruning.
func scanAudio(cfg config.Config, stats *Stats) ([]job.Job, error) {
	var jobs []job.Job

	err := paths.WalkFiles(cfg.LosslessDir, func(p string, d fs.DirEntry) error {
		if !paths.HasExt(p, ".flac") {
			return nil
		}
		dst, err := paths.Map(cfg.LosslessDir, cfg.LossyDir, p, ".flac", ".opus")
		if err != nil {
			return nil
		}

		src, err := d.Info()
		if err != nil {
			return nil
		}
		out, err := os.Stat(dst)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// never converted
		case err != nil:
			return nil
		case src.ModTime().After(out.ModTime()):
			// source re-tagged or replaced since the last conversion
		default:
			return nil
		}

		jobs = append(jobs, job.Job{Kind: job.Convert, Src: p, Dst: dst, Root: cfg.LossyDir})
		stats.Converts++
		return nil
	})
	if err != nil {
		return jobs, err
	}

	err = paths.WalkFiles(cfg.LossyDir, func(p string, d fs.DirEntry) error {
		if !paths.HasExt(p, ".opus") {
			return nil
		}
		src, err := paths.Map(cfg.LossyDir, cfg.LosslessDir, p, ".opus", ".flac")
		if err != nil {
			return nil
		}
		if paths.Exists(src) {
			return nil
		}
		jobs = append(jobs, job.Job{Kind: job.Delete, Dst: p, Root: cfg.LossyDir})
		stats.Deletes++
		return nil
	})

	return jobs, err
}

// scanPlaylists diffs the two playlist folders in both directions. Whichever
// copy is newer wins; a playlist that exists on only one side is copied to the
// other, which is how a playlist created on the phone reaches the desktop.
//
// Deletions are deliberately not propagated here. At boot there is no way to
// tell "the user deleted this on the phone" apart from "this has not synced
// yet", and guessing wrong destroys data. Live deletions seen by the watcher
// are unambiguous and do propagate.
func scanPlaylists(cfg config.Config, stats *Stats) ([]job.Job, error) {
	desktop, err := listPlaylists(cfg.PlaylistsDir)
	if err != nil {
		return nil, err
	}
	mobile, err := listPlaylists(cfg.MobilePlaylistsDir)
	if err != nil {
		return nil, err
	}

	var jobs []job.Job

	for rel, left := range desktop {
		right, ok := mobile[rel]
		if ok && !left.ModTime().After(right.ModTime().Add(skew)) {
			continue
		}
		jobs = append(jobs, job.Job{
			Kind:      job.Translate,
			Src:       filepath.Join(cfg.PlaylistsDir, rel),
			Dst:       filepath.Join(cfg.MobilePlaylistsDir, rel),
			Root:      cfg.MobilePlaylistsDir,
			Direction: job.ToMobile,
		})
		stats.Playlists++
	}

	for rel, right := range mobile {
		left, ok := desktop[rel]
		if ok && !right.ModTime().After(left.ModTime().Add(skew)) {
			continue
		}
		jobs = append(jobs, job.Job{
			Kind:      job.Translate,
			Src:       filepath.Join(cfg.MobilePlaylistsDir, rel),
			Dst:       filepath.Join(cfg.PlaylistsDir, rel),
			Root:      cfg.PlaylistsDir,
			Direction: job.ToDesktop,
		})
		stats.Playlists++
	}

	return jobs, nil
}

// listPlaylists maps every playlist under root to its FileInfo, keyed by the
// path relative to root.
func listPlaylists(root string) (map[string]os.FileInfo, error) {
	found := make(map[string]os.FileInfo)

	err := paths.WalkFiles(root, func(p string, d fs.DirEntry) error {
		if !playlist.IsPlaylist(p) {
			return nil
		}
		rel, err := paths.Rel(root, p)
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		found[rel] = info
		return nil
	})

	return found, err
}
