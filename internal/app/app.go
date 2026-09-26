// Package app ties the pieces together; the CLI and the web UI both drive it.
package app

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"supersync/internal/trash"
	"sync"
	"time"

	"supersync/internal/analyze"
	"supersync/internal/library"
	"supersync/internal/rbdb"
	"supersync/internal/rekordbox"
	"supersync/internal/soundcloud"
	"supersync/internal/spectrum"
)

type Config struct {
	// MusicDir is where downloads go (and, without rekordbox, what gets added to the library).
	MusicDir string `json:"musicDir"`
	// RekordboxDB overrides where rekordbox's master.db is (normally found automatically).
	RekordboxDB string `json:"rekordboxDb,omitempty"`
	// SCToken is the user's own SoundCloud login token (optional): with it, tracks
	// that offer SoundCloud's download button download as the artist's original file.
	SCToken string `json:"scToken,omitempty"`
	MinKbps int    `json:"minKbps"`
	// AutoSyncHours re-syncs every SoundCloud/YouTube playlist this often (0 = off).
	AutoSyncHours int `json:"autoSyncHours"`
	// AutoApply writes waiting changes as soon as rekordbox has been closed.
	AutoApply bool `json:"autoApply"`
	// NoAutoUpdate turns off downloading new versions from GitHub in the background.
	NoAutoUpdate bool `json:"noAutoUpdate,omitempty"`
	// QuitOnClose quits when the window is closed, instead of carrying on in
	// the menu bar / notification area.
	QuitOnClose bool `json:"quitOnClose,omitempty"`
	// OpenAtLogin starts SuperSync (in the background) when you log in.
	OpenAtLogin bool `json:"openAtLogin,omitempty"`
	// CleanupAction is what happens to the extra files when duplicates are
	// cleaned up: "trash" (the default: Trash / Recycle Bin), "delete"
	// (permanently) or "folder" (kept in _SuperSync Duplicates).
	CleanupAction string `json:"cleanupAction,omitempty"`
	// KeepRemovedTracks keeps songs in a synced library playlist after they're
	// taken off the SoundCloud/YouTube playlist (by default they leave it).
	KeepRemovedTracks bool `json:"keepRemovedTracks,omitempty"`
	// StereoFixed: YouTube mp3s from before v0.1.29 have had their frame
	// headers repaired (see youtube.JointStereo).
	StereoFixed bool `json:"stereoFixed,omitempty"`
	// NotDuplicates: pairs of files the user said aren't duplicates.
	NotDuplicates []string `json:"notDuplicates,omitempty"`
	// Decisions records the user's answers for uncertain matches:
	// "sc:<track id>" -> absolute path of the owned file, or "none".
	Decisions map[string]string `json:"decisions,omitempty"`
}

func DataDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "SuperSync")
}

// CacheDir is for files that can be re-created (like saved SoundCloud streams).
func CacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(DataDir(), "cache")
	}
	return filepath.Join(dir, "SuperSync")
}

func configPath() string { return filepath.Join(DataDir(), "config.json") }

func LoadConfig() *Config {
	c := &Config{MinKbps: 320}
	if b, err := os.ReadFile(configPath()); err == nil {
		json.Unmarshal(b, c)
	}
	if c.MinKbps == 0 {
		c.MinKbps = 320
	}
	if c.Decisions == nil {
		c.Decisions = map[string]string{}
	}
	return c
}

func (c *Config) Save() error {
	if err := os.MkdirAll(DataDir(), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(configPath(), b, 0o600) // holds the SoundCloud token
}

// App holds the library source, the file scan of its tracks, and state.
// Its methods are safe for concurrent use.
type App struct {
	mu  sync.Mutex
	Cfg *Config
	Src Source
	// SrcErr is why the library couldn't be opened, if it couldn't.
	SrcErr string
	// Lib is the scan of the library's audio files (tags, quality, cutoffs).
	Lib *library.Library
	// Col is the library as rekordbox sees it, keyed by file path (cues, plays, playlists).
	Col rekordbox.Collection
	SC  *soundcloud.Client

	State    *State
	jobs     map[string]*Job
	cuePlans map[analyze.Pair]*analyze.CueTransfer // cleared on rescan
	better   map[string]*BetterCopy                // track ID -> a better copy found on disk
	// cloudFound: Cloud Library Sync track (stored path) -> its file found on
	// this computer ("" = looked, not found).
	cloudFound map[string]string
}

func New() *App {
	a := &App{Cfg: LoadConfig(), SC: soundcloud.New(), jobs: map[string]*Job{}}
	a.State = loadState()
	a.openSource()
	return a
}

func (a *App) openSource() {
	if a.Src != nil {
		a.Src.Close()
	}
	src, err := openSource(a.Cfg, DataDir())
	a.mu.Lock()
	a.Src, a.SrcErr = src, ""
	if err != nil {
		a.SrcErr = err.Error()
	}
	a.mu.Unlock()
	a.rebuild()
	if src != nil {
		a.Lib = library.LoadCached(a.scanKey())
	}
}

// rebuild derives the path-keyed collection view from the source.
func (a *App) rebuild() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cuePlans = nil
	if a.Src == nil {
		a.Col = nil
		return
	}
	if a.cloudUnsearchedLocked() {
		go a.resolveCloudFiles()
	}
	pls := a.Src.TrackPlaylists()
	col := rekordbox.Collection{}
	for _, t := range a.Src.Tracks() {
		col[rekordbox.NormPath(t.Path)] = &rekordbox.CollectionTrack{
			Path: t.Path, Name: t.Title, Artist: t.Artist, Cues: t.Cues, PlayCount: t.PlayCount,
			Playlists: pls[t.ID], Attrs: trackAttrs(t), ID: t.ID,
		}
	}
	a.Col = col
}

