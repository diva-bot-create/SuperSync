// Package library scans a music folder into an index of tracks, caching the
// results so later scans only read new or changed files.
package library

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"supersync/internal/audio"
	"supersync/internal/fingerprint"
	"supersync/internal/match"
)

// QuarantineDir is where duplicates are moved; it is never scanned.
const QuarantineDir = "_SuperSync Duplicates"

type Track struct {
	audio.Info
	Rel  string      `json:"rel"`
	Keys []match.Key `json:"-"`
	// FP is the file's audio fingerprint (filled in by a background pass);
	// FPFailed means the audio couldn't be read for one.
	FP       fingerprint.FP `json:"fp,omitempty"`
	FPFailed bool           `json:"fpFailed,omitempty"`
}

type Library struct {
	// Key identifies the scan in the cache: the folder for folder scans, or
	// e.g. "rekordbox:/path/master.db" for a list of files from a library.
	Key       string    `json:"key"`
	Root      string    `json:"root"`
	ScannedAt time.Time `json:"scannedAt"`
	Tracks    []*Track  `json:"tracks"`

	index  map[string][]int
	byPath map[string]*Track
}

// Hit is a library track that may match a query.
type Hit struct {
	Track *Track
	Score float64
}

// Progress is called during a scan with the number of files handled so far.
type Progress func(done, total int)

func cachePath(key string) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha1.Sum([]byte(key))
	return filepath.Join(dir, "SuperSync", "library-"+hex.EncodeToString(sum[:6])+".json"), nil
}

// LoadCached returns the last scan for key (a folder, or a ScanFiles key)
// without touching the disk, or nil.
func LoadCached(key string) *Library {
	p, err := cachePath(key)
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var l Library
	if json.Unmarshal(b, &l) != nil || (l.Key != key && l.Root != key) {
		return nil
	}
	l.Key = key
	l.build()
	return &l
}

// Scan walks root, reusing cached metadata for files whose size and
// modification time are unchanged.
func Scan(root string, progress Progress) (*Library, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(root); err != nil {
		return nil, err
	} else if !st.IsDir() {
		return nil, &fs.PathError{Op: "scan", Path: root, Err: fs.ErrInvalid}
	}

	var paths []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable folder: skip, keep going
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (strings.HasPrefix(name, ".") || name == QuarantineDir) {
				return filepath.SkipDir
			}
			return nil
		}
		// "._x.mp3" are macOS resource forks on non-Mac drives, not audio.
		if strings.HasPrefix(name, ".") || !audio.IsAudio(name) {
			return nil
		}
		paths = append(paths, p)
		return nil
	})

	return scanPaths(root, root, paths, progress)
}

// ScanFiles reads a given list of files (e.g. every track in the rekordbox
// collection). Missing files are skipped.
func ScanFiles(key string, paths []string, progress Progress) (*Library, error) {
	return scanPaths(key, commonDir(paths), paths, progress)
}

func commonDir(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	dir := filepath.Dir(paths[0])
	for _, p := range paths[1:] {
		for dir != "" && !strings.HasPrefix(p, dir+string(filepath.Separator)) {
			parent := filepath.Dir(dir)
			if parent == dir {
				return dir
			}
			dir = parent
		}
	}
	return dir
}

