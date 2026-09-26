# SuperSync

Your rekordbox library, in step with your SoundCloud playlists. SuperSync opens rekordbox's own
library directly: tracks, playlists, hot cues, memory cues and loops. It plays your tracks, and it
turns SoundCloud playlists into rekordbox playlists in one go.

- **Library**: browse the collection and playlists, search, and play tracks with a waveform that
  shows every hot cue (A–H), memory cue and loop. Click a cue to play from it; keys 1–8 jump to
  hot cues.
- **Quality tiers**: every track is graded **LOW** (under 128 kbps), **NORMAL** (128), **HQ** (256)
  or **UHQ** (over 256 or lossless). Files that were converted up from a worse copy are graded as
  what they really are.
- **Import a SoundCloud playlist**: SuperSync creates the playlist inside a *SoundCloud* folder in
  your rekordbox library:
  - Tracks you already have are linked.
  - Tracks you don't have are downloaded and added: the artist's original file when they turned
    on SoundCloud's download button and you've set your SoundCloud login token, otherwise the
    MP3 stream (usually 128 kbps, so it shows as a rip worth upgrading).
  - Tracks with no MP3 stream (a few only stream as AAC/Opus, or as a 30 s preview) are listed
    with their free-download or buy link.
  In playlists imported from SoundCloud, any track below UHQ gets a button that goes where
  SoundCloud's own button goes: the track's download (SoundCloud requires a login there) or the
  buy / free-download link.
- **Download a playlist**: `supersync download <url>` saves a whole SoundCloud or YouTube
  playlist as tagged MP3s into *SoundCloud/<playlist>* or *YouTube/<playlist>* in your download
  folder (`--out DIR` to put them elsewhere, `--missing` for only the tracks you don't have).
  Re-running it only fetches what's new. YouTube audio is converted from its ~128 kbps AAC inside
  SuperSync; nothing else needs installing.
- **Duplicates**: finds the same track saved more than once and suggests keeping the best copy.
  It can carry your rekordbox cues over to the copy you keep.
- **Upgrades**: tracks worth buying properly, including fake 320s and WAVs made from MP3s.

**How SuperSync treats your rekordbox library.** It reads rekordbox's database (`master.db`)
directly. It only ever writes to it while rekordbox is closed. Before writing, it backs up the
database and `masterPlaylists6.xml`, keeping the last 10 backups in SuperSync's data folder. It
checks its edited copy before swapping it in. If rekordbox is open when an import finishes,
SuperSync keeps the change until you close rekordbox and click **Apply**. Without rekordbox
installed, SuperSync keeps its own library as `SuperSync Library.xml` in your download folder, which
rekordbox can import later.

**What SuperSync downloads.** Only tracks whose artist has enabled SoundCloud's download button.
With your SoundCloud login token (Settings, optional) that's the artist's original file (often a
WAV). Without it, it's the MP3 stream.

## Getting it onto your DJ computer

Copy the right file from `dist/` (build it with `scripts/build.sh`):

| Computer | File |
| --- | --- |
| Mac (Apple Silicon or Intel) | `SuperSync-mac` |
| Windows | `SuperSync-windows.exe` (`-arm64` for ARM laptops) |
| Linux | `supersync-linux-amd64` / `-arm64` |

**Mac:** the first time, right-click the file → **Open** → **Open** (it isn't signed by an Apple
developer account, so a plain double-click is blocked once). Or in Terminal:
`xattr -d com.apple.quarantine SuperSync-mac`.

**Windows:** if SmartScreen appears, click **More info → Run anyway**.

## Using it

Double-click it. A small window opens (leave it open; close it to quit) and SuperSync opens
in your browser, on your rekordbox library.

1. **Settings → Download folder**: where SoundCloud imports are saved (in a `SoundCloud` folder).
   SuperSync finds rekordbox's library by itself; if yours is somewhere unusual, choose its
   `master.db` in Settings.
2. **Library**: pick a playlist in the sidebar.
   - Click a track to see its waveform, hot cues, memory cues and loops.
   - Double-click a track, or press Enter, to play it. Space plays and pauses; keys 1–8 jump to
     hot cues.
   - The chips at the top filter by quality tier.
3. **Import SoundCloud playlist**: paste a playlist link (secret share links work too). Progress
   shows track by track. If rekordbox is open, close it and click **Apply** to finish.
4. In an imported playlist, rows below UHQ, and tracks you don't have yet, show where to get a
   better copy. **⬇ SoundCloud** opens the track's page (log in, then ⋯ → Download file). The other
   buttons go to the free-download page or store the uploader linked.
5. **Duplicates → Carry over cues**: when the copy you're keeping has no cues but another copy
   does, SuperSync decodes both and measures the offset between them. Different masters often
   start a few ms apart, and an extended mix can start a minute earlier. It checks the offset at
   three points through the track, then gives you an XML to import. Different edits (a section
   added or cut in the middle) are detected and skipped, since one shift can't fit every cue.

Everything also works from a terminal: `supersync help`.

```
supersync library                                   # the library and its playlists
supersync import https://soundcloud.com/you/sets/friday
supersync apply                                     # finish an import that waited for rekordbox to close
supersync scan                                      # read files: quality tiers, upscale checks
supersync dupes                                     # list; add --move to move the extras
supersync cues --xml cues.xml                       # carry cue points to the copies you keep
supersync upgrades --min-kbps 320
```

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

## Development

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
