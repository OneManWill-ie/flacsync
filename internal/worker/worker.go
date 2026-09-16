// Package worker implements the conversion pool: a fixed number of goroutines
// draining a job channel, each of which parks itself while the engine is
// paused.
package worker

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"flacsync/internal/config"
	"flacsync/internal/job"
	"flacsync/internal/paths"
	"flacsync/internal/playlist"
	"flacsync/internal/state"
	"flacsync/internal/transcode"
)

// queueDepth is generous: a first-run scan of a large library can enqueue
// thousands of jobs before any worker finishes one.
const queueDepth = 8192

// Pool owns the worker goroutines and the job channel.
type Pool struct {
	cfg    config.Config
	state  *state.State
	hashes *playlist.HashStore
	logger *log.Logger

	tool       transcode.Tool
	writeCover bool

	jobs chan job.Job
	wg   sync.WaitGroup

	mu       sync.Mutex
	inflight map[string]struct{}
}

// New builds a pool sized by cfg.Workers and resolves the encoder once, so a
// missing binary is reported at startup rather than once per track.
func New(cfg config.Config, st *state.State, hashes *playlist.HashStore, logger *log.Logger) *Pool {
	tool, err := transcode.Select(cfg.Encoder, cfg.FFmpegPath, cfg.OpusencPath)
	if err != nil {
		logger.Printf("encoder: %v", err)
	}

	// "auto" only writes the sidecar when the encoder cannot embed artwork,
	// so switching to opusenc silently stops littering the mirror.
	writeCover := cfg.CoverArt == "folder" ||
		(cfg.CoverArt == "auto" && !tool.EmbedsCoverArt())

	return &Pool{
		cfg:        cfg,
		state:      st,
		hashes:     hashes,
		logger:     logger,
		tool:       tool,
		writeCover: writeCover,
		jobs:       make(chan job.Job, queueDepth),
		inflight:   make(map[string]struct{}),
	}
}

// Start launches the workers. They run until ctx is cancelled.
func (p *Pool) Start(ctx context.Context) {
	for i := 0; i < p.cfg.Workers; i++ {
		p.wg.Add(1)
		go p.run(ctx, i+1)
	}

	art := "embedded"
	if !p.tool.EmbedsCoverArt() {
		art = "dropped"
		if p.writeCover {
			art = "sidecar " + transcode.CoverFile
		}
	}
	p.logger.Printf("worker pool: %d goroutines, %s via %s, cover art %s",
		p.cfg.Workers, p.cfg.Bitrate, p.tool, art)
}

// Wait blocks until every worker has returned.
func (p *Pool) Wait() { p.wg.Wait() }

// Submit queues a job. A job identical to one already queued or running is
// dropped, which is what keeps a burst of filesystem events from transcoding
// the same track several times over.
func (p *Pool) Submit(ctx context.Context, j job.Job) {
	key := j.Key()

	p.mu.Lock()
	if _, dup := p.inflight[key]; dup {
		p.mu.Unlock()
		return
	}
	p.inflight[key] = struct{}{}
	p.mu.Unlock()

	p.state.Enqueue(1)

	select {
	case p.jobs <- j:
	case <-ctx.Done():
		p.release(key)
		p.state.Cancel(1)
	}
}

func (p *Pool) release(key string) {
	p.mu.Lock()
	delete(p.inflight, key)
	p.mu.Unlock()
}

func (p *Pool) run(ctx context.Context, id int) {
	defer p.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case j := <-p.jobs:
			// Park here rather than at the channel read, so the queue keeps
			// draining into the workers and the "left" count stays honest.
			if err := p.state.WaitWhilePaused(ctx); err != nil {
				p.release(j.Key())
				p.state.Cancel(1)
				return
			}

			p.state.Begin()
			err := p.handle(ctx, j)
			p.release(j.Key())
			p.state.Complete(err)

			if err != nil && !errors.Is(err, context.Canceled) {
				p.logger.Printf("worker %d: %v", id, err)
			}
		}
	}
}

func (p *Pool) handle(ctx context.Context, j job.Job) error {
	switch j.Kind {
	case job.Convert:
		return p.convert(ctx, j)

	case job.Delete:
		return p.remove(j)

	case job.Translate:
		return playlist.Translate(j.Src, j.Dst, p.mapping(j.Direction), p.hashes)

	default:
		return errors.New("unknown job kind")
	}
}

func (p *Pool) convert(ctx context.Context, j job.Job) error {
	if err := transcode.Convert(ctx, transcode.Options{
		Tool:        p.tool,
		FFmpegPath:  p.cfg.FFmpegPath,
		OpusencPath: p.cfg.OpusencPath,
		Input:       j.Src,
		Output:      j.Dst,
		Bitrate:     p.cfg.Bitrate,
	}); err != nil {
		return err
	}

	if !p.writeCover {
		return nil
	}
	// Best effort: missing artwork is not a failed conversion, and the check
	// short-circuits on a stat once the album's first track is done.
	if _, err := transcode.ExtractCover(ctx, p.cfg.FFmpegPath, j.Src, filepath.Dir(j.Dst)); err != nil {
		p.logger.Printf("cover art for %s: %v", filepath.Base(j.Src), err)
	}
	return nil
}

// remove deletes a mirrored file and tidies up what it leaves behind, so a
// deleted album does not leave an empty folder tree on the phone.
func (p *Pool) remove(j job.Job) error {
	if err := os.Remove(j.Dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	p.hashes.Forget(j.Dst)

	dir := filepath.Dir(j.Dst)
	removeOrphanArt(dir)
	return paths.PruneEmptyDirs(dir, j.Root)
}

// removeOrphanArt deletes a cover image once the last track beside it is gone.
// Without this the sidecar would keep otherwise-empty album folders alive
// forever and pruning would never reach them.
func removeOrphanArt(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	var art []string
	for _, e := range entries {
		if e.IsDir() {
			return // a subdirectory still holds content
		}
		switch strings.ToLower(e.Name()) {
		case "cover.jpg", "cover.png", "folder.jpg", "folder.png":
			art = append(art, e.Name())
		default:
			if !paths.IsTemp(e.Name()) {
				return // a real file remains
			}
		}
	}

	for _, name := range art {
		os.Remove(filepath.Join(dir, name))
	}
}

// mapping turns a job direction into the concrete roots and extensions the
// playlist translator needs.
func (p *Pool) mapping(d job.Direction) playlist.Mapping {
	toMobile := playlist.Mapping{
		SrcAudioRoot: p.cfg.LosslessDir,
		DstAudioRoot: p.cfg.LossyDir,
		SrcExt:       ".flac",
		DstExt:       ".opus",
	}
	if d == job.ToDesktop {
		return toMobile.Invert()
	}
	return toMobile
}
