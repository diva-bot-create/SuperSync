package app

import (
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"supersync/internal/anlz"
	"supersync/internal/rbdb"
	"supersync/internal/rekordbox"
)

// Source is the DJ library SuperSync treats as the truth: rekordbox's own
// database when rekordbox is installed, otherwise SuperSync's own library
// file (rekordbox XML format, importable into rekordbox later).
type Source interface {
	Info() SourceInfo
	Tracks() []*rbdb.Track
	Track(id string) *rbdb.Track
	Playlists() []*rbdb.Playlist
	PlaylistTrackIDs(id string) []string
	Cues(id string) []rbdb.Cue
	// Analysis is the beatgrid (and rekordbox's own waveform) for a track, or nil.
	Analysis(id string) *anlz.Analysis
	TrackPlaylists() map[string][]string
	// Apply makes the changes; for rekordbox it fails with rbdb.ErrRunning while rekordbox is open.
	Apply(c *Change) (*Applied, error)
	// Merge folds duplicate entries into the ones kept (see rbdb.Tx.Merge) and
	// returns the backup taken first.
	Merge(ops []MergeOp) (backup string, err error)
	// Restore puts a backup from Merge/Apply back.
	Restore(backup string) error
	// EditPlaylists creates, renames or deletes playlists, or adds or removes
	// tracks; for rekordbox it fails with rbdb.ErrRunning while rekordbox is open.
	EditPlaylists(e PlaylistEdit) (*PlaylistEditResult, error)
	// Relocate points tracks (ID -> new path) at files that have moved.
	Relocate(moves map[string]string) error
	// Refresh reloads if the library changed on disk; reports whether it did.
	Refresh() bool
	Close()
}

type SourceInfo struct {
	Kind      string    `json:"kind"` // "rekordbox" or "supersync"
	Path      string    `json:"path"`
	Tracks    int       `json:"tracks"`
	Playlists int       `json:"playlists"`
	Running   bool      `json:"running"` // rekordbox is open (writes must wait)
	LoadedAt  time.Time `json:"loadedAt"`
	Note      string    `json:"note,omitempty"`
}

// Change is a batch of library edits: add tracks, optionally into a playlist
// (created inside Folder if needed, or appended to if it already exists).
type Change struct {
	ID        string       `json:"id"`
	Label     string       `json:"label"`
	Folder    string       `json:"folder,omitempty"`
	Playlist  string       `json:"playlist,omitempty"`
	Items     []ChangeItem `json:"items"`
	SCURL     string       `json:"scUrl,omitempty"`
	CreatedAt time.Time    `json:"createdAt"`
	// Ordered puts the playlist in the order of Items (the final step of an import).
	Ordered bool `json:"ordered,omitempty"`
	// Interim marks a partial update while an import is still downloading.
	Interim bool `json:"interim,omitempty"`
}

// PlaylistEdit is one change to the playlists.
type PlaylistEdit struct {
	Op       string   `json:"op"` // create, rename, delete, add, remove
	ID       string   `json:"id,omitempty"`
	Parent   string   `json:"parent,omitempty"` // create: the folder ("" or "root" for the top)
	Name     string   `json:"name,omitempty"`
	Folder   bool     `json:"folder,omitempty"`
	TrackIDs []string `json:"trackIds,omitempty"`
}

type PlaylistEditResult struct {
	ID      string   `json:"id,omitempty"` // create: the new playlist
	Deleted []string `json:"deleted,omitempty"`
	Backup  string   `json:"backup,omitempty"`
}

type ChangeItem struct {
	TrackID string         `json:"trackId,omitempty"` // already in the library
	New     *rbdb.NewTrack `json:"new,omitempty"`     // a file to add
	SCID    int64          `json:"scId,omitempty"`
}

// MergeOp folds Extra into Keep; CueShift (seconds) carries Extra's cues over
// when Keep has none, nil to leave cues alone.
type MergeOp struct {
	Keep, Extra string
	CueShift    *float64
}

type Applied struct {
	PlaylistID string   `json:"playlistId"`
	TrackIDs   []string `json:"trackIds"` // parallel to Change.Items
	Backup     string   `json:"backup,omitempty"`
}

// ---------------- rekordbox database ----------------

type rbSource struct {
	mu         sync.RWMutex
	loc        *rbdb.Location
	db         *rbdb.DB
	tracks     []*rbdb.Track
	byID       map[string]*rbdb.Track
	playlists  []*rbdb.Playlist
	trackPls   map[string][]string
	backupRoot string
}

