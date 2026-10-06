# AGENTS.md

flacsync is a single-module Go tray app (`module flacsync`) that mirrors a
lossless audio library (FLAC, ALAC/m4a, and other ffmpeg-readable formats)
into Opus and keeps M3U playlists synced between a desktop and a mobile folder. `README.md` is the behavior/design reference:
read it before touching sync semantics — many suspicious-looking behaviors
there (startup playlist deletions, `cover_art` fallbacks, mtime-based diffs)
are deliberate.

## Build, test, verify

- `go build -o flacsync.exe .`; add `-ldflags "-H=windowsgui"` for the tray build with no console. `build.ps1` / `build.cmd` wrap this: running-process guard, icon regeneration, and `-Console`/`-Release`/`-Test`/`-Run`/`-Out` switches.
- `flacsync.exe` is gitignored; `flacsync_windows_amd64.syso` stays tracked because it supplies the Windows icon. Regenerate the syso with windres (README has the command) only when `internal/icon/logo.ico` changes.
- `go test ./...` passes. Only `internal/playlist` has tests (4 rewrite cases using temp dirs; no encoder, GUI, or fixtures needed). Single test: `go test ./internal/playlist -run <TestName>`.
- `go vet ./...` is clean; there is no lint config or Makefile. CI (`.github/workflows/ci.yml`) runs vet + tests on windows-latest; `release.yml` builds native Windows/Linux/macOS archives on `v*` tags.
- cgo is required on Linux/macOS (systray + folder picker), so `GOOS=linux CGO_ENABLED=0` builds fail; Windows uses the Win32 API directly. Cross-compiling from Windows needs a C cross-toolchain.

## Architecture map

- `main.go` parses flags, loads config, starts `App`, then hands the main goroutine to `systray` — the tray must own the main thread on macOS/Windows.
- `app.go` owns pipeline lifecycle: `App.run` wires worker pool -> watcher -> initial `scanner.Scan` -> event loop; `jobsFor` is the only place that maps a watcher root to jobs. `App.Settings` opens folder pickers on a separate goroutine.
- Flow: `internal/scanner` (boot diff) and `internal/watcher` (recursive fsnotify + debounce) emit `job.Job`; `internal/worker.Pool` executes them via `internal/transcode` (audio) or `playlist.Translate`. `internal/state` holds counters/pause and feeds tray status through a coalescing channel.
- `internal/paths` is the single source of truth for root-to-root path mapping and ignore rules (`.tmp`, `~`, Syncthing scratch, `.stfolder`). Reuse `paths.IsTemp`/`paths.SkipDir` instead of re-listing scratch names.
- `internal/media` owns the lossless extension list (`Lossless`), the Opus-twin path mapping, and the rule for which format wins when two share a twin. Add a new format there, not as `.flac`/`.m4a` special cases in `app.go` or the scanner.
- Every write stages as `<dst>.tmp` then renames into place; mirror deletes prune empty dirs and orphan `cover.jpg`s. The scanner and watcher assume this, so new write paths must do the same.

## Invariants that will bite

- Playlist ping-pong is prevented by `playlist.HashStore`: `Translate` records the SHA256 of both the file it read and the bytes it wrote, and the watcher drops events whose hash matches. Any new playlist writer must record its output in the store too.
- Playlist files are paired by relative path and must never be renamed/retitled during translation; renaming creates two unrelated playlists that copy back and forth forever.
- `playlist.Mapping` carries extension sets (`SrcExts`/`DstExts`), not single extensions. Desktop → mobile always lands on `.opus`; the reverse picks whichever lossless twin exists on disk.
- opusenc is required: `"auto"` refuses to run without it because ffmpeg cannot carry artwork into Ogg Opus. `transcode/picture.go` reads the FLAC PICTURE block directly, WebP covers are converted in-process (`x/image/webp`), and an unconvertible picture fails the track rather than being dropped. Non-FLAC sources are decoded to a temporary FLAC by ffmpeg (tags + attached picture) before opusenc runs; `promoteFrontCover` re-tags its type-0 pictures as front cover, because ffmpeg writes them as `Other` and players like foobar2000 ignore those.
- For a terminal smoke run use `-headless -v`; default mode blocks on the tray. Conversion needs `opusenc`, plus `ffmpeg` for non-FLAC sources or the explicit ffmpeg encoder; without an encoder the engine parks at `Status: Encoder unavailable`. GUI builds log to `flacsync.log` beside `config.json` when stderr is absent.