func scanPaths(key, root string, paths []string, progress Progress) (*Library, error) {
	old := map[string]*Track{}
	if prev := LoadCached(key); prev != nil {
		for _, t := range prev.Tracks {
			old[t.Path] = t
		}
	}
	tracks := make([]*Track, len(paths))
	var mu sync.Mutex
	done := 0
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				p := paths[i]
				rel, err := filepath.Rel(root, p)
				if err != nil || root == "" {
					rel = p
				}
				rel = filepath.ToSlash(rel)
				var t *Track
				if st, err := os.Stat(p); err == nil {
					if o := old[p]; o != nil && o.Size == st.Size() && o.ModTime == st.ModTime().Unix() {
						t = o
						t.Rel = rel
					}
				}
				if t == nil {
					if in, err := audio.Read(p); err == nil {
						t = &Track{Info: *in, Rel: rel}
					}
				}
				tracks[i] = t
				mu.Lock()
				done++
				if progress != nil && (done%25 == 0 || done == len(paths)) {
					progress(done, len(paths))
				}
				mu.Unlock()
			}
		}()
	}
	for i := range paths {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	l := &Library{Key: key, Root: root, ScannedAt: time.Now()}
	for _, t := range tracks {
		if t != nil {
			l.Tracks = append(l.Tracks, t)
		}
	}
	sort.Slice(l.Tracks, func(i, j int) bool { return l.Tracks[i].Rel < l.Tracks[j].Rel })
	l.build()
	return l, l.save()
}

// Save writes the library cache (e.g. after spectrum checks filled in cutoffs).
func (l *Library) Save() error { return l.save() }

func (l *Library) save() error {
	key := l.Key
	if key == "" {
		key = l.Root
	}
	p, err := cachePath(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Keys returns the match keys for a file's tags and its filename.
func Keys(in *audio.Info) []match.Key {
	var keys []match.Key
	if in.Title != "" {
		keys = append(keys, match.Parse(in.Artist, in.Title)...)
	}
	base := strings.TrimSuffix(filepath.Base(in.Path), filepath.Ext(in.Path))
	keys = append(keys, match.ParseFilename(base)...)
	for i := range keys {
		keys[i].Duration = in.Duration
	}
	return keys
}

func (l *Library) build() {
	l.index = map[string][]int{}
	l.byPath = map[string]*Track{}
	for i, t := range l.Tracks {
		if t.Path == "" {
			t.Path = filepath.Join(l.Root, filepath.FromSlash(t.Rel))
		}
		l.byPath[t.Path] = t
		t.Keys = Keys(&t.Info)
		seen := map[string]bool{}
		for _, k := range t.Keys {
			for _, w := range append(k.Title, strings.Join(k.Title, "")) {
				if !seen[w] {
					seen[w] = true
					l.index[w] = append(l.index[w], i)
				}
			}
		}
	}
}

// Find returns library tracks scoring at least match.Maybe against keys, best first.
func (l *Library) Find(keys []match.Key) []Hit {
	cand := map[int]bool{}
	for _, k := range keys {
		for _, w := range append(k.Title, strings.Join(k.Title, "")) {
			for _, i := range l.index[w] {
				cand[i] = true
			}
		}
	}
	var hits []Hit
	for i := range cand {
		t := l.Tracks[i]
		if s := match.Best(keys, t.Keys); s >= match.Maybe {
			hits = append(hits, Hit{t, s})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Track.Quality() > hits[j].Track.Quality()
	})
	return hits
}

// ByPath returns the track at an absolute path, or nil.
func (l *Library) ByPath(p string) *Track {
	return l.byPath[p]
}

// WithExtraKeys returns a copy of the library whose tracks also match on
// extra keys (e.g. the title and artist rekordbox has for the file, which
// may differ from the file's own tags and name). The tracks are copied; the
// original library isn't changed.
func (l *Library) WithExtraKeys(extra func(t *Track) []match.Key) *Library {
	out := &Library{Key: l.Key, Root: l.Root, ScannedAt: l.ScannedAt, index: map[string][]int{}, byPath: map[string]*Track{}}
	for i, t := range l.Tracks {
		c := *t
		c.Keys = append(append([]match.Key(nil), t.Keys...), extra(t)...)
		out.Tracks = append(out.Tracks, &c)
		out.byPath[c.Path] = &c
		seen := map[string]bool{}
		for _, k := range c.Keys {
			for _, w := range append(k.Title, strings.Join(k.Title, "")) {
				if !seen[w] {
					seen[w] = true
					out.index[w] = append(out.index[w], i)
				}
			}
		}
	}
	return out
}
