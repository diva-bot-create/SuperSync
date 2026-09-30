package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"supersync/internal/analyze"
	"supersync/internal/rbdb"
)

// The history lists SuperSync's recent changes to the library, each with the
// backup taken just before it. Undoing one puts the library back as it was
// before that change (so later changes go too, since a backup is the whole
// library), moves any files it moved back, and restores the synced
// playlists' state from then.

// HistoryEntry is one change SuperSync made to the library.
type HistoryEntry struct {
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
	Label  string    `json:"label"`
	Auto   bool      `json:"auto,omitempty"` // made by SuperSync on its own (auto-swap, scheduled sync)
	Backup string    `json:"backup"`         // the library just before
	// DBMod is the library file's modification time right after the change,
	// to tell whether rekordbox has changed it since.
	DBMod time.Time      `json:"dbMod,omitempty"`
	Moves []analyze.Move `json:"moves,omitempty"`
	// Before is the synced playlists' state just before (SCPlaylists and
	// Pending, as saved in state.json).
	Before json.RawMessage `json:"before,omitempty"`
	// SC: a change to a SoundCloud playlist (no library backup), with its
	// tracks from before.
	SC *SCRestore `json:"sc,omitempty"`
}

const historyMax = 25

var historyMu sync.Mutex

func historyPath() string { return filepath.Join(DataDir(), "history.json") }

func loadHistory() []*HistoryEntry {
	var h []*HistoryEntry
	if b, err := os.ReadFile(historyPath()); err == nil {
		json.Unmarshal(b, &h)
	}
	return h
}

func saveHistory(h []*HistoryEntry) error {
	os.MkdirAll(DataDir(), 0o755)
	b, _ := json.Marshal(h)
	tmp := historyPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, historyPath())
}

// syncSnapshot captures the synced playlists' state, to restore on undo.
func (a *App) syncSnapshot() json.RawMessage {
	a.State.mu.Lock()
	defer a.State.mu.Unlock()
	b, _ := json.Marshal(struct {
		SCPlaylists []*SCPlaylist `json:"scPlaylists"`
		Pending     []*Change     `json:"pending"`
	}{a.State.SCPlaylists, a.State.Pending})
	return b
}

// record adds a change to the history. Writes that share a backup (a
// playlist import's interim writes) make one entry: the first's label and
// state, everyone's moved files.
func (a *App) record(label, backup string, before json.RawMessage, moves []analyze.Move, auto bool) {
	if backup == "" {
		return
	}
	historyMu.Lock()
	defer historyMu.Unlock()
	h := loadHistory()
	var dbMod time.Time
	if a.Src != nil {
		if info := a.Src.Info(); info.Kind == "rekordbox" {
			if st, err := os.Stat(info.Path); err == nil {
				dbMod = st.ModTime()
			}
		}
	}
	if n := len(h); n > 0 && h[n-1].Backup == backup {
		h[n-1].Moves = append(h[n-1].Moves, moves...)
		h[n-1].DBMod = dbMod
	} else {
		h = append(h, &HistoryEntry{ID: newID(), At: time.Now(), Label: label, Auto: auto, Backup: backup,
			DBMod: dbMod, Moves: moves, Before: before})
	}
	if len(h) > historyMax {
		h = h[len(h)-historyMax:]
	}
	saveHistory(h)
}

// recordSC adds a change to a SoundCloud playlist to the history.
func (a *App) recordSC(label string, before json.RawMessage, restore *SCRestore) {
	historyMu.Lock()
	defer historyMu.Unlock()
	h := append(loadHistory(), &HistoryEntry{ID: newID(), At: time.Now(), Label: label, Auto: true, Before: before, SC: restore})
	if len(h) > historyMax {
		h = h[len(h)-historyMax:]
	}
	saveHistory(h)
}

// amendLast changes the newest entry (a swap folds its "add the new file"
// write into the clean-up that follows).
func amendLast(f func(e *HistoryEntry)) {
	historyMu.Lock()
	defer historyMu.Unlock()
	h := loadHistory()
	if len(h) == 0 {
		return
	}
	f(h[len(h)-1])
	saveHistory(h)
}

