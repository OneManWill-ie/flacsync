// Package job defines the unit of work that the scanner and the watcher push
// onto the worker pool's channel.
package job

import "fmt"

// Kind selects what a worker should do with a Job.
type Kind int

const (
	// Convert transcodes Src (FLAC) into Dst (Opus).
	Convert Kind = iota
	// Delete removes Dst and prunes any directories it leaves empty.
	Delete
	// Translate rewrites the playlist at Src into Dst for the other library.
	Translate
)

func (k Kind) String() string {
	switch k {
	case Convert:
		return "convert"
	case Delete:
		return "delete"
	case Translate:
		return "translate"
	default:
		return "unknown"
	}
}

// Direction tells a Translate job which way the playlist is travelling.
type Direction int

const (
	// ToMobile rewrites desktop FLAC entries into mobile Opus entries.
	ToMobile Direction = iota
	// ToDesktop rewrites mobile Opus entries back into desktop FLAC entries.
	ToDesktop
)

func (d Direction) String() string {
	if d == ToDesktop {
		return "to-desktop"
	}
	return "to-mobile"
}

// Job is immutable once submitted.
type Job struct {
	Kind      Kind
	Src       string    // source file (empty for Delete)
	Dst       string    // file to write or remove
	Root      string    // library root that owns Dst, used to bound pruning
	Direction Direction // only meaningful for Translate
}

// Key identifies the work so the pool can drop duplicate events for a file
// that is already queued.
func (j Job) Key() string {
	return fmt.Sprintf("%d|%s|%s", j.Kind, j.Src, j.Dst)
}

func (j Job) String() string {
	if j.Kind == Delete {
		return fmt.Sprintf("delete %s", j.Dst)
	}
	return fmt.Sprintf("%s %s -> %s", j.Kind, j.Src, j.Dst)
}
