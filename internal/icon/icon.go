// Package icon holds the tray icon artwork, embedded so that the application
// ships as a single self-contained binary with no asset files.
//
// Windows wants an .ico; macOS and Linux are happy with a PNG.
package icon

import (
	_ "embed"
	"runtime"
)

var (
	//go:embed logo.png
	png []byte
	//go:embed logo.ico
	ico []byte
)

// Data returns the icon bytes in the format the current platform expects.
func Data() []byte {
	if runtime.GOOS == "windows" {
		return ico
	}
	return png
}

// PNG returns the icon as a PNG regardless of platform.
func PNG() []byte {
	return png
}
