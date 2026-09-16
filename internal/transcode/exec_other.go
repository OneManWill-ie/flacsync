//go:build !windows

package transcode

import "os/exec"

// hideWindow is a no-op outside Windows: only Windows allocates a visible
// console for a child process that doesn't already have one to inherit.
func hideWindow(cmd *exec.Cmd) {}
