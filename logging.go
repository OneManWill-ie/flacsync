package main

import (
	"log"
	"os"
	"path/filepath"
)

// newLogger decides where log lines go. An explicit -log path always wins.
// Otherwise a Windows GUI build, which has no stderr to write to, falls back
// to flacsync.log beside config.json; every other mode keeps using stderr so
// systemd/launchd, pipes and 2>file redirects keep collecting the output.
func newLogger(explicit, cfgPath string) (*log.Logger, func()) {
	target := explicit
	if target == "" && !hasStderr() {
		target = filepath.Join(filepath.Dir(cfgPath), "flacsync.log")
	}
	if target == "" {
		return log.New(os.Stderr, "", log.LstdFlags), func() {}
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		logger := log.New(os.Stderr, "", log.LstdFlags)
		logger.Printf("log: %v (falling back to stderr)", err)
		return logger, func() {}
	}

	f, err := os.OpenFile(target, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		logger := log.New(os.Stderr, "", log.LstdFlags)
		logger.Printf("log: %v (falling back to stderr)", err)
		return logger, func() {}
	}

	logger := log.New(f, "", log.LstdFlags)
	logger.Printf("flacsync logging to %s", target)
	return logger, func() { f.Close() }
}
