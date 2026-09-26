# SuperSync: developer notes

Go 1.26+, no cgo. Everything is one self-contained executable; the web UI is embedded.

## Building

```
scripts/build.sh v0.1      # Mac (universal), Windows and Linux executables in dist/
```

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

