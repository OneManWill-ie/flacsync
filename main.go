// Command flacsync mirrors a lossless FLAC library into a mobile-friendly
// Opus library and keeps M3U playlists in sync in both directions.
//
// It is a tray application: main() loads the config, starts the pipeline
// (scanner, watcher, worker pool) on a background goroutine, and then hands
// the main goroutine to the system tray, which is where macOS and Windows
// insist the UI lives.
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/sqweek/dialog"

	"flacsync/internal/config"
	"flacsync/internal/playlist"
	"flacsync/internal/state"
	"flacsync/internal/transcode"
	"flacsync/internal/tray"
)

func main() {
	var (
		cfgFlag  = flag.String("config", "", "path to config.json (default: per-user config directory)")
		logFlag  = flag.String("log", "", "write logs to this file (default: stderr, or flacsync.log beside config.json when there is no console)")
		headless = flag.Bool("headless", false, "run without a tray icon (service mode)")
		verbose  = flag.Bool("v", false, "log every filesystem event and job")
	)
	flag.Parse()

	cfgPath := *cfgFlag
	if cfgPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			log.Fatalf("config: %v", err)
		}
		cfgPath = p
	}

	logger, closeLog := newLogger(*logFlag, cfgPath)
	defer closeLog()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Printf("config: %v (continuing with defaults)", err)
	}
	tool, terr := transcode.Select(cfg.Encoder, cfg.FFmpegPath, cfg.OpusencPath)
	if terr != nil {
		logger.Printf("warning: %v", terr)
		if !*headless {
			// Give the user one clear reason instead of an idle tray and a
			// log line they cannot see in a GUI build.
			dialog.Message("%s", terr.Error()).Title("No audio encoder").Error()
		}
	} else if !tool.EmbedsCoverArt() && cfg.CoverArt != "none" {
		logger.Printf("encoder %s cannot embed cover art; writing %s beside the tracks "+
			"(install opus-tools to embed it instead)", tool, transcode.CoverFile)
	}

	app := &App{
		cfg:     cfg,
		cfgPath: cfgPath,
		st:      state.New(),
		hashes:  playlist.NewHashStore(),
		logger:  logger,
		verbose: *verbose,
	}

	app.Start()

	if *headless {
		waitForSignal(logger)
		app.Shutdown()
		return
	}

	// Ctrl-C should close the tray cleanly rather than killing the process
	// out from under the pipeline.
	go func() {
		waitForSignal(logger)
		tray.Quit()
	}()

	// Blocks until the user picks Quit; the exit callback runs Shutdown.
	tray.Run(app.st, tray.Actions{
		TogglePause: app.TogglePause,
		Rescan:      app.Rescan,
		Settings:    app.Settings,
		Quit:        app.Shutdown,
	})
}

func waitForSignal(logger *log.Logger) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	s := <-sig
	logger.Printf("received %s, shutting down", s)
}
