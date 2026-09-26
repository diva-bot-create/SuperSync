package analyze

import (
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"supersync/internal/audio"
	"supersync/internal/library"
	"supersync/internal/match"
)

// Upgrade is a track whose best copy in the library is below the quality bar.
type Upgrade struct {
	*library.Track
	Reason   string `json:"reason"`
	Links    Links  `json:"links"`
	Upscaled string `json:"upscaled,omitempty"`
	TrueKbps int    `json:"trueKbps,omitempty"`
}

// Links are store searches for buying a better copy.
type Links struct {
	Beatport   string `json:"beatport"`
	Bandcamp   string `json:"bandcamp"`
	Traxsource string `json:"traxsource"`
}

// NeedsUpgrade reports whether a file is below minKbps (MP3-equivalent), and why.
func NeedsUpgrade(in *audio.Info, minKbps int) (bool, string) {
	if in.Lossless && in.Upscaled() == "" {
		return false, ""
	}
	fromSC := strings.Contains(strings.ToLower(in.Comment+" "+in.Path), "soundcloud")
	if up := in.Upscaled(); up != "" && in.TrueKbps() < minKbps-10 {
		r := in.QualityLabel() + ", " + up
		if fromSC {
			r += " · SoundCloud rip"
		}
		return true, r
	}
	eq := in.Quality() / 100 // MP3-equivalent kbps (AAC/Opus are weighted up)
	if eq >= minKbps-10 {
		return false, ""
	}
	r := in.QualityLabel()
	if fromSC {
		r += " · SoundCloud rip"
	}
	return true, r
}

// FindUpgrades lists low-quality tracks that don't already have a good copy
// elsewhere in the library.
func FindUpgrades(lib *library.Library, groups []*Group, minKbps int) []*Upgrade {
	hasGoodCopy := map[*library.Track]bool{}
	for _, g := range groups {
		if bad, _ := NeedsUpgrade(&g.Copies[0].Info, minKbps); !bad {
			for _, c := range g.Copies[1:] {
				hasGoodCopy[c.Track] = true
			}
		}
	}
	var out []*Upgrade
	for _, t := range lib.Tracks {
		if hasGoodCopy[t] {
			continue
		}
		if bad, why := NeedsUpgrade(&t.Info, minKbps); bad {
			out = append(out, &Upgrade{Track: t, Reason: why, Links: StoreLinks(SearchText(t)), Upscaled: t.Upscaled(), TrueKbps: t.TrueKbps()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Quality() < out[j].Quality() })
	return out
}

// SearchText is "artist title" for store searches, falling back to the filename.
func SearchText(t *library.Track) string {
	if q := match.SearchQuery(t.Keys); q != "" {
		return q
	}
	return strings.TrimSuffix(filepath.Base(t.Rel), filepath.Ext(t.Rel))
}

// StoreLinks builds search URLs for a free-text query.
func StoreLinks(q string) Links {
	e := url.QueryEscape(q)
	return Links{
		Beatport:   "https://www.beatport.com/search?q=" + e,
		Bandcamp:   "https://bandcamp.com/search?item_type=t&q=" + e,
		Traxsource: "https://www.traxsource.com/search?term=" + e,
	}
}