func openRekordbox(loc *rbdb.Location, backupRoot string) (*rbSource, error) {
	s := &rbSource{loc: loc, backupRoot: backupRoot}
	return s, s.load()
}

func (s *rbSource) load() error {
	db, err := rbdb.Open(s.loc)
	if err != nil {
		return err
	}
	tracks, err := db.Tracks()
	if err != nil {
		db.Close()
		return err
	}
	pls, err := db.Playlists()
	if err != nil {
		db.Close()
		return err
	}
	tp, _ := db.TrackPlaylists()
	byID := make(map[string]*rbdb.Track, len(tracks))
	for _, t := range tracks {
		byID[t.ID] = t
	}
	s.mu.Lock()
	old := s.db
	s.db, s.tracks, s.byID, s.playlists, s.trackPls = db, tracks, byID, pls, tp
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return nil
}

func (s *rbSource) Info() SourceInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return SourceInfo{Kind: "rekordbox", Path: s.loc.DB, Tracks: len(s.tracks), Playlists: countPlaylists(s.playlists),
		Running: rbdb.Running(), LoadedAt: s.db.LoadedAt}
}
func (s *rbSource) Tracks() []*rbdb.Track { s.mu.RLock(); defer s.mu.RUnlock(); return s.tracks }
func (s *rbSource) Track(id string) *rbdb.Track {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byID[id]
}
func (s *rbSource) Playlists() []*rbdb.Playlist {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.playlists
}
func (s *rbSource) TrackPlaylists() map[string][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.trackPls
}
func (s *rbSource) PlaylistTrackIDs(id string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids, _ := s.db.PlaylistTrackIDs(id)
	return ids
}
func (s *rbSource) Cues(id string) []rbdb.Cue {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, _ := s.db.Cues(id)
	return c
}
func (s *rbSource) Analysis(id string) *anlz.Analysis {
	t := s.Track(id)
	if t == nil || t.Analysis == "" {
		return nil
	}
	return readAnalysis(t.Analysis)
}

// Analysis files are read once per change (they're rewritten when rekordbox re-analyses).
var anlzCache sync.Map // path -> cachedAnalysis

type cachedAnalysis struct {
	mod time.Time
	a   *anlz.Analysis
}

func readAnalysis(path string) *anlz.Analysis {
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if c, ok := anlzCache.Load(path); ok && c.(cachedAnalysis).mod.Equal(st.ModTime()) {
		return c.(cachedAnalysis).a
	}
	a, err := anlz.Read(path)
	if err != nil {
		return nil
	}
	anlzCache.Store(path, cachedAnalysis{st.ModTime(), a})
	return a
}

func (s *rbSource) Refresh() bool {
	s.mu.RLock()
	changed := s.db.Changed()
	s.mu.RUnlock()
	if changed && s.load() == nil {
		return true
	}
	return false
}
func (s *rbSource) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		s.db.Close()
	}
}

func (s *rbSource) Apply(c *Change) (*Applied, error) {
	tx, err := rbdb.Begin(s.loc)
	if err != nil {
		return nil, err
	}
	tx.ReuseBackup = c.SCURL != "" // an import's writes share one backup
	res := &Applied{}
	fail := func(err error) (*Applied, error) { tx.Rollback(); return nil, err }
	var ids []string
	for _, it := range c.Items {
		id := it.TrackID
		if it.New != nil {
			if id, err = tx.AddTrack(*it.New); err != nil {
				return fail(err)
			}
		}
		res.TrackIDs = append(res.TrackIDs, id)
		if id != "" {
			ids = append(ids, id)
		}
	}
	if c.Playlist != "" {
		parent := "root"
		if c.Folder != "" {
			if parent, err = tx.FindPlaylist(c.Folder, "root", true); err != nil {
				return fail(err)
			}
			if parent == "" {
				if parent, err = tx.CreatePlaylist(c.Folder, "root", true); err != nil {
					return fail(err)
				}
			}
		}
		pid, err := tx.FindPlaylist(c.Playlist, parent, false)
		if err != nil {
			return fail(err)
		}
		if pid == "" {
			if pid, err = tx.CreatePlaylist(c.Playlist, parent, false); err != nil {
				return fail(err)
			}
		}
		if err := tx.AddToPlaylist(pid, ids...); err != nil {
			return fail(err)
		}
		if c.Ordered {
			if err := tx.OrderPlaylist(pid, ids); err != nil {
				return fail(err)
			}
		}
		res.PlaylistID = pid
	}
	if res.Backup, err = tx.Commit(s.backupRoot); err != nil {
		return nil, err
	}
	return res, s.load()
}