// HistoryItem is a history entry as the page shows it.
type HistoryItem struct {
	ID       string    `json:"id"`
	At       time.Time `json:"at"`
	Label    string    `json:"label"`
	Auto     bool      `json:"auto,omitempty"`
	Files    int       `json:"files,omitempty"`   // files it moved
	Undoable bool      `json:"undoable"`          // its backup still exists
	After    int       `json:"after"`             // later changes an undo takes back too
	Changed  bool      `json:"changed,omitempty"` // rekordbox has changed the library since the newest entry
}

// History lists recent changes, newest first.
func (a *App) History() []HistoryItem {
	historyMu.Lock()
	h := loadHistory()
	historyMu.Unlock()
	changed := a.changedSince(h)
	out := make([]HistoryItem, 0, len(h))
	for i := len(h) - 1; i >= 0; i-- {
		e := h[i]
		ok := false
		if e.Backup != "" {
			_, err := os.Stat(e.Backup)
			ok = err == nil
		} else if e.SC != nil {
			ok = a.Cfg.SCToken != ""
		}
		out = append(out, HistoryItem{ID: e.ID, At: e.At, Label: e.Label, Auto: e.Auto, Files: len(e.Moves),
			Undoable: ok, After: len(h) - 1 - i, Changed: changed})
	}
	return out
}

// changedSince: has rekordbox written to the library since SuperSync's
// newest change?
func (a *App) changedSince(h []*HistoryEntry) bool {
	if len(h) == 0 || a.Src == nil || h[len(h)-1].DBMod.IsZero() {
		return false
	}
	info := a.Src.Info()
	if info.Kind != "rekordbox" {
		return false
	}
	st, err := os.Stat(info.Path)
	return err == nil && st.ModTime().After(h[len(h)-1].DBMod.Add(2*time.Second))
}

// UndoTo puts the library back as it was before the change id, taking back
// every later change too. It returns a note when some files couldn't be
// brought back (deleted permanently, or in the Recycle Bin).
func (a *App) UndoTo(id string) (string, error) {
	if a.Src == nil {
		return "", ErrNoSource
	}
	historyMu.Lock()
	h := loadHistory()
	historyMu.Unlock()
	at := -1
	for i, e := range h {
		if e.ID == id {
			at = i
		}
	}
	if at < 0 {
		return "", errors.New("that change isn't in the history any more")
	}
	e := h[at]
	// The library as it was before this change: the backup of the first
	// library change from here on (SoundCloud changes don't have one).
	backup := ""
	for _, x := range h[at:] {
		if x.Backup != "" {
			backup = x.Backup
			break
		}
	}
	if backup != "" {
		if _, err := os.Stat(backup); err != nil {
			return "", errors.New("the backup from before that change has been cleared out (SuperSync keeps the last 10)")
		}
	}
	// SoundCloud playlists first (the network can fail; nothing's changed
	// yet if it does), newest change first so each ends as it was.
	restored := map[string]bool{}
	for i := len(h) - 1; i >= at; i-- {
		if r := h[i].SC; r != nil {
			if err := a.SC.SetPlaylistTracks(a.Cfg.SCToken, r.PlaylistID, r.Tracks); err != nil {
				return "", fmt.Errorf("couldn't put the songs back on SoundCloud: %w", err)
			}
			restored[r.URL] = true
		}
	}
	if backup != "" {
		if err := a.Src.Restore(backup); err != nil {
			return "", err
		}
	}
	// Files back where they were, newest change first.
	lost := 0
	for i := len(h) - 1; i >= at; i-- {
		for _, m := range h[i].Moves {
			if m.To == "" {
				lost++
			}
		}
		analyze.UndoMoves(h[i].Moves)
	}
	if len(e.Before) > 0 {
		var st struct {
			SCPlaylists []*SCPlaylist `json:"scPlaylists"`
			Pending     []*Change     `json:"pending"`
		}
		if json.Unmarshal(e.Before, &st) == nil {
			a.State.mu.Lock()
			// The waiting list stays as it is now: an undone sync shouldn't
			// come back as a waiting change and be added again.
			a.State.SCPlaylists = st.SCPlaylists
			for _, p := range a.State.SCPlaylists {
				p.Pending = false
				for _, c := range a.State.Pending {
					if c.SCURL == p.URL {
						p.Pending = true
					}
				}
			}
			a.State.save()
			a.State.mu.Unlock()
		}
	}
	a.State.mu.Lock()
	a.State.LastCleanup = nil // it's part of what was undone, or older than it
	a.State.save()
	a.State.mu.Unlock()
	historyMu.Lock()
	saveHistory(loadHistory()[:at])
	historyMu.Unlock()
	a.rebuild()
	// Songs back on a SoundCloud playlist go back in its library playlist.
	for u := range restored {
		if sp := a.scByURLLocked(u); sp != nil {
			a.State.mu.Lock()
			entries := append([]*SCEntry(nil), sp.Entries...)
			title := sp.Title
			a.State.mu.Unlock()
			if err := a.applyChange(scChange("SoundCloud", title, u, entries, false)); err != nil && !errors.Is(err, rbdb.ErrRunning) {
				log.Printf("putting songs back in %s: %v", title, err)
			}
		}
	}
	go a.Scan(nil)
	if lost > 0 {
		bin := "Trash"
		if runtime.GOOS == "windows" {
			bin = "Recycle Bin"
		}
		return fmt.Sprintf("%d file%s had gone to the %s (or been deleted), so SuperSync couldn't put %s back. Restore from the %s if you need to, then click Rescan.",
			lost, plural(lost), bin, map[bool]string{true: "it", false: "them"}[lost == 1], bin), nil
	}
	return "", nil
}

