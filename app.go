package main

import (
	"context"
	"errors"
	"log"
	"sync"

	"github.com/sqweek/dialog"

	"flacsync/internal/config"
	"flacsync/internal/job"
	"flacsync/internal/paths"
	"flacsync/internal/playlist"
	"flacsync/internal/scanner"
	"flacsync/internal/state"
	"flacsync/internal/watcher"
	"flacsync/internal/worker"
)

// App owns the pipeline and the lifecycle around it. Everything reachable from
// more than one goroutine is either behind App.mu or is itself thread-safe
// (state.State, playlist.HashStore, worker.Pool).
type App struct {
	st      *state.State
	hashes  *playlist.HashStore
	logger  *log.Logger
	verbose bool

	mu      sync.Mutex
	cfg     config.Config
	cfgPath string
	cancel  context.CancelFunc
	done    chan struct{}
	rescan  chan struct{}
}

// Config returns a copy of the live configuration.
func (a *App) Config() config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// Start brings the pipeline up for the current configuration. If the config is
// incomplete the app stays alive and idle, showing "Needs setup" in the tray
// until the user picks folders.
func (a *App) Start() {
	cfg := a.Config()

	a.st.Reset()
	a.st.SetConfigured(cfg.Complete())

	if err := cfg.Validate(); err != nil {
		a.logger.Printf("engine idle: %v", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	rescan := make(chan struct{}, 1)

	a.mu.Lock()
	a.cancel, a.done, a.rescan = cancel, done, rescan
	a.mu.Unlock()

	go func() {
		defer close(done)
		a.run(ctx, cfg, rescan)
	}()
}

// Stop cancels the pipeline and waits for it to drain.
func (a *App) Stop() {
	a.mu.Lock()
	cancel, done := a.cancel, a.done
	a.cancel, a.done, a.rescan = nil, nil, nil
	a.mu.Unlock()

	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// Shutdown stops the pipeline for good. It is safe to call twice, which
// matters because both the signal handler and the tray exit path reach it.
func (a *App) Shutdown() {
	a.Stop()
	a.st.Close()
}

// Restart reloads the pipeline after the configuration changed.
func (a *App) Restart() {
	a.Stop()
	a.Start()
}

// TogglePause flips the pause flag. Workers finish the job in hand and then
// park; nothing is lost.
func (a *App) TogglePause() bool {
	paused := a.st.TogglePause()
	if paused {
		a.logger.Print("paused")
	} else {
		a.logger.Print("resumed")
	}
	return paused
}

// Rescan asks the running pipeline for a full diff. It never blocks: if a
// rescan is already pending, this one is folded into it.
func (a *App) Rescan() {
	a.mu.Lock()
	ch := a.rescan
	a.mu.Unlock()

	if ch == nil {
		a.logger.Print("rescan ignored: engine is not running")
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// run is the pipeline: pool, watcher, initial scan, then the event loop.
func (a *App) run(ctx context.Context, cfg config.Config, rescan <-chan struct{}) {
	pool := worker.New(cfg, a.st, a.hashes, a.logger)
	pool.Start(ctx)
	defer pool.Wait()

	w, err := watcher.New(cfg, a.logger)
	if err != nil {
		a.logger.Printf("watcher: %v", err)
		return
	}
	defer w.Close()

	go func() {
		if err := w.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			a.logger.Printf("watcher stopped: %v", err)
		}
	}()

	a.fullScan(ctx, cfg, pool)

	for {
		select {
		case <-ctx.Done():
			return

		case <-rescan:
			a.fullScan(ctx, cfg, pool)

		case ev := <-w.Events():
			if a.verbose {
				a.logger.Printf("event: %s", ev)
			}
			for _, j := range a.jobsFor(cfg, ev) {
				if a.verbose {
					a.logger.Printf("queue: %s", j)
				}
				pool.Submit(ctx, j)
			}
		}
	}
}

func (a *App) fullScan(ctx context.Context, cfg config.Config, pool *worker.Pool) {
	res, err := scanner.Scan(cfg)
	if err != nil {
		a.logger.Printf("scan: %v", err)
		return
	}
	a.logger.Printf("scan: %d to convert, %d to prune, %d playlists",
		res.Stats.Converts, res.Stats.Deletes, res.Stats.Playlists)

	for _, j := range res.Jobs {
		if ctx.Err() != nil {
			return
		}
		pool.Submit(ctx, j)
	}
}

// jobsFor turns one debounced filesystem event into zero or more jobs. This is
// the only place that knows what each library root means.
func (a *App) jobsFor(cfg config.Config, ev watcher.Event) []job.Job {
	switch ev.Root {
	case watcher.RootLossless:
		return a.losslessJobs(cfg, ev.Path)
	case watcher.RootLossy:
		return a.lossyJobs(cfg, ev.Path)
	case watcher.RootPlaylists:
		return a.playlistJobs(ev.Path, cfg.PlaylistsDir, cfg.MobilePlaylistsDir, job.ToMobile)
	case watcher.RootMobilePlaylists:
		return a.playlistJobs(ev.Path, cfg.MobilePlaylistsDir, cfg.PlaylistsDir, job.ToDesktop)
	}
	return nil
}

// losslessJobs: a FLAC appeared or changed, so (re)encode it; a FLAC vanished,
// so drop its Opus twin.
func (a *App) losslessJobs(cfg config.Config, path string) []job.Job {
	if !paths.HasExt(path, ".flac") {
		return nil
	}
	dst, err := paths.Map(cfg.LosslessDir, cfg.LossyDir, path, ".flac", ".opus")
	if err != nil {
		return nil
	}
	if paths.Exists(path) {
		return []job.Job{{Kind: job.Convert, Src: path, Dst: dst, Root: cfg.LossyDir}}
	}
	return []job.Job{{Kind: job.Delete, Dst: dst, Root: cfg.LossyDir}}
}

// lossyJobs handles tampering with the mirror: a deleted .opus whose source
// still exists is re-encoded, and an .opus with no source is pruned.
func (a *App) lossyJobs(cfg config.Config, path string) []job.Job {
	if !paths.HasExt(path, ".opus") {
		return nil
	}
	src, err := paths.Map(cfg.LossyDir, cfg.LosslessDir, path, ".opus", ".flac")
	if err != nil {
		return nil
	}

	switch {
	case !paths.Exists(src) && paths.Exists(path):
		return []job.Job{{Kind: job.Delete, Dst: path, Root: cfg.LossyDir}}
	case paths.Exists(src) && !paths.Exists(path):
		return []job.Job{{Kind: job.Convert, Src: src, Dst: path, Root: cfg.LossyDir}}
	}
	return nil
}

// playlistJobs applies the loop guard before queueing a translation: if the
// file's current hash is one we wrote (or one we have already translated from),
// the event is our own echo coming back through Syncthing and is dropped.
func (a *App) playlistJobs(src, srcRoot, dstRoot string, dir job.Direction) []job.Job {
	if !playlist.IsPlaylist(src) {
		return nil
	}
	dst, err := paths.Map(srcRoot, dstRoot, src, "", "")
	if err != nil {
		return nil
	}

	if !paths.Exists(src) {
		a.hashes.Forget(src)
		return []job.Job{{Kind: job.Delete, Dst: dst, Root: dstRoot}}
	}

	sum, err := playlist.HashFile(src)
	if err != nil {
		return nil
	}
	if a.hashes.Matches(src, sum) {
		if a.verbose {
			a.logger.Printf("ignoring own write: %s", src)
		}
		return nil
	}

	return []job.Job{{Kind: job.Translate, Src: src, Dst: dst, Root: dstRoot, Direction: dir}}
}

// Settings walks the user through the four folder pickers and restarts the
// pipeline. Cancelling any dialog abandons the whole change.
func (a *App) Settings() {
	cfg := a.Config()

	prompts := []struct {
		title  string
		target *string
	}{
		{"Select the lossless (FLAC) library", &cfg.LosslessDir},
		{"Select the lossy (Opus) mirror", &cfg.LossyDir},
		{"Select the desktop playlist folder", &cfg.PlaylistsDir},
		{"Select the mobile playlist folder", &cfg.MobilePlaylistsDir},
	}

	for _, p := range prompts {
		picked, err := dialog.Directory().Title(p.title).Browse()
		if errors.Is(err, dialog.ErrCancelled) {
			a.logger.Print("settings cancelled")
			return
		}
		if err != nil {
			a.logger.Printf("folder picker: %v", err)
			return
		}
		*p.target = picked
	}

	cfg.Normalise()
	if err := cfg.Validate(); err != nil {
		a.logger.Printf("settings rejected: %v", err)
		dialog.Message("%s", err.Error()).Title("Invalid folders").Error()
		return
	}

	a.mu.Lock()
	a.cfg = cfg
	path := a.cfgPath
	a.mu.Unlock()

	if err := cfg.Save(path); err != nil {
		a.logger.Printf("save config: %v", err)
	}

	a.logger.Print("configuration updated, restarting engine")
	a.Restart()
}