func (s *rbSource) Merge(ops []MergeOp) (string, error) {
	tx, err := rbdb.Begin(s.loc)
	if err != nil {
		return "", err
	}
	for _, op := range ops {
		if err := tx.Merge(op.Keep, op.Extra, op.CueShift); err != nil {
			tx.Rollback()
			return "", err
		}
	}
	backup, err := tx.Commit(s.backupRoot)
	if err != nil {
		return "", err
	}
	return backup, s.load()
}

func (s *rbSource) EditPlaylists(e PlaylistEdit) (*PlaylistEditResult, error) {
	tx, err := rbdb.Begin(s.loc)
	if err != nil {
		return nil, err
	}
	res := &PlaylistEditResult{}
	switch e.Op {
	case "create":
		res.ID, err = tx.CreatePlaylist(e.Name, e.Parent, e.Folder)
	case "rename":
		err = tx.RenamePlaylist(e.ID, e.Name)
	case "delete":
		res.Deleted, err = tx.DeletePlaylist(e.ID)
	case "add":
		err = tx.AddToPlaylist(e.ID, e.TrackIDs...)
	case "remove":
		err = tx.RemoveFromPlaylist(e.ID, e.TrackIDs...)
	default:
		err = fmt.Errorf("unknown playlist change %q", e.Op)
	}
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	if res.Backup, err = tx.Commit(s.backupRoot); err != nil {
		return nil, err
	}
	return res, s.load()
}

func (s *rbSource) Relocate(moves map[string]string) error {
	tx, err := rbdb.Begin(s.loc)
	if err != nil {
		return err
	}
	for id, p := range moves {
		if err := tx.Relocate(id, p); err != nil {
			tx.Rollback()
			return err
		}
	}
	if _, err := tx.Commit(s.backupRoot); err != nil {
		return err
	}
	return s.load()
}

func (s *rbSource) Restore(backup string) error {
	if err := rbdb.Restore(s.loc, backup); err != nil {
		return err
	}
	return s.load()
}

func countPlaylists(ps []*rbdb.Playlist) int {
	n := 0
	for _, p := range ps {
		if p.Kind != "folder" {
			n++
		}
		n += countPlaylists(p.Children)
	}
	return n
}

// ---------------- SuperSync's own library (rekordbox XML) ----------------

type xmlSource struct {
	mu   sync.RWMutex
	lib  *rekordbox.Library
	mod  time.Time
	byID map[string]*rbdb.Track
	list []*rbdb.Track
}

func openXMLLibrary(path string) (*xmlSource, error) {
	s := &xmlSource{}
	return s, s.load(path)
}

func (s *xmlSource) load(path string) error {
	lib, err := rekordbox.LoadLibrary(path)
	if err != nil {
		return err
	}
	var mod time.Time
	if st, err := os.Stat(path); err == nil {
		mod = st.ModTime()
	}
	byID := map[string]*rbdb.Track{}
	var list []*rbdb.Track
	for _, t := range lib.Tracks {
		tr := xmlTrack(t)
		byID[tr.ID] = tr
		list = append(list, tr)
	}
	s.mu.Lock()
	s.lib, s.mod, s.byID, s.list = lib, mod, byID, list
	s.mu.Unlock()
	return nil
}

func xmlTrack(t *rekordbox.LibTrack) *rbdb.Track {
	num := func(k string) float64 { v, _ := strconv.ParseFloat(t.Get(k), 64); return v }
	tr := &rbdb.Track{
		ID: t.ID, Path: t.Path(), Title: t.Get("Name"), Artist: t.Get("Artist"), Album: t.Get("Album"),
		Genre: t.Get("Genre"), Key: t.Get("Tonality"), Label: t.Get("Label"), Comment: t.Get("Comments"),
		BPM: num("AverageBpm"), Length: int(num("TotalTime")), BitRate: int(num("BitRate")),
		SampleRate: int(num("SampleRate")), FileSize: int64(num("Size")), PlayCount: int(num("PlayCount")),
		Added: t.Get("DateAdded"), Year: int(num("Year")), Cues: len(t.Marks), Analysed: len(t.Tempos) > 0,
	}
	if r := int(num("Rating")); r > 5 {
		tr.Rating = (r + 25) / 51
	}
	switch strings.ToLower(filepath.Ext(tr.Path)) {
	case ".mp3":
		tr.FileType = 1
	case ".m4a", ".mp4", ".aac":
		tr.FileType = 4
	case ".flac":
		tr.FileType = 5
	case ".wav":
		tr.FileType = 11
	case ".aif", ".aiff":
		tr.FileType = 12
	}
	return tr
}

