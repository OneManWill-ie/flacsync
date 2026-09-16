# flacsync

A background Go service that mirrors a lossless FLAC library into a
mobile-friendly Opus library, keeps M3U playlists in sync in both directions,
and stays out of the way in the system tray.

Transport between desktop and phone is Syncthing's job. This program only cares
about the four folders Syncthing sees.

## Build

```sh
go mod tidy          # resolves the indirect dependencies
go build -o flacsync.exe .
```

The Windows executable includes the application icon. If `logo.ico` changes,
regenerate the Windows resource before building:

```powershell
windres '--preprocessor=gcc -E -xc -DRC_INVOKED' app.rc -O coff -o flacsync_windows_amd64.syso
go build -o flacsync.exe .
```

Requires Go 1.22+ and an encoder on `PATH`:

- **`opusenc`** (from `opus-tools`) — recommended. Carries tags *and* embedded
  cover art into the Opus files.
- **`ffmpeg`** with `libopus` — always works, but cannot embed cover art (see
  below). Also needed for the `cover.jpg` sidecar and nothing else.

`"encoder": "auto"` picks opusenc when it is installed and falls back to
ffmpeg.

Platform packages needed by the tray and the folder picker, both of which use
cgo:

| Platform | Packages |
| --- | --- |
| Debian/Ubuntu | `libgtk-3-dev libayatana-appindicator3-dev` |
| Fedora | `gtk3-devel libappindicator-gtk3-devel` |
| macOS | Xcode command line tools |
| Windows | nothing (uses the Win32 API directly) |

On Windows, build with `-ldflags -H=windowsgui` to suppress the console window.

## Run

```sh
./flacsync              # tray mode
./flacsync -headless    # no tray, for a systemd user unit or launchd job
./flacsync -v           # log every filesystem event and job
./flacsync -config /path/to/config.json
```

First launch has no folders configured, so the tray shows `Status: Needs setup`.
Open **Settings…** and pick the four directories; the engine restarts itself as
soon as they are saved.

### Start Automatically on Windows

To start flacsync when you sign in to Windows:

1. Press `Win + R`, enter `shell:startup`, and press Enter.
2. Create a shortcut in the folder that points to `flacsync.exe`.
3. In the shortcut properties, set **Start in** to the folder containing
  `flacsync.exe`.

For this project, the target can be:

```text
C:\Users\<username>\Documents\_misc\coding_projects\flacsync\flacsync.exe
```

No startup or boot arguments are required. To avoid a console window, build the
Windows executable with `-ldflags -H=windowsgui` as described above.

### config.json

