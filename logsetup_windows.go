//go:build windows

package main

import "syscall"

// hasStderr reports whether the process was given a usable stderr. A build
// made with -ldflags "-H=windowsgui" starts with no standard handles at all,
// so anything written to stderr is discarded. A console window, a pipe and a
// 2>file redirect all leave a valid handle behind, and in those cases the
// output should go to stderr as usual.
func hasStderr() bool {
	h, err := syscall.GetStdHandle(syscall.STD_ERROR_HANDLE)
	return err == nil && h != 0 && h != syscall.InvalidHandle
}
