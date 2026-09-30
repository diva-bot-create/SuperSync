package app

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"supersync/internal/analyze"
	"supersync/internal/audio"
	"supersync/internal/fingerprint"
	"supersync/internal/library"
	"supersync/internal/match"
	"supersync/internal/rbdb"
)

// BetterCopy is a better-quality file of a library track that isn't in the
// library itself: something the user downloaded or bought to replace it.
type BetterCopy struct {
	Path  string `json:"path"`
	Label string `json:"label"` // "WAV 24/44.1", "MP3 320"
	Where string `json:"where"` // "Downloads", or the path within the music folder
}

var findingBetter atomic.Bool

// FindBetterCopies looks through the music folder and recent Downloads for
// better copies of the library's LOW and NORMAL tracks. It runs in the
// background after scans; BetterFor reports what it found.
func (a *App) FindBetterCopies() {
	if !findingBetter.CompareAndSwap(false, true) {
		return
	}
	defer findingBetter.Store(false)
	lib := a.Lib
	if lib == nil || a.Src == nil {
		return
	}
	type cand struct {
		t     *library.Track
		where string
	}
	var cands []cand
	if dir := a.Cfg.MusicDir; dir != "" {
		if l, err := library.Scan(dir, nil); err == nil {
			for _, t := range l.Tracks {
				cands = append(cands, cand{t, t.Rel})
			}
		}
	}
	if dir := downloadsDir(); dir != "" {
		if paths := recentAudio(dir, 120*24*time.Hour); len(paths) > 0 {
			if l, err := library.ScanFiles("downloads:"+dir, paths, nil); err == nil {
				for _, t := range l.Tracks {
					cands = append(cands, cand{t, "Downloads"})
				}
			}
		}
	}
	out := map[string]*BetterCopy{}
	best := map[string]int{}
	for _, c := range cands {
		if a.Col.Lookup(c.t.Path) != nil || !goodEnough(&c.t.Info) {
			continue // already in the library, or not an upgrade anyone wants
		}
		for _, h := range lib.Find(c.t.Keys) {
			if match.SameFile(c.t.Keys, h.Track.Keys) < match.Sure || c.t.Quality() <= h.Track.Quality() {
				continue
			}
			if c.t.Duration > 0 && h.Track.Duration > 0 && math.Abs(c.t.Duration-h.Track.Duration) > 15 {
				continue // a different edit
			}
			t := a.TrackByPath(h.Track.Path)
			if t == nil {
				continue
			}
			if tier, _, _ := a.Tier(t); tier != "low" && tier != "normal" {
				continue
			}
			if q := c.t.Quality(); q > best[t.ID] {
				best[t.ID] = q
				out[t.ID] = &BetterCopy{Path: c.t.Path, Label: c.t.QualityLabel(), Where: c.where}
			}
		}
	}
	a.mu.Lock()
	a.better = out
	a.mu.Unlock()
	a.autoSwap(out)
}

// autoSwap swaps in better copies by itself for tracks without cues, when
// the two files sound like the same recording, rekordbox is closed, and the
// user hasn't swapped that file back before. Each swap is in the history.
func (a *App) autoSwap(found map[string]*BetterCopy) {
	if a.Cfg.NoAutoSwap || len(found) == 0 || a.Src == nil || a.Cfg.MusicDir == "" {
		return
	}
	if a.Src.Info().Kind == "rekordbox" && rbdb.Running() {
		return // tried again at the next look (every few minutes)
	}
	a.State.mu.Lock()
	done := map[string]bool{}
	for _, p := range a.State.AutoSwapped {
		done[filepath.Clean(p)] = true
	}
	a.State.mu.Unlock()
	var swapped []string
	for id, b := range found {
		t := a.Src.Track(id)
		if t == nil || done[filepath.Clean(b.Path)] {
			continue
		}
		if ct := a.Col.Lookup(t.Path); ct == nil || ct.Cues > 0 {
			continue
		}
		if !a.soundsSame(t.Path, t.Length, b.Path) {
			continue
		}
		a.State.mu.Lock()
		a.State.AutoSwapped = append(a.State.AutoSwapped, b.Path)
		a.State.save()
		a.State.mu.Unlock()
		if _, err := a.SwapUpgrade(id, b.Path); err != nil {
			log.Printf("auto-swap %s: %v", t.Title, err)
			continue
		}
		amendLast(func(e *HistoryEntry) { e.Auto = true })
		swapped = append(swapped, t.Title)
	}
	if len(swapped) > 0 {
		a.notify(Notice{Kind: "swap", Title: fmt.Sprintf("Swapped in %d better cop%s", len(swapped), map[bool]string{true: "y", false: "ies"}[len(swapped) == 1]),
			Body: strings.Join(swapped, ", ")})
	}
}

