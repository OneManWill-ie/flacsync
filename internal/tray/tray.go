// Package tray is the entire user interface: an icon, a disabled status line,
// and four menu items. No windows, no GUI toolkit.
package tray

import (
	"github.com/getlantern/systray"

	"flacsync/internal/icon"
	"flacsync/internal/state"
)

// Actions are the callbacks the tray invokes. Any of them may be nil.
type Actions struct {
	// TogglePause flips the pause flag and returns the new value.
	TogglePause func() bool
	// Rescan triggers a full diff of both libraries.
	Rescan func()
	// Settings opens the folder pickers.
	Settings func()
	// Quit shuts the engine down. Called on the systray exit path.
	Quit func()
}

// Run takes over the calling goroutine and does not return until the user
// quits. It must be called from the main goroutine: on macOS and Windows the
// tray has to own the main thread.
func Run(st *state.State, a Actions) {
	systray.Run(func() { onReady(st, a) }, func() {
		if a.Quit != nil {
			a.Quit()
		}
	})
}

// Quit asks the tray to tear itself down, which unblocks Run.
func Quit() { systray.Quit() }

func onReady(st *state.State, a Actions) {
	systray.SetIcon(icon.Data())
	systray.SetTooltip("FLAC \u2192 Opus sync engine")

	mStatus := systray.AddMenuItem(st.Status(), "Current engine state")
	mStatus.Disable()

	systray.AddSeparator()
	mPause := systray.AddMenuItem("Pause", "Stop starting new conversions")
	mRescan := systray.AddMenuItem("Rescan library", "Diff both libraries now")
	mSettings := systray.AddMenuItem("Settings\u2026", "Choose the four library folders")

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Stop the engine and exit")

	// The status label is driven entirely by the state manager's channel, so
	// no part of the pipeline needs a reference to the menu item.
	go func() {
		for text := range st.Updates() {
			mStatus.SetTitle(text)
		}
	}()

	go func() {
		for {
			select {
			case <-mPause.ClickedCh:
				if a.TogglePause == nil {
					continue
				}
				if a.TogglePause() {
					mPause.SetTitle("Resume")
				} else {
					mPause.SetTitle("Pause")
				}

			case <-mRescan.ClickedCh:
				if a.Rescan != nil {
					go a.Rescan()
				}

			case <-mSettings.ClickedCh:
				if a.Settings != nil {
					go a.Settings()
				}

			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}
