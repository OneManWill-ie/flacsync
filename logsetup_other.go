//go:build !windows

package main

// hasStderr is always true outside Windows: daemons and terminal runs have a
// usable stderr, so there is no silent output to work around.
func hasStderr() bool { return true }