// trackAttrs renders a track's rekordbox info as XML attributes (used when
// carrying prep to another file through rekordbox XML).
func trackAttrs(t *rbdb.Track) []xml.Attr {
	var at []xml.Attr
	add := func(k, v string) {
		if v != "" && v != "0" {
			at = append(at, xml.Attr{Name: xml.Name{Local: k}, Value: v})
		}
	}
	add("Name", t.Title)
	add("Artist", t.Artist)
	add("Album", t.Album)
	add("Genre", t.Genre)
	add("Label", t.Label)
	add("Comments", t.Comment)
	add("Tonality", t.Key)
	if t.BPM > 0 {
		add("AverageBpm", strconv.FormatFloat(t.BPM, 'f', 2, 64))
	}
	add("Rating", strconv.Itoa(t.Rating*51))
	add("PlayCount", strconv.Itoa(t.PlayCount))
	add("DateAdded", t.Added)
	add("Year", strconv.Itoa(t.Year))
	return at
}

func (a *App) scanKey() string {
	if a.Src == nil {
		return ""
	}
	return a.Src.Info().Kind + ":" + a.Src.Info().Path
}

// Refresh picks up changes rekordbox made to its library since we loaded it.
func (a *App) Refresh() bool {
	if a.Src != nil && a.Src.Refresh() {
		a.rebuild()
		return true
	}
	return false
}

var ErrNoMusicDir = errors.New("no download folder set yet")
var ErrNoSource = errors.New("no library is open")

// SetMusicDir changes the download folder.
func (a *App) SetMusicDir(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return errors.New("that folder doesn't exist: " + abs)
	}
	a.mu.Lock()
	a.Cfg.MusicDir = abs
	kind := ""
	if a.Src != nil {
		kind = a.Src.Info().Kind
	}
	a.mu.Unlock()
	if err := a.Cfg.Save(); err != nil {
		return err
	}
	if kind != "rekordbox" {
		a.openSource() // SuperSync's own library lives in the music folder
	}
	return nil
}

// SetRekordboxDB points SuperSync at a specific master.db ("" = find automatically).
func (a *App) SetRekordboxDB(path string) error {
	if path != "" {
		if _, err := rbdb.Find(path); err != nil {
			return err
		}
	}
	a.Cfg.RekordboxDB = path
	if err := a.Cfg.Save(); err != nil {
		return err
	}
	a.openSource()
	if a.SrcErr != "" {
		return errors.New(a.SrcErr)
	}
	return nil
}

// Scan reads every audio file in the library (cached; only new or changed
// files are read again).
func (a *App) Scan(progress library.Progress) error {
	a.Refresh()
	if a.Src == nil {
		return ErrNoSource
	}
	var paths []string
	for _, t := range a.Src.Tracks() {
		if _, err := os.Stat(t.Path); err == nil {
			paths = append(paths, t.Path)
		}
	}
	lib, err := library.ScanFiles(a.scanKey(), paths, progress)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.Lib, a.cuePlans = lib, nil
	a.mu.Unlock()
	go a.FindBetterCopies()
	return nil
}

// Library returns the file scan, scanning first if there is none.
func (a *App) Library(progress library.Progress) (*library.Library, error) {
	a.mu.Lock()
	lib := a.Lib
	a.mu.Unlock()
	if lib != nil {
		return lib, nil
	}
	if err := a.Scan(progress); err != nil {
		return nil, err
	}
	return a.Lib, nil
}

