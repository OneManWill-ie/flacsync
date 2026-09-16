//go:build windows

package transcode

import (
	"os/exec"
	"syscall"
)

// hideWindow stops opusenc/ffmpeg from popping up a console window of its
// own when it's spawned. -H=windowsgui on the flacsync build only removes
// flacsync's own console; it does nothing for child processes. Since a GUI
// process has no console for a child to inherit, Windows allocates a brand
// new one for each os/exec.Command call - which is exactly the flurry of
// opusenc windows flashing open and closing during a batch of conversions.
// HideWindow alone isn't reliably enough to stop that new console from
// flashing briefly before it's hidden; pairing it with the CREATE_NO_WINDOW
// creation flag stops one from being allocated in the first place.
func hideWindow(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