Written to the per-user config directory (`~/.config/flacsync/config.json` on
Linux, `~/Library/Application Support/flacsync/` on macOS,
`%AppData%\flacsync\` on Windows).

```json
{
  "lossless_dir": "/music/_lossless",
  "lossy_dir": "/music/_lossy_opus",
  "playlists_dir": "/music/_playlists",
  "mobile_playlists_dir": "/music/_mobileplaylists",
  "bitrate": "192k",
  "workers": 4,
  "debounce_ms": 2000,
  "encoder": "auto",
  "ffmpeg_path": "ffmpeg",
  "opusenc_path": "opusenc",
  "cover_art": "auto"
}
```

`workers` defaults to half the CPU count, leaving headroom for everything else
on the machine. `encoder` is `auto`, `ffmpeg` or `opusenc`; `cover_art` is
`auto`, `folder` or `none`.

## Layout

```
main.go                    flags, signals, hands the main goroutine to the tray
app.go                     pipeline lifecycle, event routing, settings dialog
internal/config            config.json load/save/validate
internal/state             counters, pause flag, status channel  (Module A)
internal/tray              systray menu                          (Module B)
internal/scanner           boot-time diff of both libraries      (Module C)
internal/watcher           fsnotify + recursive + debounce        (Module D)
internal/worker            worker pool                           (Module E)
internal/playlist          M3U translation + SHA256 loop guard   (Module F)
internal/ffmpeg            the transcode command
internal/paths             mirror path mapping, ignore rules
internal/icon              embedded tray icon
```

## How it behaves

**Startup diff.** Walks all four roots. A FLAC with no Opus twin, or one newer
than its twin, is queued for conversion. An Opus file whose FLAC source is gone
is deleted, and any directories that leaves empty are removed.

**Watching.** Every root is watched recursively; new subdirectories are picked
up as they appear, and the files already inside them are replayed (an album can
land all at once before the watch exists). Events are debounced for two seconds
because editors and Syncthing both emit a burst of writes per file.

**Ignored files.** Anything ending in `.tmp` or `~`, plus `.syncthing.*`,
`~syncthing~*`, and the `.stfolder` / `.stversions` trees. Our own output is
staged through `.tmp` and renamed into place, so a half-written file is never
visible and never triggers work.

**Pausing.** Workers park in `sync.Cond.Wait` between jobs rather than polling.
The job in flight finishes; the queue is preserved.

**Loop prevention.** Every playlist the engine writes has its SHA256 recorded,
along with the source it was translated from. When a watcher event arrives, the
file is hashed and compared: a match means this is our own write coming back
through Syncthing, and the event is dropped. Anything else is a real edit.
Translation is also idempotent, so even if a hash is missed the two sides
converge instead of oscillating.

**Playlist entries.** Rewritten relative to the playlist's own location with
forward slashes. Four entry forms are understood: relative to the playlist
file, relative to the music root, a foreign absolute path such as
`/storage/emulated/0/Music/…` or `/sdcard/Music/…` (matched by its trailing
components), and a Windows path with backslashes, including UNC shares — which
matters when the engine runs on Linux and the playlist came from a Windows
player. An entry that cannot be placed inside the library keeps its shape but
still gets the right extension, and stream URLs pass through untouched.

No list of known path prefixes is needed: a candidate only wins when the file
it points at is really there, so a wrong guess cannot silently invent a path.

**Playlist file names are never changed.** The two sides are paired by relative
path, so renaming `My Playlist.m3u` to `my_playlist.m3u8` during translation
would make the engine see two unrelated playlists and copy them back and forth
forever. Normalise names once on the desktop side instead.

## Deliberate choices worth knowing about

- **Playlist deletions do not propagate at startup**, only when the watcher
  sees them live. At boot there is no way to distinguish "deleted on the phone"
  from "not synced yet", and guessing wrong destroys a playlist.
- **Cover art depends on the encoder.** ffmpeg silently discards the attached
  picture when muxing Ogg Opus and will not write the `METADATA_BLOCK_PICTURE`
  comment that Opus files use for artwork; `-vn` just makes that explicit.
  opusenc carries it across. With ffmpeg, `cover_art: "auto"` writes a
  `cover.jpg` beside the tracks instead, which mainstream mobile players fall
  back to. Tags survive either way, as stream-level Vorbis comments.
- **Sidecar covers are cleaned up.** When the last track in a mirrored folder
  is deleted, any `cover.jpg` / `folder.jpg` left beside it goes too, otherwise
  it would keep empty album folders alive forever.
- **Modification times drive the audio diff.** A re-tagged FLAC is re-encoded.
  Restoring old files from a backup that preserves timestamps will not trigger
  one; use **Rescan library** after deleting the stale Opus if that happens.
- **Manual edits in the Opus mirror are undone.** Delete an `.opus` whose
  source still exists and it is re-encoded; add one with no source and it is
  pruned. The mirror is derived state, not a second library.
- **The folder picker runs off the main thread.** `systray` owns the main
  goroutine, so the dialog is opened from a separate one. This is fine on Linux
  and Windows; on macOS some GTK/Cocoa dialog builds insist on the main thread,
  so if Settings misbehaves there, edit `config.json` directly and restart.

## Tests

```sh
go test ./...
```

Covers playlist translation across all the entry forms, round-trip stability,
the loop guard, path mapping, and empty-directory pruning.