// AddFolder adds every audio file in the music folder that isn't in the
// library yet (for SuperSync's own library; rekordbox users add music in rekordbox).
func (a *App) AddFolder(progress library.Progress) (int, error) {
	if a.Cfg.MusicDir == "" {
		return 0, ErrNoMusicDir
	}
	if a.Src == nil {
		return 0, ErrNoSource
	}
	scan, err := library.Scan(a.Cfg.MusicDir, progress)
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	for _, t := range a.Src.Tracks() {
		have[rekordbox.NormPath(t.Path)] = true
	}
	ch := &Change{Label: "Add music folder", CreatedAt: time.Now()}
	for _, t := range scan.Tracks {
		if !have[rekordbox.NormPath(t.Path)] {
			ch.Items = append(ch.Items, ChangeItem{New: newTrack(&t.Info, "", "")})
		}
	}
	if len(ch.Items) == 0 {
		return 0, nil
	}
	if _, err := a.Src.Apply(ch); err != nil {
		return 0, err
	}
	a.rebuild()
	return len(ch.Items), a.Scan(nil)
}

func (a *App) Duplicates() ([]*analyze.Group, error) {
	lib, err := a.Library(nil)
	if err != nil {
		return nil, err
	}
	return a.duplicates(lib), nil
}

func (a *App) Upgrades() ([]*analyze.Upgrade, error) {
	lib, err := a.Library(nil)
	if err != nil {
		return nil, err
	}
	return analyze.FindUpgrades(lib, a.duplicates(lib), a.Cfg.MinKbps), nil
}

func (a *App) quarantineDir() (string, error) {
	if a.Cfg.MusicDir == "" {
		return "", errors.New("set a download folder first; moved duplicates go into a folder inside it")
	}
	return analyze.QuarantineDir(a.Cfg.MusicDir), nil
}

// Quarantine moves files into the duplicates folder and rescans.
func (a *App) Quarantine(paths []string) ([]analyze.Move, error) {
	var moves []analyze.Move
	var err error
	switch a.Cfg.CleanupAction {
	case "delete":
		for _, p := range paths {
			if err = os.Remove(p); err != nil && !os.IsNotExist(err) {
				break
			}
			err = nil
			moves = append(moves, analyze.Move{Time: time.Now(), From: p}) // gone: nothing to undo
		}
	case "", "trash":
		for _, p := range paths {
			to, terr := trash.Move(p)
			if errors.Is(terr, trash.ErrUnsupported) {
				a.Cfg.CleanupAction = "folder" // no Trash here: keep them in the folder instead
				rest, qerr := a.quarantine(paths[len(moves):])
				moves, err = append(moves, rest...), qerr
				break
			}
			if terr != nil {
				err = fmt.Errorf("moving %s to the Trash: %w", filepath.Base(p), terr)
				break
			}
			moves = append(moves, analyze.Move{Time: time.Now(), From: p, To: to}) // To is "" in the Recycle Bin
		}
	default:
		moves, err = a.quarantine(paths)
	}
	if len(moves) > 0 {
		if serr := a.Scan(nil); err == nil {
			err = serr
		}
	}
	return moves, err
}

// quarantine moves files into the _SuperSync Duplicates folder.
func (a *App) quarantine(paths []string) ([]analyze.Move, error) {
	q, err := a.quarantineDir()
	if err != nil {
		return nil, err
	}
	root := ""
	if a.Lib != nil {
		root = a.Lib.Root
	}
	return analyze.Quarantine(q, root, paths)
}

// Undo restores every quarantined file and rescans.
func (a *App) Undo() (int, error) {
	q, err := a.quarantineDir()
	if err != nil {
		return 0, err
	}
	n, err := analyze.Undo(q)
	if n > 0 {
		if serr := a.Scan(nil); err == nil {
			err = serr
		}
	}
	return n, err
}

// Quarantined counts files currently moved aside.
func (a *App) Quarantined() int {
	q, err := a.quarantineDir()
	if err != nil {
		return 0
	}
	b, err := os.ReadFile(filepath.Join(q, "moves.jsonl"))
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}

// Decide records whether a SoundCloud track is owned (path) or not (path == "").
func (a *App) Decide(scID int64, path string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	k := decisionKey(scID)
	switch path {
	case "":
		a.Cfg.Decisions[k] = "none"
	case "-":
		delete(a.Cfg.Decisions, k)
	default:
		a.Cfg.Decisions[k] = path
	}
	return a.Cfg.Save()
}

// QualityCheckPending counts files that claim high quality but haven't had
// their spectrum checked.
func (a *App) QualityCheckPending() int {
	lib := a.Lib
	if lib == nil {
		return 0
	}
	n := 0
	for _, t := range lib.Tracks {
		if t.Cutoff == 0 && t.ClaimsHigh() {
			n++
		}
	}
	return n
}