func (s *xmlSource) Info() SourceInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return SourceInfo{Kind: "supersync", Path: s.lib.Path, Tracks: len(s.list), Playlists: countPlaylists(s.playlistsLocked()),
		LoadedAt: s.mod, Note: "No rekordbox library was found, so SuperSync keeps its own. rekordbox can import it later (it's rekordbox XML)."}
}
func (s *xmlSource) Tracks() []*rbdb.Track { s.mu.RLock(); defer s.mu.RUnlock(); return s.list }
func (s *xmlSource) Track(id string) *rbdb.Track {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byID[id]
}
func (s *xmlSource) Playlists() []*rbdb.Playlist {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.playlistsLocked()
}
func (s *xmlSource) playlistsLocked() []*rbdb.Playlist {
	var conv func(n *rekordbox.LibNode, parent string) *rbdb.Playlist
	conv = func(n *rekordbox.LibNode, parent string) *rbdb.Playlist {
		p := &rbdb.Playlist{ID: n.ID, ParentID: parent, Name: n.Name, Kind: "playlist", Count: len(n.Keys)}
		if n.Folder {
			p.Kind = "folder"
		}
		for i, c := range n.Children {
			cp := conv(c, n.ID)
			cp.Seq = i
			p.Children = append(p.Children, cp)
		}
		return p
	}
	return conv(s.lib.Root, "").Children
}
func (s *xmlSource) PlaylistTrackIDs(id string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if n := s.lib.Node(id); n != nil {
		return n.Keys
	}
	return nil
}
func (s *xmlSource) TrackPlaylists() map[string][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string][]string{}
	var walk func(n *rekordbox.LibNode, prefix string)
	walk = func(n *rekordbox.LibNode, prefix string) {
		name := n.Name
		if prefix != "" {
			name = prefix + " / " + name
		}
		for _, k := range n.Keys {
			out[k] = append(out[k], name)
		}
		for _, c := range n.Children {
			walk(c, name)
		}
	}
	for _, c := range s.lib.Root.Children {
		walk(c, "")
	}
	return out
}
func (s *xmlSource) Cues(id string) []rbdb.Cue {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := s.lib.Track(id)
	if t == nil {
		return nil
	}
	var out []rbdb.Cue
	for _, m := range t.Marks {
		start, _ := strconv.ParseFloat(m.Get("Start"), 64)
		c := rbdb.Cue{InMs: int(math.Round(start * 1000)), OutMs: -1, Color: -1, Comment: m.Get("Name")}
		if e, err := strconv.ParseFloat(m.Get("End"), 64); err == nil && m.Get("End") != "" {
			c.OutMs = int(math.Round(e * 1000))
		}
		if n, err := strconv.Atoi(m.Get("Num")); err == nil && n >= 0 {
			c.Hot = n + 1
		}
		if r, g, b := m.Get("Red"), m.Get("Green"), m.Get("Blue"); r != "" {
			ri, _ := strconv.Atoi(r)
			gi, _ := strconv.Atoi(g)
			bi, _ := strconv.Atoi(b)
			c.RGB = fmt.Sprintf("#%02x%02x%02x", ri, gi, bi)
		}
		out = append(out, c)
	}
	return out
}

// Analysis builds a beatgrid from the XML's TEMPO markers.
func (s *xmlSource) Analysis(id string) *anlz.Analysis {
	s.mu.RLock()
	t := s.lib.Track(id)
	s.mu.RUnlock()
	if t == nil || len(t.Tempos) == 0 {
		return nil
	}
	end, _ := strconv.ParseFloat(t.Get("TotalTime"), 64)
	a := &anlz.Analysis{}
	for i, m := range t.Tempos {
		start, _ := strconv.ParseFloat(m.Get("Inizio"), 64)
		bpm, _ := strconv.ParseFloat(m.Get("Bpm"), 64)
		bar, _ := strconv.Atoi(m.Get("Battito"))
		stop := end
		if i+1 < len(t.Tempos) {
			stop, _ = strconv.ParseFloat(t.Tempos[i+1].Get("Inizio"), 64)
		}
		if bpm <= 0 || bar < 1 {
			continue
		}
		for at := start; at < stop && len(a.Grid) < 20000; at += 60 / bpm {
			a.Grid = append(a.Grid, anlz.Beat{Ms: int(math.Round(at * 1000)), Bar: bar, BPM: bpm})
			bar = bar%4 + 1
		}
	}
	return a
}

