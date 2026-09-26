package app

import (
	"errors"
	"math"
	"path"
	"path/filepath"

	"supersync/internal/match"
	"supersync/internal/rbdb"
	"supersync/internal/soundcloud"
)

// libIndex matches SoundCloud tracks against the library's own track info
// (rekordbox's artist and title, and the stored file name). It catches
// tracks the file scan can't see: Cloud Library Sync tracks, files SuperSync
// can't read, or files whose tags and names differ from rekordbox's.
type libIndex struct {
	tracks []*rbdb.Track
	keys   [][]match.Key
	byTok  map[string][]int
}

func (a *App) newLibIndex() *libIndex {
	x := &libIndex{byTok: map[string][]int{}}
	if a.Src == nil {
		return x
	}
	for _, t := range a.Src.Tracks() {
		if t.Stream != "" {
			continue
		}
		ks := match.Parse(t.Artist, t.Title)
		name := t.StoredPath
		if name == "" {
			name = t.Path
		}
		ks = append(ks, match.ParseFilename(path.Base(filepath.ToSlash(name)))...)
		for k := range ks {
			ks[k].Duration = float64(t.Length)
		}
		i := len(x.tracks)
		x.tracks, x.keys = append(x.tracks, t), append(x.keys, ks)
		seen := map[string]bool{}
		for _, k := range ks {
			for _, w := range k.Title {
				if !seen[w] {
					seen[w] = true
					x.byTok[w] = append(x.byTok[w], i)
				}
			}
		}
	}
	return x
}

// best is the library track most like sc, and how alike (0..1).
func (x *libIndex) best(sc *soundcloud.Track) (*rbdb.Track, float64) {
	keys := trackKeys(sc)
	cand := map[int]bool{}
	for _, k := range keys {
		for _, w := range k.Title {
			for _, i := range x.byTok[w] {
				cand[i] = true
			}
		}
	}
	var best *rbdb.Track
	bestScore := 0.0
	scLen := float64(sc.DurationMS) / 1000
	for i := range cand {
		t := x.tracks[i]
		if scLen > 0 && t.Length > 0 && math.Abs(scLen-float64(t.Length)) > 15 {
			continue // a different edit
		}
		if s := match.Best(keys, x.keys[i]); s > bestScore {
			best, bestScore = t, s
		}
	}
	return best, bestScore
}

// LinkEntry says a synced playlist's track is one already in the library:
// it joins the playlist in its place, and later syncs remember it.
func (a *App) LinkEntry(playlistURL string, scID int64, trackID string) error {
	sp := a.scByURLLocked(playlistURL)
	if sp == nil {
		return errors.New("that playlist isn't synced any more")
	}
	t := a.Src.Track(trackID)
	if t == nil {
		return errors.New("that track isn't in the library any more")
	}
	a.State.mu.Lock()
	var entry *SCEntry
	for _, e := range sp.Entries {
		if e.SC != nil && e.SC.ID == scID {
			entry = e
		}
	}
	if entry == nil {
		a.State.mu.Unlock()
		return errors.New("that track isn't in the playlist any more")
	}
	previous := entry.TrackID
	entry.Status, entry.TrackID, entry.File, entry.Maybe, entry.Note = "have", t.ID, "", "", ""
	entries := append([]*SCEntry(nil), sp.Entries...)
	pid := sp.PlaylistID
	a.State.save()
	a.State.mu.Unlock()
	// Re-linked: the track it pointed at before leaves the playlist.
	if previous != "" && previous != t.ID && pid != "" {
		if _, err := a.Src.EditPlaylists(PlaylistEdit{Op: "remove", ID: pid, TrackIDs: []string{previous}}); err != nil {
			return err
		}
	}
	a.Decide(scID, t.Path)
	folder := "SoundCloud"
	if sp.Source == "youtube" {
		folder = "YouTube"
	}
	err := a.applyChange(scChange(folder, sp.Title, sp.URL, entries, false))
	if errors.Is(err, rbdb.ErrRunning) {
		return nil // added when rekordbox closes
	}
	return err
}
