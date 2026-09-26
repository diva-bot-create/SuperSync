# SuperSync

**Keep your rekordbox library in step with your SoundCloud playlists, without the duplicates.**

You build a playlist on SoundCloud of what you want to play. Then you download or buy the
tracks, drop them into your music folder, and add them to rekordbox by hand. Do that for a few
playlists and you end up re-downloading or re-buying songs you already have, with copies of the
same track scattered around and cue points on the wrong one.

SuperSync does that job for you:

- **Paste a SoundCloud playlist link and it becomes a rekordbox playlist.** Songs you already
  own are linked instead of downloaded again. Missing songs are downloaded into your music folder
  and added. When the SoundCloud playlist grows, one click (or an automatic schedule) brings the
  new songs in. YouTube playlists work too.
- **It never downloads a song you already have**, even when the SoundCloud title is a mess like
  `H0t 1n H3r3 (JOHNY GAMBLE EDIT) [FREE DL]`.
- **It finds duplicates and cleans them up.** It keeps the best-quality copy and moves your
  playlists, play history and cue points over to it, lined up to the millisecond. The extra
  files go into a separate folder rather than being deleted, and one click undoes the whole thing.
- **It shows the quality of every track at a glance**, and tells you which ones are worth
  buying properly, including "320s" and WAVs that were secretly made from a 128 kbps rip.
- **You can browse and play your library**, with rekordbox's own waveforms, beatgrids, hot cues,
  memory cues and loops.

## Download

Get the latest version from the **[Releases page](../../releases/latest)**.

| Your computer | Download |
| --- | --- |
| Mac (Apple Silicon or Intel) | `SuperSync-mac.zip` |
| Windows | `SuperSync-windows.zip` |

There's nothing to install. Unzip it and double-click **SuperSync**.

**Mac, first time only.** SuperSync isn't registered with Apple yet, so macOS blocks it the first
time it's opened:
1. Double-click SuperSync and click **Done** on the warning.
2. Open **System Settings → Privacy & Security**, scroll down and click **Open Anyway** next to
   the SuperSync message.
3. Double-click SuperSync again and click **Open Anyway**.

**Windows, first time only.** If a blue "Windows protected your PC" box appears, click
**More info → Run anyway**.

## Getting started

When you open SuperSync, a small window appears (on Mac, a Terminal window). Leave it open while
you use SuperSync; closing it quits the app. SuperSync itself opens in your web browser. It runs
only on your computer, and nothing is uploaded anywhere.

1. SuperSync finds your rekordbox library by itself and shows it: your playlists on the left,
   tracks on the right.
2. Go to **Settings → Download folder** and choose where new downloads should go, for example
   your main music folder.
3. Click **＋ Add SoundCloud playlist**, paste the playlist link (secret links work too), and
   watch it fill in, track by track.

You don't need rekordbox installed to try it. Without rekordbox, SuperSync keeps its own library,
which rekordbox can import later.

## What you can do

### Sync playlists from SoundCloud and YouTube

Synced playlists appear in your rekordbox library inside a **SoundCloud** or **YouTube** folder,
marked **⟳ SC** or **⟳ YT** in SuperSync's sidebar. Open one and click **Sync** to pick up songs
added since last time, or turn on **Settings → Automatic sync** (every hour, 6 hours or day)
while SuperSync is open.

What gets downloaded:
- The artist's own file when they've switched on SoundCloud's download button. This is often a
  WAV, but only if you've added your SoundCloud login in Settings; the instructions are on that
  page.
- Otherwise, the SoundCloud stream, usually 128 kbps. It's marked as low quality so you know it's
  worth replacing.
- Songs that can't be downloaded at all are listed with a link to the artist's free-download or
  buy page.

### See the quality of every track

Every track is labelled **LOW** (under 128 kbps), **NORMAL** (128), **HQ** (256) or **UHQ**
(above 256, or lossless like WAV, AIFF and FLAC). Click the labels at the top of a playlist to show
only those tracks. In synced playlists, anything below UHQ has a button that takes you to where you
can get a better copy.

SuperSync also listens for fakes. A "320 kbps" MP3 or a WAV that was made from a low-quality
download is labelled as what it really is, and shows up under **Upgrades**.

### Clean up duplicates

The **Duplicates** tab lists every song you have more than once and picks the best copy to keep.
**Clean up** (or **Clean up all**) then does the following:
- Your playlists and history switch to the copy you keep, and play counts are added together.
- Cue points and loops move over from the extra copy, lined up precisely even when the two files
  start at slightly different times.
- The extra files move into a `_SuperSync Duplicates` folder inside your music folder.

Copies with different lengths (like a radio edit and an extended mix) are left alone unless you
clean them up one at a time. **Undo clean-up** puts everything back.

### Browse and play your library

Click any track to see its waveform, beatgrid, hot cues, memory cues and loops. Double-click to
play it, click a cue to jump to it, or press keys 1–8 for hot cues A–H. Space plays and pauses.

## Keeping your library safe

- **Everything is backed up.** Before SuperSync changes your rekordbox library, it saves a copy of
  it. The last 10 copies are kept.
- **Nothing is deleted.** Duplicate files are moved, not deleted, and can be put back.
- **rekordbox must be closed for changes to be saved.** While it's open, SuperSync keeps your
  changes waiting, and a box in the sidebar shows what's waiting. To apply them, either:
  - close rekordbox and click **Apply now**;
  - switch on **Settings → Add waiting changes as soon as it's closed**; or
  - click **Restart rekordbox & apply…**. SuperSync asks you to confirm first, then closes
    rekordbox (forcing it if it doesn't close within a few seconds), saves the changes, and opens
    rekordbox again. **Don't do this during a set.**

SuperSync never closes rekordbox without asking.

## Questions

**Does it work with my existing folders?** Yes. It reads whatever is in your rekordbox library,
wherever the files are. New downloads go into the download folder you choose.

**Will it mess up my cue points?** No. Cue points are only moved during a duplicate clean-up. If
the two copies are genuinely different edits, the cues are left where they are. Either way you can
undo.

**Why do some downloads say LOW or NORMAL?** That's what SoundCloud streams. Add your SoundCloud
login in Settings to get artists' original files where they allow downloads. For the rest, the
buttons next to each track take you to where you can buy or download a better copy.

**Where are my settings kept?** On your computer only:
`~/Library/Application Support/SuperSync/` on Mac, `%AppData%\SuperSync\` on Windows.

---

Built for DJs who dig on SoundCloud. Developer notes are in [DEVELOPMENT.md](DEVELOPMENT.md).
