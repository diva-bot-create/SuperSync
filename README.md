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
| Mac (Apple Silicon or Intel, macOS 11 or later) | `SuperSync.dmg` |
| Windows 10 or 11 | `SuperSync-Setup.exe` |

**Mac:** open `SuperSync.dmg` and drag **SuperSync** into **Applications**. The first time you
open it, macOS blocks it, because SuperSync isn't registered with Apple yet:
1. Open SuperSync from Applications and click **Done** on the warning.
2. Open **System Settings → Privacy & Security**, scroll down and click **Open Anyway** next to
   the SuperSync message.
3. Open SuperSync again and click **Open Anyway**.

**Windows:** run `SuperSync-Setup.exe`. If a blue "Windows protected your PC" box appears, click
**More info → Run anyway**. SuperSync installs just for you, so no administrator password is
needed. It adds shortcuts to the Start menu and desktop, and you can remove it from
**Settings → Apps** like any other app.

**Updates.** SuperSync updates itself. When a new version is out, it downloads it in the
background and an **Update** button appears at the top. Click it and SuperSync restarts into the
new version in a few seconds. You can turn this off in **Settings → Updates**. If you have v0.1,
download the latest version once by hand; after that it's automatic.

## Getting started

SuperSync opens in its own window. It runs only on your computer, and nothing is uploaded
anywhere.

1. SuperSync finds your rekordbox library by itself and shows it: your playlists on the left,
   tracks on the right.
2. Go to **Settings → Download folder** and choose where new downloads should go, for example
   your main music folder.
3. Click **＋ Add SoundCloud playlist** and paste the playlist link (secret links work too). The
   playlist opens straight away, and its tracks fill in as they download. You can play any of
   them while you wait.

You don't need rekordbox installed to try it. Without rekordbox, SuperSync keeps its own library,
which rekordbox can import later.

**Running in the background.** Closing the window doesn't quit SuperSync. It keeps going in the
menu bar on Mac (the ⟳ icon at the top of the screen) or the notification area on Windows (next to
the clock), so automatic syncs carry on. Open it again from there or from its icon; quit from
there too, or with ⌘Q on Mac. In **Settings → App** you can switch this off, or have SuperSync
open by itself (in the background) when you log in.

**Right-click anything.** Tracks, playlists, folders, duplicates, cue points, text boxes and the
player all have their own menu. From there you can create, rename or delete playlists (with Undo),
add tracks to a playlist or remove them, play a track next, show the file in Finder or Explorer,
open it on SoundCloud, or copy its name or link.

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

If a song can't be downloaded (some SoundCloud tracks are only streamed copy-protected), click
**Use another link…** next to it. Paste a link to the same song elsewhere: another SoundCloud
upload, a YouTube video, or an audio file. SuperSync downloads it into its place in the playlist.

Tracks you've added to rekordbox through its **SoundCloud streaming** show a **SOUNDCLOUD** label.
There's no file for them on your computer, but they still play in SuperSync.

### See the quality of every track

Every track is labelled **LOW** (under 128 kbps), **NORMAL** (128), **HQ** (256) or **UHQ**
(above 256, or lossless like WAV, AIFF and FLAC). Click the labels at the top of a playlist to show
only those tracks. In synced playlists, anything below UHQ has a button that takes you to where you
can get a better copy.

**Upgrading a track.** LOW and NORMAL labels can be clicked. Click one to download or buy a
better copy, using the links the uploader put on SoundCloud. Once you've downloaded it (to your
music folder or Downloads), SuperSync notices within a few minutes and the label gets a **↑**.
Click it and choose **Swap in**. The better copy then takes the old one's place in every playlist
and its history, your cues move across lined up to the new file, and the old file goes to
the Trash (or wherever **Settings → Clean-up** says). **Undo** puts the library back. If SuperSync hasn't spotted the file, choose
**I have a better copy** and either choose the file or pick it from your library.

**Songs you already have.** When a playlist syncs, songs already in your library are added as they
are: nothing is downloaded, and your cues, beatgrid and play history stay. If SuperSync isn't sure
a file is the same recording (maybe it's another version), the track shows **MAYBE**. Choose
**Same track** to use your copy, or **Different** to download this one.

SuperSync also listens for fakes. A "320 kbps" MP3 or a WAV that was made from a low-quality
download is labelled as what it really is, and shows up under **Upgrades**.

### Clean up duplicates

The **Duplicates** tab lists every song you have more than once and picks the best copy to keep.
**Clean up** (or **Clean up all**) then does the following:
- Your playlists and history switch to the copy you keep, and play counts are added together.
- Cue points and loops move over from the extra copy, lined up precisely even when the two files
  start at slightly different times.
- The extra files go to the Trash (Recycle Bin on Windows). In **Settings → Clean-up** you can choose to delete them permanently instead, or keep them in a `_SuperSync Duplicates` folder.

Copies with different lengths (like a radio edit and an extended mix) are left alone unless you
clean them up one at a time. **Undo clean-up** puts everything back.

### Browse and play your library

Click any track to see its waveform, beatgrid, hot cues, memory cues and loops. Double-click to
play it, click a cue to jump to it, or press keys 1–8 for hot cues A–H. Space plays and pauses.

## Keeping your library safe

- **Everything is backed up.** Before SuperSync changes your rekordbox library, it saves a copy of
  it. The last 10 copies are kept.
- **Nothing is lost by accident.** Cleaned-up duplicate files go to the Trash (Recycle Bin), where you can get them back, unless you choose permanent deletion in Settings.
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

**Why does a track say NO ACCESS?** The file is there, but your computer isn't letting
SuperSync read that folder. This is often your Downloads or Documents folder, or an external drive.
On Mac, allow SuperSync in **System Settings → Privacy & Security → Files & Folders**, then click
**Rescan**. **MISSING** means the file isn't where rekordbox expects it. Hover over it to see why: a drive
that isn't connected, a file that was moved, or a library shared from another computer. Click
**find them** at the top of the playlist and SuperSync searches your music folder for the moved
files, or right-click a track and choose **Locate…** to pick the file yourself.

**Where are my settings kept?** On your computer only:
`~/Library/Application Support/SuperSync/` on Mac, `%AppData%\SuperSync\` on Windows.

---

Built for DJs who dig on SoundCloud. Developer notes are in [DEVELOPMENT.md](DEVELOPMENT.md).
