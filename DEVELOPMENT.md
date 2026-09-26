# SuperSync: developer notes

Go 1.26+, no cgo. Everything is one self-contained executable; the web UI is embedded.

## Building

```
scripts/build.sh v0.1      # Mac (universal), Windows and Linux executables in dist/
```

## Releases and self-update

Tag `vX.Y.Z` and run `scripts/build.sh vX.Y.Z` on a Mac with Xcode's command line tools and NSIS
installed (`brew install makensis`). It writes these files:

- the installers `SuperSync.dmg` and `SuperSync-Setup.exe`;
- `SuperSync-mac.zip` (holding `SuperSync.app`) and `SuperSync-windows.zip` (holding
  `SuperSync.exe`), which the updater uses.

Attach all four to a GitHub release. The app icon comes from `scripts/icon/main.go`
(`go run scripts/icon/main.go`, then `iconutil`, as the script's comment shows).

An installed Mac app updates by unpacking the new `SuperSync.app` beside itself and swapping the
two bundles. A bare executable (v0.1.1, or run from a terminal) takes just the executable from
inside the bundle. These names are what `internal/update` looks for.

A running copy checks `releases/latest` 5 s after starting and then every 6 h. When it finds a
newer version, it downloads the zip and checks the size and GitHub's SHA-256 digest. It then
extracts the executable next to itself as `.SuperSync-update-<tag>`, and runs `help` on it to make
sure it starts and reports the right version. **Update** waits for any library write in progress
and refuses while playlists are syncing. It then renames the new file over the old one and
restarts. On Mac this is `exec` in place, so the process and Terminal window stay the same. On
Windows the old `.exe` is renamed to `.old`, the new one is started in the same console, and the
`.old` file is removed on the next start. The new process takes over the same port, and the page
reloads once `/api/state` reports the new version. `SUPERSYNC_UPDATE_API` points the check at
another releases JSON, for testing.

## The app window, background mode and login item

`internal/window` shows the web UI in a native window. On macOS that's WKWebView through cgo
(`window_darwin.m`), which also provides a menu bar item and the app menus. On Windows it's Edge
WebView2 through `github.com/jchv/go-webview2`, which needs no cgo, with a notification-area icon
drawn with `Shell_NotifyIcon`. On any other system, or if WebView2 is missing, SuperSync falls back
to the browser. `SUPERSYNC_BROWSER=1` forces the browser.

Closing the window hides it, unless **Keep running** is off. `--background` starts with the window
hidden, which is how the login item starts it. That login item is `internal/autostart`: a Launch
Agent on macOS and the `HKCU\…\Run` value on Windows. A second launch asks the running copy to
show its window (`POST /api/focus`) and then exits. When there's no terminal, output goes to
`SuperSync.log` in the data folder.

## Tests
Go 1.26+, no cgo. `go test ./...` (the audio tests use ffmpeg when it's installed).
The rekordbox database tests use pyrekordbox's real test library (MIT). Download
`.testdata/rekordbox 6/master_locked.db`, `master_unlocked.db` and `backup/masterPlaylists6.xml`
from github.com/dylanljones/pyrekordbox and set `REKORDBOX_TESTDATA` to that folder.

```
internal/audio       tag + stream readers: MP3, FLAC, AIFF, WAV, M4A/ALAC, Ogg/Opus
internal/match       title normalization and scoring
internal/library     folder scan with incremental cache
internal/soundcloud  playlist fetching (public web API, auto client_id)
internal/rekordbox   collection XML reader, playlist XML writer
internal/sqlcipher   SQLCipher 4 page decryption/encryption (rekordbox's master.db)
internal/rbdb        rekordbox library: read tracks/playlists/cues, write playlists and tracks safely
internal/app         library source (rekordbox or SuperSync XML), SoundCloud import jobs, tiers
internal/analyze     duplicates, upgrades, quarantine/undo, cue transfer plans
internal/spectrum    encoder-cutoff detection for upscaled files
internal/dsp         FFT
internal/align       FFT cross-correlation to measure the offset between two recordings
internal/app         shared logic for the CLI and the web UI
internal/web         localhost server + the embedded single-page UI
```

The beatgrid/waveform tests (`internal/anlz`) use pyrekordbox's ANLZ test files; set `REKORDBOX_ANLZ` to the folder containing them.

## Command line

Everything in the app also works from a terminal: `supersync help`.

```
supersync library                                   # the library and its playlists
supersync import https://soundcloud.com/you/sets/friday
supersync apply                                     # finish an import that waited for rekordbox to close
supersync scan                                      # read files: quality tiers, upscale checks
supersync dupes                                     # list duplicates; add --move to move the extras
supersync cues --xml cues.xml                       # carry cue points to the copies you keep
supersync upgrades --min-kbps 320
```

## How SuperSync writes rekordbox's library

rekordbox's `master.db` is SQLCipher 4. SuperSync decrypts it in pure Go (`internal/sqlcipher`),
edits a temporary plain copy, runs `PRAGMA integrity_check`, re-encrypts it, and checks that it
decrypts again. Only then does it back up the live files (`master.db`, `-wal`, `-shm` and
`masterPlaylists6.xml`, keeping the last 10) and swap the new file in. Writes take turns under a
process-wide lock. A write is refused if rekordbox is running, or if the library file changed
since the write began. Restart-and-apply asks rekordbox to quit normally, waits 8 s, and then
force-quits it. This happens only after the user confirms in the UI.

## How matching works

SoundCloud titles are messy, so both sides are normalized before comparing:

- promo noise is dropped (`[FREE DL]`, `OUT NOW`, `PREMIERE:`, `Tech House |` prefixes, label names);
- leetspeak used to dodge copyright bots is decoded (`H0t 1n H3r3` → hot in here);
- accents, `feat.`, `&`/`x`, and one-letter typos are tolerated;
- the uploader is treated as a *possible* artist only, since it's often a channel or label;
- the version is kept: `(Hammer Remix)` is a different recording from the original or from
  `(Ewan McVicar Remix)`, while `(Original Mix)`, `(Extended Mix)` and `(Radio Edit)` count as the
  same song;
- file tags and the filename are both used, so untagged downloads still match.

### Spotting upscaled files

After a scan, every file that claims to be high quality (lossless, or 224 kbps and up) gets a
one-time spectrum check in the background. Lossy encoders cut the highest frequencies at a point set
by the bitrate: about 16–17 kHz for 128 kbps, 18.6 kHz for 192, and 20+ kHz for 256–320. A sharp
cliff below 19 kHz in a "320" or a WAV means it was made from a worse file. It's shown as e.g.
"MP3 320 → ~128", ranked as what it really is in Duplicates, and listed in Upgrades. Gentle
roll-offs (dark masters, old records) aren't flagged, because only a brick-wall cliff counts.
It can't catch a transcode from a source that had no lowpass. `supersync info FILE` shows the
check for any file.

### Cue timing

rekordbox skips an MP3's LAME/Xing header frame but keeps the encoder delay (about 25 ms), so
SuperSync decodes MP3s the same way. Get this wrong and cues move 26 ms, the classic
"Traktor → rekordbox" problem. MP3, FLAC, WAV and AIFF are decoded built-in. AAC, ALAC and Opus
need ffmpeg installed, and lossy AAC/Opus transfers are marked "check one cue" because rekordbox's
handling of their encoder lead-in isn't verified.

Duplicates use a stricter version of the same rules. Copies whose lengths differ by more than
15 seconds are flagged as "different lengths" (probably a radio edit and an extended mix) and are
never moved by "Move all".

## Where things are stored

- Settings and your Same/Not-same answers: `~/Library/Application Support/SuperSync/` (Mac),
  `%AppData%\SuperSync\` (Windows), `~/.config/SuperSync/` (Linux).
- The scan cache is in the matching cache folder. Delete it any time; it's rebuilt on the next scan.
- Moved duplicates, and `moves.jsonl` (the undo log), are in `<music folder>/_SuperSync Duplicates/`.

