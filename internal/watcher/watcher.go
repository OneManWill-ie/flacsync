// Package watcher wraps fsnotify with the three things the raw API does not
// give you: recursive watches, debouncing, and knowing which library root an
// event belongs to.
package watcher

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"flacsync/internal/config"
	"flacsync/internal/paths"
)

// Root identifies which of the four configured folders an event came from.
type Root int

const (
	RootUnknown Root = iota
	RootLossless
	RootLossy
	RootPlaylists
	RootMobilePlaylists
)

func (r Root) String() string {
	switch r {
	case RootLossless:
		return "lossless"
	case RootLossy:
		return "lossy"
	case RootPlaylists:
		return "playlists"
	case RootMobilePlaylists:
		return "mobile-playlists"
	default:
		return "unknown"
	}
}

// Event is a debounced change to a single file.
type Event struct {
	Path string
	Root Root
	Op   fsnotify.Op // union of every op seen during the debounce window
}

func (e Event) String() string {
	return fmt.Sprintf("%s %s (%s)", e.Root, e.Path, e.Op)
}

type pending struct {
	timer *time.Timer
	op    fsnotify.Op
	root  Root
}

// Watcher fans filesystem events from all four roots into a single channel.
type Watcher struct {
	cfg      config.Config
	fsw      *fsnotify.Watcher
	logger   *log.Logger
	debounce time.Duration

	events chan Event
	done   chan struct{}

	mu     sync.Mutex
	timers map[string]*pending
	closed bool
}

// New creates the underlying fsnotify watcher. Call Run to start delivering.
func New(cfg config.Config, logger *log.Logger) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}
	return &Watcher{
		cfg:      cfg,
		fsw:      fsw,
		logger:   logger,
		debounce: cfg.Debounce(),
		events:   make(chan Event, 256),
		done:     make(chan struct{}),
		timers:   make(map[string]*pending),
	}, nil
}

// Events is the debounced output stream. It is never closed; consumers should
// select on it together with their own context.
func (w *Watcher) Events() <-chan Event { return w.events }

// Run watches all four roots until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	for _, root := range w.cfg.Dirs() {
		if err := w.addRecursive(root); err != nil {
			w.logger.Printf("watch %s: %v", root, err)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case ev, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			w.handle(ev)

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			w.logger.Printf("watcher: %v", err)
		}
	}
}

// Close stops every pending timer and releases the fsnotify handles.
func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	for _, p := range w.timers {
		p.timer.Stop()
	}
	w.timers = make(map[string]*pending)
	w.mu.Unlock()

	close(w.done)
	return w.fsw.Close()
}

// handle applies the ignore rules, keeps the watch set up to date as folders
// appear, and hands everything else to the debouncer.
func (w *Watcher) handle(ev fsnotify.Event) {
	name := filepath.Clean(ev.Name)
	base := filepath.Base(name)

	// Syncthing writes each incoming file to a scratch name and renames it
	// into place, and ffmpeg stages through .tmp. Reacting to either would
	// mean transcoding a half-transferred file.
	if paths.IsTemp(base) || paths.SkipDir(base) {
		return
	}

	root := w.rootOf(name)
	if root == RootUnknown {
		return
	}

	if ev.Has(fsnotify.Create) {
		if info, err := os.Stat(name); err == nil && info.IsDir() {
			// A whole album can land at once. Watch the new folder, then
			// replay whatever is already inside it, because those files were
			// created before the watch existed.
			if err := w.addRecursive(name); err != nil {
				w.logger.Printf("watch %s: %v", name, err)
			}
			w.replay(name, root)
			return
		}
	}

	w.schedule(name, root, ev.Op)
}

// schedule starts or extends the quiet period for a path. Editors and
// Syncthing both emit a burst of WRITE events per file; only the last one
// matters.
func (w *Watcher) schedule(path string, root Root, op fsnotify.Op) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return
	}
	if p, ok := w.timers[path]; ok {
		p.op |= op
		p.timer.Reset(w.debounce)
		return
	}

	p := &pending{op: op, root: root}
	p.timer = time.AfterFunc(w.debounce, func() { w.fire(path) })
	w.timers[path] = p
}

func (w *Watcher) fire(path string) {
	w.mu.Lock()
	p, ok := w.timers[path]
	if !ok || w.closed {
		w.mu.Unlock()
		return
	}
	delete(w.timers, path)
	w.mu.Unlock()

	select {
	case w.events <- Event{Path: path, Root: p.root, Op: p.op}:
	case <-w.done:
	}
}

// replay schedules events for files that already exist inside a newly created
// directory.
func (w *Watcher) replay(dir string, root Root) {
	_ = paths.WalkFiles(dir, func(p string, d fs.DirEntry) error {
		w.schedule(p, root, fsnotify.Create)
		return nil
	})
}

// addRecursive registers dir and every subdirectory under it.
func (w *Watcher) addRecursive(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if p != filepath.Clean(dir) && paths.SkipDir(d.Name()) {
			return filepath.SkipDir
		}
		if err := w.fsw.Add(p); err != nil {
			w.logger.Printf("watch %s: %v", p, err)
		}
		return nil
	})
}

// rootOf attributes a path to the deepest configured root that contains it,
// so nesting one library inside another still resolves correctly.
func (w *Watcher) rootOf(p string) Root {
	candidates := []struct {
		dir  string
		root Root
	}{
		{w.cfg.LosslessDir, RootLossless},
		{w.cfg.LossyDir, RootLossy},
		{w.cfg.PlaylistsDir, RootPlaylists},
		{w.cfg.MobilePlaylistsDir, RootMobilePlaylists},
	}

	best := RootUnknown
	bestLen := -1
	for _, c := range candidates {
		if c.dir == "" || !paths.Under(c.dir, p) {
			continue
		}
		if len(c.dir) > bestLen {
			best, bestLen = c.root, len(c.dir)
		}
	}
	return best
}
