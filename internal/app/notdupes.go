package app

import (
	"sort"

	"supersync/internal/match"
	"supersync/internal/rbdb"

	"supersync/internal/analyze"
	"supersync/internal/library"
	"supersync/internal/rekordbox"
)

// Files the user said aren't duplicates of each other are remembered as
// pairs, so a later scan doesn't group them again (a new copy of the song
// still shows up, with them).

func pairKey(a, b string) string {
	a, b = rekordbox.NormPath(a), rekordbox.NormPath(b)
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}

// duplicates is FindDuplicates without the groups the user rejected. Files
// are compared by rekordbox's title and artist for them as well as their own
// tags and names, so two songs that look the same in rekordbox are compared
// even when their files are tagged or named differently.
func (a *App) duplicates(lib *library.Library) []*analyze.Group {
	// Work on a copy: the background listening pass fills in fingerprints.
	a.mu.Lock()
	lib = lib.WithExtraKeys(func(*library.Track) []match.Key { return nil })
	a.mu.Unlock()
	if a.Src != nil {
		byPath := map[string]*rbdb.Track{}
		for _, t := range a.Src.Tracks() {
			byPath[rekordbox.NormPath(t.Path)] = t
		}
		lib = lib.WithExtraKeys(func(t *library.Track) []match.Key {
			rt := byPath[rekordbox.NormPath(t.Path)]
			if rt == nil || rt.Title == "" {
				return nil
			}
			ks := match.Parse(rt.Artist, rt.Title)
			for i := range ks {
				ks[i].Duration = t.Duration
			}
			return ks
		})
	}
	gs := analyze.FindDuplicates(lib, a.Col)
	a.mu.Lock()
	rejected := map[string]bool{}
	for _, k := range a.Cfg.NotDuplicates {
		rejected[k] = true
	}
	a.mu.Unlock()
	if len(rejected) == 0 {
		return gs
	}
	out := gs[:0]
	for _, g := range gs {
		if !allRejected(g, rejected) {
			out = append(out, g)
		}
	}
	return out
}

func allRejected(g *analyze.Group, rejected map[string]bool) bool {
	for i := range g.Copies {
		for j := i + 1; j < len(g.Copies); j++ {
			if !rejected[pairKey(g.Copies[i].Path, g.Copies[j].Path)] {
				return false
			}
		}
	}
	return true
}

// RejectDuplicates remembers that these files aren't duplicates of each
// other (or, with undo, forgets it).
func (a *App) RejectDuplicates(paths []string, undo bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	set := map[string]bool{}
	for _, k := range a.Cfg.NotDuplicates {
		set[k] = true
	}
	for i := range paths {
		for j := i + 1; j < len(paths); j++ {
			k := pairKey(paths[i], paths[j])
			if undo {
				delete(set, k)
			} else {
				set[k] = true
			}
		}
	}
	a.Cfg.NotDuplicates = a.Cfg.NotDuplicates[:0]
	for k := range set {
		a.Cfg.NotDuplicates = append(a.Cfg.NotDuplicates, k)
	}
	sort.Strings(a.Cfg.NotDuplicates)
	return a.Cfg.Save()
}