// soundsSame fingerprints two files and says whether they're one recording.
func (a *App) soundsSame(pathA string, lengthA int, pathB string) bool {
	var fa fingerprint.FP
	if a.Lib != nil {
		a.mu.Lock()
		if lt := a.Lib.ByPath(pathA); lt != nil {
			fa = lt.FP
		}
		a.mu.Unlock()
	}
	var err error
	if len(fa) == 0 {
		if fa, err = fingerprint.File(pathA, float64(lengthA)); err != nil {
			return false
		}
	}
	in, err := audio.Read(pathB)
	if err != nil {
		return false
	}
	fb, err := fingerprint.File(pathB, in.Duration)
	if err != nil || len(fa) == 0 || len(fb) == 0 {
		return false
	}
	return fingerprint.Compare(fa, fb).BER <= fingerprint.Same
}

// goodEnough: HQ or better (and not a known upscale).
func goodEnough(in *audio.Info) bool {
	if in.TrueKbps() > 0 {
		return in.TrueKbps() >= 240
	}
	return in.Lossless || in.Bitrate >= 240
}

// BetterFor is the better copy found for a library track, if any.
func (a *App) BetterFor(id string) *BetterCopy {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.better[id]
}

func downloadsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	d := filepath.Join(home, "Downloads")
	if st, err := os.Stat(d); err == nil && st.IsDir() {
		return d
	}
	return ""
}

// recentAudio lists audio files in dir (two levels deep) changed within age.
func recentAudio(dir string, age time.Duration) []string {
	var out []string
	cut := time.Now().Add(-age)
	base := strings.Count(dir, string(filepath.Separator))
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && (strings.HasPrefix(d.Name(), ".") || strings.Count(p, string(filepath.Separator))-base >= 2) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || !audio.IsAudio(d.Name()) {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(cut) {
			out = append(out, p)
		}
		return nil
	})
	if len(out) > 2000 {
		out = out[:2000]
	}
	return out
}

// SwapUpgrade replaces a library track with a better copy of it: the new
// file joins the library, takes over the old one's playlists, history and
// cues (lined up to its audio), and the old file moves to the duplicates
// folder. Undo clean-up puts it back. A file outside the music folder (e.g.
// in Downloads) is moved next to the one it replaces.
func (a *App) SwapUpgrade(id, path string) (*CleanupResult, error) {
	if a.Src == nil {
		return nil, ErrNoSource
	}
	if a.Cfg.MusicDir == "" {
		return nil, errors.New("choose a download folder in Settings first")
	}
	old := a.Src.Track(id)
	if old == nil {
		return nil, errors.New("that track isn't in the library any more")
	}
	if filepath.Clean(path) == filepath.Clean(old.Path) {
		return nil, errors.New("that's the same file")
	}
	in, err := audio.Read(path)
	if err != nil {
		return nil, errors.New("can't read " + filepath.Base(path) + " as audio")
	}
	var brought *analyze.Move
	if !within(path, a.Cfg.MusicDir) {
		dst := freePath(filepath.Join(filepath.Dir(old.Path), filepath.Base(path)))
		if err := moveFile(path, dst); err != nil {
			return nil, err
		}
		brought = &analyze.Move{Time: time.Now(), From: path, To: dst}
		path = dst
		in.Path = dst
	}
	var before string // the library before the swap, for undo
	if a.Col.Lookup(path) == nil {
		ch := &Change{ID: newID(), Label: "Upgrade: " + old.Title, CreatedAt: time.Now(),
			Items: []ChangeItem{{New: newTrack(in, old.Title, old.Artist)}}}
		applied, err := a.Src.Apply(ch)
		if err != nil {
			if brought != nil {
				moveFile(brought.To, brought.From)
			}
			return nil, err
		}
		before = applied.Backup
		a.rebuild()
	}
	res, err := a.CleanupDuplicates([]CleanupGroup{{Keep: path, Extras: []string{old.Path}, Cues: rbdb.CuesBoth}})
	if err == nil {
		// Undo goes back to before the swap: the new file out of the library
		// (and back where it came from), the old one back in its place.
		a.State.mu.Lock()
		if lc := a.State.LastCleanup; lc != nil {
			if before != "" {
				lc.Backup = before
			}
			if brought != nil {
				lc.Moves = append(lc.Moves, *brought)
			}
			lc.Summary = "Upgraded “" + old.Title + "”"
			a.State.save()
		}
		a.State.mu.Unlock()
		amendLast(func(e *HistoryEntry) {
			e.Label = "Swapped in a better copy of “" + old.Title + "”"
			if before != "" {
				e.Backup = before
			}
			if brought != nil {
				e.Moves = append(e.Moves, *brought)
			}
		})
	}
	a.mu.Lock()
	delete(a.better, id)
	a.mu.Unlock()
	go a.Scan(nil)
	return res, err
}

func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func freePath(p string) string {
	if _, err := os.Stat(p); err != nil {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 2; ; i++ {
		c := base + " (" + itoa(i) + ")" + ext
		if _, err := os.Stat(c); err != nil {
			return c
		}
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		in.Close()
		return err
	}
	_, err = io.Copy(out, in)
	in.Close()
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}
