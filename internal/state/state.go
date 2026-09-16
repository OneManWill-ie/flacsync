// Package state holds the shared, mutable state of the engine: how much work
// is queued, how much is in flight, and whether the user has paused things.
//
// Everything is guarded by a single mutex. A sync.Cond built on that same
// mutex lets worker goroutines block cheaply while paused instead of polling,
// and a coalescing channel pushes human-readable status text to the tray.
package state

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrClosed is returned by WaitWhilePaused once the engine is shutting down.
var ErrClosed = errors.New("state: closed")

// Snapshot is a consistent copy of the counters, safe to read without locking.
type Snapshot struct {
	Queued     int
	Active     int
	Done       int
	Failed     int
	Paused     bool
	Configured bool
}

// Remaining is the number of jobs not yet finished.
func (s Snapshot) Remaining() int { return s.Queued + s.Active }

// State is safe for concurrent use by every goroutine in the program.
type State struct {
	mu   sync.Mutex
	cond *sync.Cond

	queued     int
	active     int
	done       int
	failed     int
	paused     bool
	configured bool
	closed     bool

	updates chan string
	last    string
}

// New returns a State with a buffered, coalescing updates channel.
func New() *State {
	s := &State{updates: make(chan string, 1)}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Updates yields status strings such as "Status: Processing (14 left)".
// It is never closed; the tray reads from it for the life of the process.
func (s *State) Updates() <-chan string { return s.updates }

// SetConfigured records whether the four library folders have been chosen.
func (s *State) SetConfigured(ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configured = ok
	s.publishLocked()
}

// Enqueue records n newly queued jobs.
func (s *State) Enqueue(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queued += n
	s.publishLocked()
}

// Cancel drops n jobs that were queued but will never run (shutdown).
func (s *State) Cancel(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queued = clamp(s.queued - n)
	s.publishLocked()
}

// Begin moves one job from the queue into the active set.
func (s *State) Begin() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queued = clamp(s.queued - 1)
	s.active++
	s.publishLocked()
}

// Complete retires one active job, recording success or failure.
func (s *State) Complete(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = clamp(s.active - 1)
	if err != nil {
		s.failed++
	} else {
		s.done++
	}
	s.publishLocked()
}

// TogglePause flips the pause flag and returns the new value. Resuming wakes
// every worker blocked in WaitWhilePaused.
func (s *State) TogglePause() bool {
	s.mu.Lock()
	s.paused = !s.paused
	paused := s.paused
	s.publishLocked()
	s.mu.Unlock()

	s.cond.Broadcast()
	return paused
}

// Paused reports the current pause flag.
func (s *State) Paused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused
}

// WaitWhilePaused blocks until the engine is resumed, ctx is cancelled, or the
// state is closed. Workers call this before starting each job.
func (s *State) WaitWhilePaused(ctx context.Context) error {
	s.mu.Lock()
	if !s.paused {
		s.mu.Unlock()
		return ctx.Err()
	}

	// sync.Cond has no context support, so wake the waiter when ctx dies.
	stop := context.AfterFunc(ctx, s.cond.Broadcast)
	defer stop()

	for s.paused && !s.closed && ctx.Err() == nil {
		s.cond.Wait()
	}
	closed := s.closed
	s.mu.Unlock()

	if closed {
		return ErrClosed
	}
	return ctx.Err()
}

// Reset zeroes the counters, e.g. when the pipeline restarts after a settings
// change. The pause flag is deliberately preserved.
func (s *State) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queued, s.active, s.done, s.failed = 0, 0, 0, 0
	s.publishLocked()
}

// Close releases every waiter and marks the state as dead.
func (s *State) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cond.Broadcast()
}

// Snapshot copies the counters.
func (s *State) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		Queued:     s.queued,
		Active:     s.active,
		Done:       s.done,
		Failed:     s.failed,
		Paused:     s.paused,
		Configured: s.configured,
	}
}

// Status returns the text currently shown in the tray.
func (s *State) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func (s *State) statusLocked() string {
	remaining := s.queued + s.active
	switch {
	case !s.configured:
		return "Status: Needs setup"
	case s.paused && remaining > 0:
		return fmt.Sprintf("Status: Paused (%d left)", remaining)
	case s.paused:
		return "Status: Paused"
	case remaining > 0:
		return fmt.Sprintf("Status: Processing (%d left)", remaining)
	case s.failed > 0:
		return fmt.Sprintf("Status: Idle (%d failed)", s.failed)
	default:
		return "Status: Idle"
	}
}

// publishLocked pushes the status text to the tray. The channel holds one
// slot: if the tray has not drained the previous value we replace it, so the
// label always shows the newest text and nothing ever blocks on the mutex.
func (s *State) publishLocked() {
	text := s.statusLocked()
	if text == s.last {
		return
	}
	s.last = text

	select {
	case s.updates <- text:
		return
	default:
	}
	select {
	case <-s.updates:
	default:
	}
	select {
	case s.updates <- text:
	default:
	}
}

func clamp(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
