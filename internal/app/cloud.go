package app

import (
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"

	"supersync/internal/library"
	"supersync/internal/match"
	"supersync/internal/rbdb"
)

// rekordbox's Cloud Library Sync stores tracks as "/contents_…/artist/album/
// file" and, on some computers, doesn't record where its local copy is. The
// files are usually still here (in the music or download folder) under their
// full names; find them by path, name, or tags and length.

var contentsPrefix = regexp.MustCompile(`^/contents_[^/]+/`)

var resolvingCloud atomic.Bool

// applyCloudPaths points Cloud Library Sync tracks at files already found
// for them, and reports whether any haven't been looked for yet. Called with
// a.mu held.
func (a *App) applyCloudPathsLocked() (unsearched bool) {
	for _, t := range a.Src.Tracks() {
		if !t.Cloud || exists(t.Path) {
			continue
		}
		p, tried := a.cloudFound[t.StoredPath]
		if p != "" && exists(p) {
			t.Path = p
		} else if !tried {
			unsearched = true
		}
	}
	return unsearched
}

// resolveCloudFiles searches the download folder and the Music folder for
// Cloud Library Sync tracks' files, then rebuilds the library view.
func (a *App) resolveCloudFiles() {
	if !resolvingCloud.CompareAndSwap(false, true) {
		return
	}
	defer resolvingCloud.Store(false)
	var need []*rbdb.Track
	for _, t := range a.Src.Tracks() {
		if t.Cloud && !exists(t.Path) {
			need = append(need, t)
		}
	}
	if len(need) == 0 {
		return
	}
	var libs []*library.Library
	for _, d := range cloudSearchDirs(a.Cfg.MusicDir) {
		if l, err := library.Scan(d, nil); err == nil {
			libs = append(libs, l)
		}
	}
	byName := map[string][]*library.Track{}
	for _, l := range libs {
		for _, t := range l.Tracks {
			n := strings.ToLower(filepath.Base(t.Path))
			byName[n] = append(byName[n], t)
		}
	}
	found := map[string]string{}
	for _, t := range need {
		found[t.StoredPath] = findCloudFile(t, byName, libs)
	}
	a.mu.Lock()
	if a.cloudFound == nil {
		a.cloudFound = map[string]string{}
	}
	for k, v := range found {
		a.cloudFound[k] = v
	}
	a.mu.Unlock()
	a.rebuild()
	for _, v := range found {
		if v != "" {
			go a.Scan(nil) // read the found files (quality, upscales)
			break
		}
	}
}

func cloudSearchDirs(musicDir string) []string {
	var out []string
	add := func(d string) {
		if d == "" || !exists(d) {
			return
		}
		for _, o := range out {
			if within(d, o) {
				return // already covered
			}
		}
		out = append(out, d)
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		add(filepath.Join(home, "Music"))
	}
	add(musicDir)
	return out
}

func findCloudFile(t *rbdb.Track, byName map[string][]*library.Track, libs []*library.Library) string {
	rel := strings.ToLower(contentsPrefix.ReplaceAllString(t.StoredPath, ""))
	cands := byName[path.Base(rel)]
	// Same layout under some folder, then the same name (size breaks ties).
	for _, c := range cands {
		if strings.HasSuffix(strings.ToLower(filepath.ToSlash(c.Path)), "/"+rel) {
			return c.Path
		}
	}
	if len(cands) == 1 {
		return cands[0].Path
	}
	for _, c := range cands {
		if t.FileSize > 0 && c.Size == t.FileSize {
			return c.Path
		}
	}
	// The cloud name may be cut short: match on the track's tags and length.
	keys := match.Parse(t.Artist, t.Title)
	best, bestScore := "", 0.0
	for _, l := range libs {
		for _, h := range l.Find(keys) {
			if h.Score < match.Sure {
				continue
			}
			if t.Length > 0 && h.Track.Duration > 0 && math.Abs(h.Track.Duration-float64(t.Length)) > 2 {
				continue
			}
			s := h.Score
			if t.FileSize > 0 && h.Track.Size == t.FileSize {
				s += 1 // the very same file
			}
			if s > bestScore {
				best, bestScore = h.Track.Path, s
			}
		}
	}
	return best
}
