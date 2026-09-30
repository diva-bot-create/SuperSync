package app

import (
	"errors"
	"math"
	"path"
	"path/filepath"

	"supersync/internal/match"
	"supersync/internal/rbdb"
	"supersync/internal/rekordbox"
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

// LinkResult says what confirming a track did to the other copy: Folded, it
// was cleaned up like a duplicate (Undo clean-up reverses it); Removed, a
// download waiting for rekordbox was set aside.
type LinkResult struct {
	Folded  bool `json:"folded,omitempty"`
	Removed bool `json:"removed,omitempty"`
}

// LinkEntry says a synced playlist's track is one already in the library:
// it joins the playlist in its place, and later syncs remember it.
func (a *App) LinkEntry(playlistURL string, scID int64, trackID string) (*LinkResult, error) {
	sp := a.scByURLLocked(playlistURL)
	if sp == nil {
		return nil, errors.New("that playlist isn't synced any more")
	}
	t := a.Src.Track(trackID)
	if t == nil {
		return nil, errors.New("that track isn't in the library any more")
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
		return nil, errors.New("that track isn't in the playlist any more")
	}
	previous, downloaded, file := entry.TrackID, entry.Status == "downloaded", entry.File
	a.State.mu.Unlock()
	keepID, res := t.ID, &LinkResult{}
	switch {
	case previous != "" && previous != t.ID && downloaded:
		// SuperSync's own download of this song: the same song, so fold
		// the worse copy into the better one, like a duplicate clean-up.
		id, err := a.foldSame(previous, t.ID)
		if err != nil {
			return nil, err
		}
		keepID, res.Folded = id, true
	case previous != "" && previous != t.ID:
		// A wrong match (a different song): it just leaves the playlist.
		if pid := sp.PlaylistID; pid != "" {
			if _, err := a.Src.EditPlaylists(PlaylistEdit{Op: "remove", ID: pid, TrackIDs: []string{previous}}); err != nil {
				return nil, err
			}
		}
	case previous == "" && file != "" && exists(file):
		// Downloaded but not in the library yet: the library copy wins.
		a.Quarantine([]string{file})
		res.Removed = true
	}
	if kt := a.Src.Track(keepID); kt != nil {
		t = kt
	}
	a.State.mu.Lock()
	entry.Status, entry.TrackID, entry.File, entry.Maybe, entry.Note = "have", t.ID, "", "", ""
	entries := append([]*SCEntry(nil), sp.Entries...)
	a.State.save()
	a.State.mu.Unlock()
	a.Decide(scID, t.Path)
	folder := "SoundCloud"
	if sp.Source == "youtube" {
		folder = "YouTube"
	}
	err := a.applyChange(scChange(folder, sp.Title, sp.URL, entries, false))
	if errors.Is(err, rbdb.ErrRunning) {
		return res, nil // added when rekordbox closes
	}
	return res, err
}

// foldSame: the user says library tracks x and y are the same song. The
// better copy stays and the other is folded into it, exactly as a duplicate
// clean-up does (playlists, history, cues; its file goes where the Clean-up
// setting says). It returns the kept track's ID.
func (a *App) foldSame(xID, yID string) (string, error) {
	x, y := a.Src.Track(xID), a.Src.Track(yID)
	switch {
	case x == nil && y == nil:
		return "", errors.New("those tracks aren't in the library any more")
	case x == nil || xID == yID:
		return yID, nil
	case y == nil:
		return xID, nil
	}
	keep, extra := a.betterOf(x, y)
	if rekordbox.NormPath(keep.Path) == rekordbox.NormPath(extra.Path) {
		return keep.ID, nil // two entries for one file: nothing to clean up on disk
	}
	if _, err := a.CleanupDuplicates([]CleanupGroup{{Keep: keep.Path, Extras: []string{extra.Path}, Cues: rbdb.CuesBoth}}); err != nil {
		return "", err
	}
	return keep.ID, nil
}

// betterOf orders two library tracks by quality, better first. On a tie
// the second one (the copy that was already in the library, with its cues)
// wins.
func (a *App) betterOf(x, y *rbdb.Track) (*rbdb.Track, *rbdb.Track) {
	q := func(t *rbdb.Track) int {
		tier, kbps, upscaled := a.Tier(t)
		score := map[string]int{"uhq": 4, "hq": 3, "normal": 2, "low": 1}[tier]*100000 + kbps
		if lossless := t.FileType == 5 || t.FileType == 11 || t.FileType == 12; lossless && upscaled == "" {
			score += 10000 // a real WAV/FLAC/AIFF beats any MP3
		}
		return score
	}
	if q(x) > q(y) {
		return x, y
	}
	return y, x
}
