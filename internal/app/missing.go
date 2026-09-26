package app

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// MissingReason explains why a track's file can't be found at path.
func MissingReason(path string) string {
	p := filepath.FromSlash(path)
	switch {
	case runtime.GOOS == "darwin" && strings.HasPrefix(p, "/Volumes/"):
		vol := strings.SplitN(strings.TrimPrefix(p, "/Volumes/"), "/", 2)[0]
		if _, err := os.Stat("/Volumes/" + vol); err != nil {
			return "The drive “" + vol + "” isn't connected."
		}
	case runtime.GOOS == "windows" && len(p) > 2 && p[1] == ':':
		if _, err := os.Stat(p[:3]); err != nil {
			return "Drive " + strings.ToUpper(p[:2]) + " isn't connected."
		}
	case runtime.GOOS != "windows" && len(p) > 2 && p[1] == ':':
		return "This track's file is on a Windows computer (" + p[:2] + "); this library came from there."
	case runtime.GOOS == "windows" && strings.HasPrefix(p, `\Users\`) || runtime.GOOS == "windows" && strings.HasPrefix(p, `\Volumes\`):
		return "This track's file is on a Mac; this library came from there."
	}
	if _, err := os.Stat(filepath.Dir(p)); err == nil {
		return "The file isn't in its folder any more: it was moved, renamed or deleted."
	}
	return "Its folder doesn't exist on this computer. If this library is shared with another computer (rekordbox's Cloud Library Sync), the file may only be there."
}

// Moved is a missing track and where its file seems to be now.
type Moved struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// FindMoved looks for missing tracks' files among the scanned music files:
// the same file name (and size, when there are several).
func (a *App) FindMoved() ([]Moved, error) {
	if a.Src == nil {
		return nil, ErrNoSource
	}
	if a.Lib == nil {
		return nil, errors.New("scan your music first (Rescan)")
	}
	byName := map[string][]string{}
	for _, t := range a.Lib.Tracks {
		n := strings.ToLower(filepath.Base(t.Path))
		byName[n] = append(byName[n], t.Path)
	}
	var out []Moved
	for _, t := range a.Src.Tracks() {
		if t.Stream != "" {
			continue
		}
		if _, err := os.Stat(t.Path); err == nil || os.IsPermission(err) {
			continue
		}
		cands := byName[strings.ToLower(filepath.Base(filepath.FromSlash(t.Path)))]
		if len(cands) > 1 && t.FileSize > 0 {
			var same []string
			for _, c := range cands {
				if st, err := os.Stat(c); err == nil && st.Size() == t.FileSize {
					same = append(same, c)
				}
			}
			cands = same
		}
		if len(cands) == 1 {
			out = append(out, Moved{ID: t.ID, Title: strings.TrimSpace(t.Artist + " – " + t.Title), From: t.Path, To: cands[0]})
		}
	}
	return out, nil
}

// Relocate points tracks at their files' new places (ID -> path).
func (a *App) Relocate(moves map[string]string) error {
	if a.Src == nil {
		return ErrNoSource
	}
	for id, p := range moves {
		if _, err := os.Stat(p); err != nil {
			return errors.New("can't read " + filepath.Base(p))
		}
		if a.Src.Track(id) == nil {
			delete(moves, id)
		}
	}
	if len(moves) == 0 {
		return nil
	}
	if err := a.Src.Relocate(moves); err != nil {
		return err
	}
	a.rebuild()
	return nil
}
