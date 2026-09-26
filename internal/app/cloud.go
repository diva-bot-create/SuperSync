package app

import (
	"golang.org/x/text/unicode/norm"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
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

// cloudUnsearchedLocked reports whether any Cloud Library Sync track's file
// hasn't been looked for yet. Called with a.mu held.
func (a *App) cloudUnsearchedLocked() bool {
	for _, t := range a.Src.Tracks() {
		if !t.Cloud || exists(t.Path) {
			continue
		}
		if _, tried := a.cloudFound[t.StoredPath]; !tried {
			return true
		}
	}
	return false
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
	idx := &cloudIndex{byName: map[string][]*library.Track{}, bySize: map[string][]*library.Track{}, libs: libs}
	for _, l := range libs {
		for _, t := range l.Tracks {
			n := strings.ToLower(filepath.Base(t.Path))
			idx.byName[n] = append(idx.byName[n], t)
			idx.bySize[sizeKey(t.Size, t.Path)] = append(idx.bySize[sizeKey(t.Size, t.Path)], t)
			idx.all = append(idx.all, t)
		}
	}
	found := map[string]string{}
	for _, t := range need {
		found[t.StoredPath] = idx.find(t)
	}
	a.mu.Lock()
	all := map[string]string{}
	for k, v := range a.cloudFound {
		all[k] = v
	}
	for k, v := range found {
		all[k] = v
	}
	a.cloudFound = all
	a.mu.Unlock()
	if fs, ok := a.Src.(interface{ SetFound(map[string]string) }); ok {
		fs.SetFound(all)
	}
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

type cloudIndex struct {
	byName, bySize map[string][]*library.Track
	all            []*library.Track
	libs           []*library.Library
}

func sizeKey(size int64, p string) string {
	return strings.ToLower(filepath.Ext(p)) + ":" + strconv.FormatInt(size, 10)
}

// nameKey compares file names the way rekordbox's cloud names are made:
// lowercased, in one Unicode form, without the extension.
func nameKey(p string) string {
	b := strings.TrimSuffix(path.Base(filepath.ToSlash(p)), path.Ext(p))
	return strings.TrimSpace(norm.NFC.String(strings.ToLower(b)))
}

func (x *cloudIndex) find(t *rbdb.Track) string {
	rel := strings.ToLower(contentsPrefix.ReplaceAllString(t.StoredPath, ""))
	ext := strings.ToLower(path.Ext(rel))
	cands := x.byName[path.Base(rel)]
	// 1. The same layout under some folder.
	for _, c := range cands {
		if strings.HasSuffix(strings.ToLower(filepath.ToSlash(c.Path)), "/"+rel) {
			return c.Path
		}
	}
	// 2. The same name (the size breaks ties).
	if len(cands) == 1 {
		return cands[0].Path
	}
	for _, c := range cands {
		if t.FileSize > 0 && c.Size == t.FileSize {
			return c.Path
		}
	}
	// 3. The very same file: exactly the size rekordbox recorded, same type.
	if t.FileSize > 0 {
		if same := x.bySize[ext+":"+strconv.FormatInt(t.FileSize, 10)]; len(same) == 1 {
			return same[0].Path
		}
	}
	// 4. rekordbox cuts cloud names short: a file whose name starts with it.
	cut := nameKey(rel)
	if len(cut) >= 12 {
		var hits []*library.Track
		for _, c := range x.all {
			if strings.ToLower(filepath.Ext(c.Path)) == ext && strings.HasPrefix(nameKey(c.Path), cut) {
				hits = append(hits, c)
			}
		}
		if len(hits) == 1 {
			return hits[0].Path
		}
		for _, h := range hits {
			if t.FileSize > 0 && h.Size == t.FileSize {
				return h.Path
			}
		}
	}
	// 5. The track's tags and length.
	return findByTags(t, x.libs)
}

func findByTags(t *rbdb.Track, libs []*library.Library) string {
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
