// Package paths contains the rules for translating a file path from one
// mirrored library root to the other, plus the ignore rules that keep
// Syncthing's scratch files out of the pipeline.
package paths

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Map translates src (an absolute path under srcRoot) into the equivalent path
// under dstRoot, swapping srcExt for dstExt. Passing empty extensions keeps
// the file name unchanged, which is what playlists need.
func Map(srcRoot, dstRoot, src, srcExt, dstExt string) (string, error) {
	rel, err := Rel(srcRoot, src)
	if err != nil {
		return "", err
	}
	return filepath.Join(dstRoot, SwapExt(rel, srcExt, dstExt)), nil
}

// Rel returns p relative to root, refusing anything that escapes root.
func Rel(root, p string) (string, error) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	if err != nil {
		return "", fmt.Errorf("relativise %s against %s: %w", p, root, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside %s", p, root)
	}
	return rel, nil
}

// Under reports whether p lies inside root.
func Under(root, p string) bool {
	if root == "" {
		return false
	}
	_, err := Rel(root, p)
	return err == nil
}

// SwapExt replaces a trailing from extension with to, case-insensitively.
// A path whose extension does not match is returned untouched.
func SwapExt(p, from, to string) string {
	if from == "" || to == "" {
		return p
	}
	if !strings.EqualFold(filepath.Ext(p), from) {
		return p
	}
	return strings.TrimSuffix(p, filepath.Ext(p)) + to
}

// HasExt reports whether p ends in ext, ignoring case.
func HasExt(p, ext string) bool {
	return strings.EqualFold(filepath.Ext(p), ext)
}

// IsTemp reports whether a file name is scratch output that must never enter
// the pipeline: our own ".tmp" staging files, Syncthing's transfer files, and
// the usual editor backups.
func IsTemp(name string) bool {
	lower := strings.ToLower(filepath.Base(name))
	switch {
	case strings.HasSuffix(lower, ".tmp"):
		return true
	case strings.HasSuffix(lower, "~"):
		return true
	case strings.HasPrefix(lower, ".syncthing."):
		return true
	case strings.HasPrefix(lower, "~syncthing~"):
		return true
	case strings.HasPrefix(lower, ".goutputstream"):
		return true
	}
	return false
}

// SkipDir reports whether a directory should never be walked or watched.
func SkipDir(name string) bool {
	switch strings.ToLower(filepath.Base(name)) {
	case ".stfolder", ".stversions", ".git", ".svn", "@eadir", ".trash", ".trash-1000":
		return true
	}
	return false
}

// PruneEmptyDirs walks up from start removing directories that have become
// empty, stopping at (and never removing) root.
func PruneEmptyDirs(start, root string) error {
	if root == "" {
		return nil
	}
	root = filepath.Clean(root)
	dir := filepath.Clean(start)

	for dir != root && Under(root, dir) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil // already gone, or not readable: nothing to prune
		}
		if len(entries) > 0 {
			return nil
		}
		if err := os.Remove(dir); err != nil {
			return nil
		}
		dir = filepath.Dir(dir)
	}
	return nil
}

// Exists reports whether a path currently resolves to something.
func Exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// WalkFiles calls fn for every regular file under root, skipping the
// directories and scratch files the engine ignores. Unreadable subtrees are
// stepped over rather than aborting the walk.
func WalkFiles(root string, fn func(path string, d fs.DirEntry) error) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != filepath.Clean(root) && SkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || IsTemp(d.Name()) {
			return nil
		}
		return fn(p, d)
	})
}