// CheckQuality measures the spectrum cutoff of every high-quality-looking
// file not checked yet, to catch ones upscaled from a low-quality source.
func (a *App) CheckQuality(progress library.Progress) error {
	a.mu.Lock()
	lib := a.Lib
	a.mu.Unlock()
	if lib == nil {
		return ErrNoSource
	}
	var todo []*library.Track
	for _, t := range lib.Tracks {
		if t.Cutoff == 0 && t.ClaimsHigh() {
			todo = append(todo, t)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	results := make([]int, len(todo))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for w := 0; w < max(2, runtime.NumCPU()-1); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				c, err := spectrum.Analyze(todo[i].Path, todo[i].Duration)
				if err != nil {
					c = -2
				}
				results[i] = c
				mu.Lock()
				done++
				if progress != nil && (done%10 == 0 || done == len(todo)) {
					progress(done, len(todo))
				}
				mu.Unlock()
			}
		}()
	}
	for i := range todo {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	a.mu.Lock()
	for i, t := range todo {
		t.Cutoff = results[i]
	}
	a.cuePlans = nil
	a.mu.Unlock()
	return lib.Save()
}

// ---------------- cue transfer ----------------

// CuePairs lists the duplicate groups whose cue points sit on a copy that
// isn't the keeper. keep overrides the recommended keeper per group ID.
func (a *App) CuePairs(keep map[string]string) ([]analyze.Pair, error) {
	groups, err := a.Duplicates()
	if err != nil {
		return nil, err
	}
	return analyze.CuePairs(groups, keep), nil
}

// withMarks loads the source's cues for a file into its collection entry as
// rekordbox XML position marks.
func (a *App) withMarks(path string) {
	ct := a.Col.Lookup(path)
	if ct == nil || ct.Marks != nil || a.Src == nil {
		return
	}
	marks := []rekordbox.Elem{}
	for _, c := range a.Src.Cues(ct.ID) {
		kv := [][2]string{{"Name", c.Comment}, {"Type", "0"}, {"Start", fmtMs(c.InMs)}, {"Num", strconv.Itoa(c.Hot - 1)}}
		if c.OutMs > c.InMs {
			kv[1][1] = "4"
			kv = append(kv, [2]string{"End", fmtMs(c.OutMs)})
		}
		var e rekordbox.Elem
		for _, p := range kv {
			e.Attrs = rekordbox.SetAttr(e.Attrs, p[0], p[1])
		}
		marks = append(marks, e)
	}
	ct.Marks = marks
}

func fmtMs(ms int) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64) }

// PlanCues lines up each pair's audio (cached until the next rescan).
func (a *App) PlanCues(pairs []analyze.Pair) ([]*analyze.CueTransfer, error) {
	lib, err := a.Library(nil)
	if err != nil {
		return nil, err
	}
	for _, p := range pairs {
		a.withMarks(p.From)
	}
	a.mu.Lock()
	if a.cuePlans == nil {
		a.cuePlans = map[analyze.Pair]*analyze.CueTransfer{}
	}
	var todo []analyze.Pair
	for _, p := range pairs {
		if a.cuePlans[p] == nil {
			todo = append(todo, p)
		}
	}
	a.mu.Unlock()
	planned, err := analyze.PlanCues(lib, a.Col, todo)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, t := range planned {
		a.cuePlans[t.Pair] = t
	}
	out := make([]*analyze.CueTransfer, len(pairs))
	for i, p := range pairs {
		out[i] = a.cuePlans[p]
	}
	return out, nil
}

// CueXML writes a rekordbox XML giving each pair's To file the From file's
// cues, loops and track info.
func (a *App) CueXML(w io.Writer, pairs []analyze.Pair) (int, error) {
	plans, err := a.PlanCues(pairs)
	if err != nil {
		return 0, err
	}
	pl := rekordbox.Playlist{Name: "Cues carried over " + time.Now().Format("2006-01-02 15.04")}
	for _, t := range plans {
		if t.Usable() {
			pl.Tracks = append(pl.Tracks, analyze.CueTrack(t, a.Lib, a.Col))
		}
	}
	if len(pl.Tracks) == 0 {
		return 0, errors.New("none of these could be lined up, so there's nothing to import")
	}
	return len(pl.Tracks), rekordbox.WritePlaylists(w, "SuperSync", []rekordbox.Playlist{pl})
}

// TrackByPath maps a file to its library track.
func (a *App) TrackByPath(path string) *rbdb.Track {
	if ct := a.Col.Lookup(path); ct != nil && a.Src != nil {
		return a.Src.Track(ct.ID)
	}
	return nil
}