// dropHistoryFrom forgets the entry with this backup and every later one
// (after an undo that restored that backup some other way).
func dropHistoryFrom(backup string) {
	historyMu.Lock()
	defer historyMu.Unlock()
	h := loadHistory()
	for i, e := range h {
		if e.Backup == backup {
			saveHistory(h[:i])
			return
		}
	}
}

// playlistName is a library playlist's name, or "".
func (a *App) playlistName(id string) string {
	if a.Src == nil || id == "" {
		return ""
	}
	var walk func(ps []*rbdb.Playlist) string
	walk = func(ps []*rbdb.Playlist) string {
		for _, p := range ps {
			if p.ID == id {
				return p.Name
			}
			if n := walk(p.Children); n != "" {
				return n
			}
		}
		return ""
	}
	return walk(a.Src.Playlists())
}

// editLabel describes a playlist edit for the history.
func (a *App) editLabel(e PlaylistEdit, name string) string {
	what := "playlist"
	if e.Folder {
		what = "folder"
	}
	n := len(e.TrackIDs)
	switch e.Op {
	case "create":
		return fmt.Sprintf("Created %s “%s”", what, e.Name)
	case "rename":
		return fmt.Sprintf("Renamed “%s” to “%s”", name, e.Name)
	case "delete":
		return fmt.Sprintf("Deleted “%s”", name)
	case "add":
		return fmt.Sprintf("Added %d track%s to “%s”", n, plural(n), name)
	case "remove":
		return fmt.Sprintf("Took %d track%s off “%s”", n, plural(n), name)
	}
	return "Edited playlists"
}

// editPlaylists is Src.EditPlaylists, recorded in the history.
func (a *App) editPlaylists(e PlaylistEdit, label string) (*PlaylistEditResult, error) {
	before := a.syncSnapshot()
	if label == "" {
		label = a.editLabel(e, a.playlistName(e.ID))
	}
	res, err := a.Src.EditPlaylists(e)
	if err == nil {
		a.record(label, res.Backup, before, nil, false)
	}
	return res, err
}
