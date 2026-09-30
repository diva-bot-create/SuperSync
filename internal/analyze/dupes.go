// Package analyze finds duplicate files and low-quality files in a library,
// and moves duplicates into a quarantine folder (reversibly).
package analyze

import (
	"crypto/sha1"
	"encoding/hex"
	"math"
	"sort"
	"strings"

	"supersync/internal/library"
	"supersync/internal/match"
	"supersync/internal/rekordbox"
)

// Copy is one file in a duplicate group.
type Copy struct {
	*library.Track
	InRekordbox bool     `json:"inRekordbox"`
	Cues        int      `json:"cues"`
	PlayCount   int      `json:"playCount"`
	Playlists   []string `json:"playlists,omitempty"`
	Upscaled    string   `json:"upscaled,omitempty"`
	TrueKbps    int      `json:"trueKbps,omitempty"`
}

// Group is a set of files that appear to be the same recording. Copies[0] is
// the recommended keeper.
type Group struct {
	ID     string  `json:"id"`
	Copies []*Copy `json:"copies"`
	// LengthsDiffer means the copies differ in length by more than 15s, so they
	// may be different edits (radio vs extended); don't auto-resolve these.
	LengthsDiffer bool `json:"lengthsDiffer"`
	// CueWarning is set when a copy that would be moved has rekordbox cue
	// points and the keeper doesn't.
	CueWarning bool `json:"cueWarning"`
	// BySound means some copies were found by their audio, not their names.
	BySound bool `json:"bySound,omitempty"`
}

// FindDuplicates groups library tracks that appear to be the same recording.
func FindDuplicates(lib *library.Library, col rekordbox.Collection) []*Group {
	n := len(lib.Tracks)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	idx := map[*library.Track]int{}
	for i, t := range lib.Tracks {
		idx[t] = i
	}
	for i, t := range lib.Tracks {
		for _, h := range lib.Find(t.Keys) {
			j := idx[h.Track]
			if j <= i || find(i) == find(j) {
				continue
			}
			if match.SameFile(t.Keys, h.Track.Keys) >= match.Sure && !soundsDifferent(t, h.Track) {
				parent[find(j)] = find(i)
			}
		}
	}
	// Files that sound the same, whatever they're called.
	bySound := map[int]bool{}
	for _, p := range soundPairs(lib) {
		i, j := p[0], p[1]
		if find(i) != find(j) {
			parent[find(j)] = find(i)
			bySound[i], bySound[j] = true, true
		}
	}

	members := map[int][]int{}
	for i := range lib.Tracks {
		r := find(i)
		members[r] = append(members[r], i)
	}
	var groups []*Group
	for _, m := range members {
		if len(m) < 2 {
			continue
		}
		g := &Group{}
		for _, i := range m {
			if bySound[i] {
				g.BySound = true
			}
			t := lib.Tracks[i]
			c := &Copy{Track: t, Upscaled: t.Upscaled(), TrueKbps: t.TrueKbps()}
			if ct := col.Lookup(t.Path); ct != nil {
				c.InRekordbox, c.Cues, c.PlayCount, c.Playlists = true, ct.Cues, ct.PlayCount, ct.Playlists
			}
			g.Copies = append(g.Copies, c)
		}
		rank(g)
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].Copies[0].Rel) < strings.ToLower(groups[j].Copies[0].Rel)
	})
	return groups
}

// rank orders copies best-first and fills in the group's flags.
func rank(g *Group) {
	sort.SliceStable(g.Copies, func(i, j int) bool {
		a, b := g.Copies[i], g.Copies[j]
		if qa, qb := a.Quality(), b.Quality(); qa != qb {
			return qa > qb
		}
		if a.Cues != b.Cues {
			return a.Cues > b.Cues
		}
		if len(a.Playlists) != len(b.Playlists) {
			return len(a.Playlists) > len(b.Playlists)
		}
		return len(a.Rel) < len(b.Rel)
	})
	lo, hi := math.Inf(1), 0.0
	paths := make([]string, len(g.Copies))
	for i, c := range g.Copies {
		paths[i] = c.Rel
		if c.Duration > 0 {
			lo, hi = math.Min(lo, c.Duration), math.Max(hi, c.Duration)
		}
		if i > 0 && c.Cues > 0 && g.Copies[0].Cues == 0 {
			g.CueWarning = true
		}
	}
	g.LengthsDiffer = hi-lo > 15
	sort.Strings(paths)
	sum := sha1.Sum([]byte(strings.Join(paths, "\x00")))
	g.ID = hex.EncodeToString(sum[:8])
}