func (s *xmlSource) Refresh() bool {
	s.mu.RLock()
	path, mod := s.lib.Path, s.mod
	s.mu.RUnlock()
	if st, err := os.Stat(path); err == nil && st.ModTime().After(mod) {
		return s.load(path) == nil
	}
	return false
}
func (s *xmlSource) Close() {}

func (s *xmlSource) Apply(c *Change) (*Applied, error) {
	s.mu.Lock()
	lib := s.lib
	res := &Applied{}
	var keys []string
	for _, it := range c.Items {
		id := it.TrackID
		if it.New != nil {
			n := it.New
			pt := rekordbox.PlaylistTrack{Path: n.Path, Name: n.Title, Artist: n.Artist, Album: n.Album,
				Duration: float64(n.Length), Bitrate: n.BitRate, Kind: rekordbox.Kind(strings.TrimPrefix(strings.ToLower(filepath.Ext(n.Path)), "."))}
			if n.Genre != "" {
				pt.Attrs = append(pt.Attrs, xml.Attr{Name: xml.Name{Local: "Genre"}, Value: n.Genre})
			}
			pt.Attrs = append(pt.Attrs, xml.Attr{Name: xml.Name{Local: "DateAdded"}, Value: time.Now().Format("2006-01-02")})
			id = lib.AddTrack(pt).ID
		}
		res.TrackIDs = append(res.TrackIDs, id)
		if id != "" {
			keys = append(keys, id)
		}
	}
	if c.Playlist != "" {
		parent := lib.Root
		if c.Folder != "" {
			parent = lib.Child(lib.Root, c.Folder, true)
		}
		pl := lib.Child(parent, c.Playlist, false)
		have := map[string]bool{}
		for _, k := range pl.Keys {
			have[k] = true
		}
		for _, k := range keys {
			if !have[k] {
				pl.Keys = append(pl.Keys, k)
				have[k] = true
			}
		}
		res.PlaylistID = pl.ID
	}
	err := lib.Save()
	path := lib.Path
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return res, s.load(path)
}

func (s *xmlSource) EditPlaylists(e PlaylistEdit) (*PlaylistEditResult, error) {
	s.mu.Lock()
	lib := s.lib
	res := &PlaylistEditResult{}
	backup := filepath.Join(DataDir(), "library backups", time.Now().Format("2006-01-02 15.04.05.000"))
	os.MkdirAll(backup, 0o755)
	if b, err := os.ReadFile(lib.Path); err == nil {
		os.WriteFile(filepath.Join(backup, filepath.Base(lib.Path)), b, 0o644)
		res.Backup = backup
	}
	var err error
	node := lib.Node(e.ID)
	switch e.Op {
	case "create":
		parent := lib.Root
		if e.Parent != "" && e.Parent != "root" {
			if parent = lib.Node(e.Parent); parent == nil || !parent.Folder {
				err = errors.New("that folder isn't in the library any more")
			}
		}
		if err == nil {
			res.ID = lib.Child(parent, e.Name, e.Folder).ID
		}
	case "rename", "add", "remove":
		if node == nil {
			err = errors.New("that playlist isn't in the library any more")
			break
		}
		switch e.Op {
		case "rename":
			node.Name = e.Name
		case "add":
			have := map[string]bool{}
			for _, k := range node.Keys {
				have[k] = true
			}
			for _, k := range e.TrackIDs {
				if !have[k] {
					node.Keys = append(node.Keys, k)
					have[k] = true
				}
			}
		case "remove":
			drop := map[string]bool{}
			for _, k := range e.TrackIDs {
				drop[k] = true
			}
			keys := node.Keys[:0]
			for _, k := range node.Keys {
				if !drop[k] {
					keys = append(keys, k)
				}
			}
			node.Keys = keys
		}
	case "delete":
		if !lib.Delete(e.ID) {
			err = errors.New("that playlist isn't in the library any more")
		}
		res.Deleted = []string{e.ID}
	default:
		err = fmt.Errorf("unknown playlist change %q", e.Op)
	}
	if err == nil {
		err = lib.Save()
	}
	path := lib.Path
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return res, s.load(path)
}

