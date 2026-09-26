// SuperSync keeps a DJ music folder in step with SoundCloud playlists:
// what do I already own, what do I still need, where are my duplicates, and
// which tracks deserve a better-quality copy.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"supersync/internal/analyze"
	"supersync/internal/app"
	"supersync/internal/audio"
	"supersync/internal/rbdb"
	"supersync/internal/soundcloud"
	"supersync/internal/spectrum"
	"supersync/internal/web"
	"supersync/internal/youtube"
)

var version = "dev"

const usage = `SuperSync %s — your rekordbox library, in step with your SoundCloud playlists.

Run with no arguments (or double-click) to open the app in your browser.
SuperSync works on rekordbox's own library (found automatically); without
rekordbox it keeps its own library file that rekordbox can import later.

Commands:
  supersync                         open the web app
  supersync library                 show the library and its playlists
  supersync import <url>            make a library playlist from a SoundCloud playlist,
                                    downloading tracks the artists allow
  supersync download <url>          download a whole SoundCloud or YouTube playlist as mp3s
      --out DIR                     where to put them (default: <music>/SoundCloud|YouTube/<playlist>)
      --missing                     only the tracks you don't already have
  supersync apply                   finish imports that waited for rekordbox to close
  supersync scan                    read the library's audio files (quality, upscales)
  supersync check <url|list.txt>    what do I have / need from a playlist
      --xml FILE                    also write a rekordbox playlist XML of the tracks you have
      --m3u FILE                    also write an M3U8 playlist of the tracks you have
      --need FILE                   also write a CSV of tracks to buy, with store links
  supersync dupes                   list duplicate tracks
      --move                        move the extra copies to "_SuperSync Duplicates"
  supersync cues                    carry cue points from rips to the copies you keep
      --xml FILE                    write the rekordbox XML to import
  supersync undo                    put every moved duplicate back
  supersync upgrades                list low-quality tracks worth re-buying
  supersync info <file>...          show what SuperSync reads from audio files
  supersync config                  show settings

Settings (any command; they're remembered):
  --music DIR            where downloads go
  --rekordbox-db FILE    rekordbox's master.db, if it isn't found automatically
  --sc-token TOKEN       your SoundCloud login token, to download artists' original files
  --min-kbps N           quality bar for upgrades (default 320)
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 || strings.HasPrefix(args[0], "-") && args[0] != "-h" && args[0] != "--help" {
		runCmd("ui", args)
		return
	}
	runCmd(args[0], args[1:])
}

func runCmd(cmd string, args []string) {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprintf(os.Stderr, usage, version) }
	music := fs.String("music", "", "")
	rbdbPath := fs.String("rekordbox-db", "", "")
	scToken := fs.String("sc-token", "", "")
	minKbps := fs.Int("min-kbps", 0, "")
	xmlOut := fs.String("xml", "", "")
	outDir := fs.String("out", "", "")
	missing := fs.Bool("missing", false, "")
	m3uOut := fs.String("m3u", "", "")
	needOut := fs.String("need", "", "")
	move := fs.Bool("move", false, "")
	port := fs.Int("port", 0, "")
	noOpen := fs.Bool("no-browser", false, "")
	fs.Parse(reorder(args))

	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		fs.Usage()
		return
	}
	if cmd == "info" {
		info(fs.Args())
		return
	}

	a := app.New()
	if *music != "" {
		must(a.SetMusicDir(*music))
	}
	if *rbdbPath != "" {
		must(a.SetRekordboxDB(*rbdbPath))
	}
	if *scToken != "" {
		a.Cfg.SCToken = *scToken
		must(a.Cfg.Save())
	}
	if *minKbps > 0 {
		a.Cfg.MinKbps = *minKbps
		must(a.Cfg.Save())
	}

	if cmd == "ui" {
		must(web.Serve(a, *port, !*noOpen, version))
		return
	}
	if cmd == "config" {
		lib := "(none)"
		if a.Src != nil {
			lib = a.Src.Info().Kind + " — " + a.Src.Info().Path
		}
		fmt.Printf("library:          %s\ndownload folder:  %s\nSoundCloud token: %v\nmin kbps:         %d\n",
			lib, orNone(a.Cfg.MusicDir), a.Cfg.SCToken != "", a.Cfg.MinKbps)
		if a.SrcErr != "" {
			fmt.Println("library problem:", a.SrcErr)
		}
		return
	}
	if a.Src == nil {
		die(orNone(a.SrcErr))
	}

	switch cmd {
	case "library":
		info := a.Src.Info()
		fmt.Printf("%s library: %s\n%d tracks, %d playlists\n", info.Kind, info.Path, info.Tracks, info.Playlists)
		if info.Note != "" {
			fmt.Println(info.Note)
		}
		var walk func(ps []*rbdb.Playlist, indent string)
		walk = func(ps []*rbdb.Playlist, indent string) {
			for _, p := range ps {
				if p.Kind == "folder" {
					fmt.Printf("%s▸ %s\n", indent, p.Name)
				} else {
					fmt.Printf("%s  %s (%d)\n", indent, p.Name, p.Count)
				}
				walk(p.Children, indent+"  ")
			}
		}
		walk(a.Src.Playlists(), "")
		if n := len(a.PendingChanges()); n > 0 {
			fmt.Printf("\n%d change(s) waiting for rekordbox to close — run `supersync apply`.\n", n)
		}
	case "import":
		if fs.NArg() != 1 {
			die("usage: supersync import <soundcloud playlist url>")
		}
		ensureScanned(a)
		j, err := a.ImportSoundCloud(fs.Arg(0))
		must(err)
		shown := map[int]string{}
		for {
			snap := j.Snapshot()
			for _, st := range snap.Steps {
				if shown[st.N] != st.State && st.State != "queued" && st.State != "downloading" {
					shown[st.N] = st.State
					line := fmt.Sprintf("%3d %-10s %s", st.N, st.State, st.Title)
					if st.Note != "" {
						line += "  (" + st.Note + ")"
					}
					fmt.Println(line)
				}
			}
			if snap.Status == "done" || snap.Status == "error" || snap.Status == "waiting" {
				if snap.Message != "" {
					fmt.Println("\n" + snap.Message)
				}
				if snap.Status == "waiting" {
					fmt.Println("Run `supersync apply` after closing rekordbox.")
				}
				if snap.Status == "done" {
					fmt.Printf("\nAdded to your library as SoundCloud / %s.\n", snap.Title)
				}
				if snap.Status == "error" {
					os.Exit(1)
				}
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
	case "download":
		if fs.NArg() != 1 {
			die("usage: supersync download <soundcloud or youtube playlist url> [--out DIR] [--missing]")
		}
		if *outDir == "" && a.Cfg.MusicDir == "" {
			die("Tell me where downloads go first, e.g.\n  supersync download <url> --music ~/Music/DJ   (remembered)\nor pass --out DIR for just this download.")
		}
		if youtube.IsLink(fs.Arg(0)) {
			downloadYouTube(a, fs.Arg(0), *outDir, *missing)
			return
		}
		download(a, fs.Arg(0), *outDir, *missing)
	case "apply":
		n, err := a.ApplyPending()
		must(err)
		fmt.Printf("applied %d change(s)\n", n)
	case "scan":
		must(a.Scan(progressBar))
		fmt.Fprintln(os.Stderr)
		if n := a.QualityCheckPending(); n > 0 {
			fmt.Fprintf(os.Stderr, "Checking %d high-quality files for upscales (first time only)…\n", n)
			must(a.CheckQuality(progressBar))
			fmt.Fprintln(os.Stderr)
		}
		summary(a)
	case "check":
		if fs.NArg() != 1 {
			die("usage: supersync check <soundcloud playlist url | list.txt>")
		}
		ensureScanned(a)
		res, err := a.Check(fs.Arg(0), progressBar)
		must(err)
		printCheck(res)
		var have []string
		for _, r := range res.Rows {
			if r.Status == app.Have {
				have = append(have, r.Match.Path)
			}
		}
		if *xmlOut != "" {
			f := create(*xmlOut)
			must(a.ExportXML(f, res.Playlist.Title, have))
			f.Close()
			fmt.Printf("\nrekordbox XML: %s (%d tracks)\n", *xmlOut, len(have))
		}
		if *m3uOut != "" {
			f := create(*m3uOut)
			must(app.ExportM3U(f, a.Lib, have))
			f.Close()
			fmt.Printf("M3U playlist: %s (%d tracks)\n", *m3uOut, len(have))
		}
		if *needOut != "" {
			f := create(*needOut)
			must(app.NeedCSV(f, res, true))
			f.Close()
			fmt.Printf("shopping list: %s\n", *needOut)
		}
	case "dupes":
		ensureScanned(a)
		groups, err := a.Duplicates()
		must(err)
		printDupes(groups)
		if *move && len(groups) > 0 {
			var extra []string
			for _, g := range groups {
				if g.LengthsDiffer {
					continue
				}
				for _, c := range g.Copies[1:] {
					extra = append(extra, c.Path)
				}
			}
			if len(extra) == 0 {
				fmt.Println("\nNothing to move automatically (remaining groups have different lengths; use the app to choose).")
				return
			}
			fmt.Printf("\nMove %d extra copies to %q? They can be restored with `supersync undo`. [y/N] ",
				len(extra), filepath.Join(a.Cfg.MusicDir, "_SuperSync Duplicates"))
			if !confirm() {
				return
			}
			moves, err := a.Quarantine(extra)
			fmt.Printf("moved %d files\n", len(moves))
			must(err)
		}
	case "cues":
		ensureScanned(a)
		pairs, err := a.CuePairs(nil)
		must(err)
		if len(pairs) == 0 {
			fmt.Println("No duplicates have cue points on a copy other than the one being kept.")
			return
		}
		fmt.Fprintf(os.Stderr, "Lining up %d tracks…\n", len(pairs))
		plans, err := a.PlanCues(pairs)
		must(err)
		ok := 0
		for _, t := range plans {
			fmt.Printf("\n%s\n  from %s [%s]\n  to   %s [%s]\n", t.Name, t.FromRel, t.FromQuality, t.ToRel, t.ToQuality)
			line := fmt.Sprintf("  %s", t.Status)
			if t.Usable() {
				ok++
				line += fmt.Sprintf(": %d cues%s, shifted %s (match %.0f%%)", t.Cues, map[bool]string{true: " + beatgrid"}[t.Grid], fmtOffset(t.Offset), t.Confidence*100)
				if t.Dropped > 0 {
					line += fmt.Sprintf(", %d fall outside the new file", t.Dropped)
				}
			}
			fmt.Println(line)
			if t.Note != "" {
				fmt.Println("  " + t.Note)
			}
			if len(t.Playlists) > 0 {
				fmt.Println("  in playlists: " + strings.Join(t.Playlists, ", "))
			}
		}
		if *xmlOut != "" && ok > 0 {
			f := create(*xmlOut)
			var usable []analyze.Pair
			for _, t := range plans {
				if t.Usable() {
					usable = append(usable, t.Pair)
				}
			}
			n, err := a.CueXML(f, usable)
			f.Close()
			must(err)
			fmt.Printf("\nWrote %s (%d tracks). In rekordbox: open it under rekordbox xml, select the tracks, right-click → Import To Collection.\n", *xmlOut, n)
		} else if ok > 0 {
			fmt.Println("\nAdd --xml FILE to write the rekordbox XML.")
		}
	case "undo":
		n, err := a.Undo()
		must(err)
		fmt.Printf("restored %d files\n", n)
	case "upgrades":
		ensureScanned(a)
		ups, err := a.Upgrades()
		must(err)
		for _, u := range ups {
			fmt.Printf("%-16s %s\n%16s %s\n", u.Reason, u.Rel, "", u.Links.Beatport)
		}
		fmt.Printf("\n%d tracks below %d kbps\n", len(ups), a.Cfg.MinKbps)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		fs.Usage()
		os.Exit(2)
	}
}

// download fetches every track of a playlist (or, with missing, just the ones
// the library doesn't have), three at a time. Files already in the folder are
// kept, so re-running picks up only what's new.
func download(a *app.App, link, dir string, missing bool) {
	var tracks []*soundcloud.Track
	var title string
	if missing {
		ensureScanned(a)
		res, err := a.Check(link, progressBar)
		must(err)
		title = res.Playlist.Title
		for _, r := range res.Rows {
			if r.Status == app.Need {
				tracks = append(tracks, r.SC)
			}
		}
	} else {
		pl, err := a.SC.FetchPlaylist(link)
		must(err)
		title, tracks = pl.Title, pl.Tracks
	}
	if dir == "" {
		dir = filepath.Join(a.Cfg.MusicDir, "SoundCloud", soundcloud.SafeFilename(title))
	}
	fmt.Printf("%s: downloading %d tracks to %s\n\n", title, len(tracks), dir)

	var mu sync.Mutex
	var got, kept, failed int
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for i, t := range tracks {
		wg.Add(1)
		go func(n int, t *soundcloud.Track) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var line string
			var err error
			var path string
			var original bool
			if !t.CanDownload() {
				err = soundcloud.ErrNotDownloadable
				if t.Unavailable {
					err = errors.New("unavailable on SoundCloud")
				}
			} else if path = soundcloud.Existing(t, dir); path == "" {
				path, original, err = a.SC.Download(t, dir, a.Cfg.SCToken, nil)
			} else {
				mu.Lock()
				kept++
				fmt.Printf("%3d = %s  (already downloaded)\n", n, filepath.Base(path))
				mu.Unlock()
				return
			}
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				failed++
				line = fmt.Sprintf("%3d ✗ %s — %v", n, t.Title, err)
			case original:
				got++
				line = fmt.Sprintf("%3d ✓ %s  (original file)", n, filepath.Base(path))
			default:
				got++
				line = fmt.Sprintf("%3d ✓ %s", n, filepath.Base(path))
			}
			fmt.Println(line)
		}(i+1, t)
	}
	wg.Wait()
	fmt.Printf("\n%d downloaded · %d already had · %d failed\n", got, kept, failed)
	if got > 0 {
		fmt.Println("Stream copies are usually 128 kbps; run `supersync scan` to add them to the library.")
	}
}

// downloadYouTube is download for YouTube links, converted to mp3 in Go.
// With missing, the videos are matched against the
// library like a SoundCloud playlist first.
func downloadYouTube(a *app.App, link, dir string, missing bool) {
	yt := youtube.New()
	pl, err := yt.List(link)
	must(err)
	entries := pl.Entries
	if missing {
		ensureScanned(a)
		// Reuse the SoundCloud matcher: it only needs a title, uploader and length.
		sc := &soundcloud.Playlist{Title: pl.Title}
		for i, e := range entries {
			sc.Tracks = append(sc.Tracks, &soundcloud.Track{ID: int64(i), Title: e.Title, Uploader: e.Artist(), DurationMS: int64(e.Duration * 1000)})
		}
		res := a.Compare(sc, a.Lib)
		var need []*youtube.Entry
		for i, r := range res.Rows {
			if r.Status == app.Need {
				need = append(need, entries[i])
			}
		}
		fmt.Printf("already have %d of %d\n", len(entries)-len(need), len(entries))
		entries = need
	}
	if dir == "" {
		dir = filepath.Join(a.Cfg.MusicDir, "YouTube", soundcloud.SafeFilename(pl.Title))
	}
	fmt.Printf("%s: downloading %d videos to %s\n\n", pl.Title, len(entries), dir)

	var mu sync.Mutex
	var got, kept, failed int
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func(n int, e *youtube.Entry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if p := youtube.Existing(e, dir); p != "" {
				mu.Lock()
				kept++
				fmt.Printf("%3d = %s  (already downloaded)\n", n, filepath.Base(p))
				mu.Unlock()
				return
			}
			path, err := yt.Download(e, dir, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				fmt.Printf("%3d ✗ %s — %v\n", n, e.Title, err)
				return
			}
			got++
			fmt.Printf("%3d ✓ %s\n", n, filepath.Base(path))
		}(i+1, e)
	}
	wg.Wait()
	fmt.Printf("\n%d downloaded · %d already had · %d failed\n", got, kept, failed)
	if got > 0 {
		fmt.Println("These are converted from YouTube's ~128 kbps AAC; run `supersync scan` to add them to the library.")
	}
}

// reorder lets flags come after positional args ("check URL --xml out.xml").
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") && name != "move" && name != "no-browser" && name != "missing" && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

func ensureScanned(a *app.App) {
	if a.Lib == nil {
		fmt.Fprintln(os.Stderr, "Indexing your music folder (first time only)…")
		must(a.Scan(progressBar))
		fmt.Fprintln(os.Stderr)
	}
}

func summary(a *app.App) {
	lib := a.Lib
	lossless := 0
	for _, t := range lib.Tracks {
		if t.Lossless {
			lossless++
		}
	}
	fmt.Printf("%d audio files read (%d lossless)\n", len(lib.Tracks), lossless)
	fake := 0
	for _, t := range lib.Tracks {
		if t.Upscaled() != "" {
			fake++
		}
	}
	if fake > 0 {
		fmt.Printf("%d files are upscaled from a lower quality (see `supersync upgrades`)\n", fake)
	}
	if a.Src != nil {
		info := a.Src.Info()
		fmt.Printf("library: %s (%d tracks, %d playlists)\n", info.Kind, info.Tracks, info.Playlists)
	}
}

func printCheck(res *app.Result) {
	fmt.Printf("\n%s — %d tracks\n\n", res.Playlist.Title, len(res.Rows))
	for _, r := range res.Rows {
		mark := map[app.Status]string{app.Have: "✓ have ", app.Maybe: "? maybe", app.Need: "✗ NEED ", app.Unavailable: "- n/a  "}[r.Status]
		fmt.Printf("%3d %s  %s\n", r.N, mark, r.SC.Title)
		switch {
		case r.Match != nil:
			line := "              → " + r.Match.Rel + "  [" + r.Match.Quality + "]"
			if r.Upgrade != "" {
				line += "  ⚠ low quality"
			}
			fmt.Println(line)
		case r.Status == app.Maybe && len(r.Others) > 0:
			fmt.Printf("              ? %s  [%s]\n", r.Others[0].Rel, r.Others[0].Quality)
		}
		if r.Status == app.Need || r.Status == app.Maybe || r.Upgrade != "" {
			if u := app.FreeLink(r.SC); u != "" {
				fmt.Println("              ⬇ free: " + u)
			} else if u := app.BuyLink(r.SC); u != "" {
				fmt.Println("              $ buy:  " + u)
			} else if r.SC.Downloadable {
				fmt.Println("              (SoundCloud download button, but its download limit is used up)")
			}
		}
	}
	fmt.Printf("\nhave %d · maybe %d · need %d · low quality %d · free downloads available %d\n", res.Have, res.Maybe, res.Need, res.Upgrades, res.Free)
}

func printDupes(groups []*analyze.Group) {
	for _, g := range groups {
		note := ""
		if g.LengthsDiffer {
			note = "  (different lengths — may be different edits)"
		}
		fmt.Printf("\n%s%s\n", g.Copies[0].Rel, note)
		for i, c := range g.Copies {
			mark := "  keep"
			if i > 0 {
				mark = "  extra"
			}
			extra := ""
			if c.InRekordbox {
				extra = fmt.Sprintf("  rekordbox: %d cues, %d plays", c.Cues, c.PlayCount)
			}
			fmt.Printf("%-7s [%-14s %s] %s%s\n", mark, c.QualityLabel(), fmtDur(c.Duration), c.Rel, extra)
		}
		if g.CueWarning {
			fmt.Println("         ⚠ an extra copy has cue points in rekordbox; the keeper doesn't")
		}
	}
	fmt.Printf("\n%d duplicate groups\n", len(groups))
}

func info(paths []string) {
	for _, p := range paths {
		in, err := audio.Read(p)
		if err != nil {
			fmt.Println(p+":", err)
			continue
		}
		fmt.Printf("%s\n  artist: %s\n  title:  %s\n  album:  %s\n  %s, %s, %d Hz\n",
			p, in.Artist, in.Title, in.Album, in.QualityLabel(), fmtDur(in.Duration), in.SampleRate)
		if c, err := spectrum.Analyze(p, in.Duration); err == nil {
			in.Cutoff = c
			switch {
			case c == spectrum.NoCutoff:
				fmt.Println("  spectrum: full range, no encoder cutoff")
			case in.Upscaled() != "":
				fmt.Println("  spectrum: UPSCALED, " + in.Upscaled())
			default:
				fmt.Printf("  spectrum: cuts off at %.1f kHz (normal for %s)\n", float64(c)/1000, in.QualityLabel())
			}
		}
		if in.Err != "" {
			fmt.Println("  problem:", in.Err)
		}
	}
}

func fmtOffset(v float64) string {
	if math.Abs(v) < 0.0005 {
		return "0 ms (already lined up)"
	}
	if math.Abs(v) < 1 {
		return fmt.Sprintf("%+.0f ms", v*1000)
	}
	return fmt.Sprintf("%+.3f s", v)
}

func fmtDur(s float64) string {
	return fmt.Sprintf("%d:%02d", int(s)/60, int(s)%60)
}

func progressBar(done, total int) {
	fmt.Fprintf(os.Stderr, "\r  %d / %d files", done, total)
}

func confirm() bool {
	s, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "y")
}

func create(p string) *os.File {
	f, err := os.Create(p)
	must(err)
	return f
}

func orNone(s string) string {
	if s == "" {
		return "(not set)"
	}
	return s
}

func must(err error) {
	if err != nil {
		die(err.Error())
	}
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "error:", msg)
	os.Exit(1)
}