func (s *xmlSource) Relocate(moves map[string]string) error {
	s.mu.Lock()
	lib := s.lib
	for id, p := range moves {
		if t := lib.Track(id); t != nil {
			t.Attrs = rekordbox.SetAttr(t.Attrs, "Location", rekordbox.LocationFromPath(p))
		}
	}
	err := lib.Save()
	path := lib.Path
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.load(path)
}

func (s *xmlSource) Merge(ops []MergeOp) (string, error) {
	s.mu.Lock()
	lib := s.lib
	backup := filepath.Join(DataDir(), "library backups", time.Now().Format("2006-01-02 15.04.05"))
	os.MkdirAll(backup, 0o755)
	if b, err := os.ReadFile(lib.Path); err == nil {
		os.WriteFile(filepath.Join(backup, filepath.Base(lib.Path)), b, 0o644)
	}
	for _, op := range ops {
		keep, extra := lib.Track(op.Keep), lib.Track(op.Extra)
		if keep == nil || extra == nil {
			continue
		}
		var walk func(n *rekordbox.LibNode)
		walk = func(n *rekordbox.LibNode) {
			var out []string
			seen := map[string]bool{}
			for _, k := range n.Keys {
				if k == op.Extra {
					k = op.Keep
				}
				if !seen[k] {
					seen[k] = true
					out = append(out, k)
				}
			}
			n.Keys = out
			for _, c := range n.Children {
				walk(c)
			}
		}
		walk(lib.Root)
		if op.CueShift != nil && len(keep.Marks) == 0 {
			for _, m := range extra.Marks {
				attrs := append(m.Attrs[:0:0], m.Attrs...)
				ok := true
				for _, k := range []string{"Start", "End"} {
					if v, err := strconv.ParseFloat(rekordbox.Elem{Attrs: attrs}.Get(k), 64); err == nil {
						v += *op.CueShift
						if k == "Start" && v < -0.005 {
							ok = false
						}
						attrs = rekordbox.SetAttr(attrs, k, strconv.FormatFloat(math.Max(0, v), 'f', 3, 64))
					}
				}
				if ok {
					keep.Marks = append(keep.Marks, rekordbox.Elem{Attrs: attrs})
				}
			}
		}
		plays, _ := strconv.Atoi(keep.Get("PlayCount"))
		more, _ := strconv.Atoi(extra.Get("PlayCount"))
		keep.Attrs = rekordbox.SetAttr(keep.Attrs, "PlayCount", strconv.Itoa(plays+more))
		for _, k := range []string{"Rating", "Comments", "Colour"} {
			if keep.Get(k) == "" || keep.Get(k) == "0" {
				if v := extra.Get(k); v != "" {
					keep.Attrs = rekordbox.SetAttr(keep.Attrs, k, v)
				}
			}
		}
		for i, t := range lib.Tracks {
			if t == extra {
				lib.Tracks = append(lib.Tracks[:i], lib.Tracks[i+1:]...)
				break
			}
		}
	}
	err := lib.Save()
	path := lib.Path
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return backup, s.load(path)
}

func (s *xmlSource) Restore(backup string) error {
	s.mu.RLock()
	path := s.lib.Path
	s.mu.RUnlock()
	b, err := os.ReadFile(filepath.Join(backup, filepath.Base(path)))
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	return s.load(path)
}

// ---------------- opening ----------------

// openSource finds rekordbox's library (or an explicit path); failing that,
// it opens/creates SuperSync's own library next to the music.
func openSource(cfg *Config, dataDir string) (Source, error) {
	loc, err := rbdb.Find(cfg.RekordboxDB)
	if err == nil {
		s, err := openRekordbox(loc, filepath.Join(dataDir, "rekordbox backups"))
		if err != nil {
			return nil, fmt.Errorf("found rekordbox's library at %s but couldn't read it: %w", loc.DB, err)
		}
		return s, nil
	}
	if cfg.RekordboxDB != "" && !errors.Is(err, rbdb.ErrNotFound) {
		return nil, err
	}
	dir := cfg.MusicDir
	if dir == "" {
		dir = dataDir
	}
	return openXMLLibrary(filepath.Join(dir, "SuperSync Library.xml"))
}
