package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"supersync/internal/audio"
	"supersync/internal/match"
	"supersync/internal/rbdb"
	"supersync/internal/soundcloud"
)

// ---------------- persistent state ----------------

// State is SuperSync's own bookkeeping (state.json in the data folder):
// which library playlists came from SoundCloud, and changes waiting for
// rekordbox to close.
type State struct {
	mu          sync.Mutex
	SCPlaylists []*SCPlaylist `json:"scPlaylists"`
	Pending     []*Change     `json:"pending"`
}

// SCPlaylist links a SoundCloud playlist to a library playlist.
type SCPlaylist struct {
	URL        string     `json:"url"`
	Title      string     `json:"title"`
	Owner      string     `json:"owner"`
	Artwork    string     `json:"artwork,omitempty"`
	PlaylistID string     `json:"playlistId,omitempty"` // library playlist, once applied
	Pending    bool       `json:"pending,omitempty"`    // waiting for rekordbox to close
	Entries    []*SCEntry `json:"entries"`
	ImportedAt time.Time  `json:"importedAt"`
}

// SCEntry is one SoundCloud track and where it stands in the library.
type SCEntry struct {
	SC      *soundcloud.Track `json:"sc"`
	TrackID string            `json:"trackId,omitempty"`
	File    string            `json:"file,omitempty"` // downloaded by SuperSync
	Status  string            `json:"status"`         // have, downloaded, maybe, missing, failed
	Note    string            `json:"note,omitempty"`
	Maybe   string            `json:"maybe,omitempty"` // path of a similar file
}

func statePath() string { return filepath.Join(DataDir(), "state.json") }

func loadState() *State {
	s := &State{}
	if b, err := os.ReadFile(statePath()); err == nil {
		json.Unmarshal(b, s)
	}
	return s
}

func (s *State) save() error {
	os.MkdirAll(DataDir(), 0o755)
	b, _ := json.MarshalIndent(s, "", " ")
	tmp := statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, statePath())
}

// SCFor returns the SoundCloud link for a library playlist, if any.
func (a *App) SCFor(playlistID string) *SCPlaylist {
	a.State.mu.Lock()
	defer a.State.mu.Unlock()
	for _, p := range a.State.SCPlaylists {
		if p.PlaylistID == playlistID && playlistID != "" {
			return p
		}
	}
	return nil
}

// SCPlaylists lists everything imported from SoundCloud.
func (a *App) SCPlaylists() []*SCPlaylist {
	a.State.mu.Lock()
	defer a.State.mu.Unlock()
	return append([]*SCPlaylist(nil), a.State.SCPlaylists...)
}

func (a *App) scByURL(u string) *SCPlaylist {
	for _, p := range a.State.SCPlaylists {
		if p.URL == u {
			return p
		}
	}
	return nil
}

// ---------------- jobs ----------------

// Job is a long-running import, reported to the UI as it goes.
type Job struct {
	mu         sync.Mutex
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	URL        string     `json:"url"`
	Status     string     `json:"status"` // fetching, matching, downloading, applying, waiting, done, error
	Message    string     `json:"message,omitempty"`
	Steps      []*JobStep `json:"steps"`
	Started    time.Time  `json:"started"`
	PlaylistID string     `json:"playlistId,omitempty"`
}

type JobStep struct {
	N        int     `json:"n"`
	Title    string  `json:"title"`
	State    string  `json:"state"` // queued, have, maybe, downloading, downloaded, skipped, failed
	Progress float64 `json:"progress"`
	Note     string  `json:"note,omitempty"`
}

func (j *Job) set(f func()) { j.mu.Lock(); f(); j.mu.Unlock() }

// JobView is a copy of a job safe to serialize while it runs.
type JobView struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	URL        string     `json:"url"`
	Status     string     `json:"status"`
	Message    string     `json:"message,omitempty"`
	Steps      []*JobStep `json:"steps"`
	Started    time.Time  `json:"started"`
	PlaylistID string     `json:"playlistId,omitempty"`
}

// Snapshot is a copy safe to serialize while the job runs.
func (j *Job) Snapshot() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	c := JobView{ID: j.ID, Title: j.Title, URL: j.URL, Status: j.Status, Message: j.Message, Started: j.Started, PlaylistID: j.PlaylistID}
	for _, s := range j.Steps {
		cp := *s
		c.Steps = append(c.Steps, &cp)
	}
	return c
}

func (a *App) Job(id string) *Job {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.jobs[id]
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------- import ----------------

// ImportSoundCloud turns a SoundCloud playlist into a library playlist in
// one go: tracks already in the library are linked, tracks whose artist
// enabled SoundCloud's download button are downloaded into the music
// folder and added, and the rest are listed with their download/buy links.
func (a *App) ImportSoundCloud(link string) (*Job, error) {
	if a.Src == nil {
		return nil, ErrNoSource
	}
	if a.Cfg.MusicDir == "" {
		return nil, errors.New("choose a download folder in Settings first")
	}
	j := &Job{ID: newID(), URL: link, Status: "fetching", Started: time.Now()}
	a.mu.Lock()
	a.jobs[j.ID] = j
	a.mu.Unlock()
	go a.runImport(j, link)
	return j, nil
}

func (a *App) runImport(j *Job, link string) {
	fail := func(err error) { j.set(func() { j.Status, j.Message = "error", err.Error() }) }
	pl, err := a.SC.FetchPlaylist(link)
	if err != nil {
		fail(err)
		return
	}
	j.set(func() {
		j.Title, j.Status = pl.Title, "matching"
		for i, t := range pl.Tracks {
			j.Steps = append(j.Steps, &JobStep{N: i + 1, Title: t.Title, State: "queued"})
		}
	})
	lib, err := a.Library(nil)
	if err != nil {
		fail(err)
		return
	}
	res := a.Compare(pl, lib)
	scp := &SCPlaylist{URL: link, Title: pl.Title, Owner: pl.Owner, ImportedAt: time.Now()}
	if len(pl.Tracks) > 0 {
		scp.Artwork = pl.Tracks[0].Artwork
		for _, t := range pl.Tracks {
			if t.Artwork != "" {
				scp.Artwork = t.Artwork
				break
			}
		}
	}
	for i, row := range res.Rows {
		e := &SCEntry{SC: row.SC}
		step := j.Steps[i]
		switch {
		case row.SC.Unavailable:
			e.Status, e.Note = "missing", "Unavailable on SoundCloud"
		case row.Status == Have:
			if tr := a.TrackByPath(row.Match.Path); tr != nil {
				e.Status, e.TrackID = "have", tr.ID
			} else {
				e.Status, e.File = "have", row.Match.Path // on disk but not in the library yet
			}
		case row.Status == Maybe:
			e.Status = "maybe"
			if len(row.Others) > 0 {
				e.Maybe = row.Others[0].Path
			}
		default:
			e.Status = "missing"
		}
		j.set(func() {
			step.State = map[string]string{"have": "have", "maybe": "maybe"}[e.Status]
			if step.State == "" {
				step.State = "queued"
			}
		})
		scp.Entries = append(scp.Entries, e)
	}

	// Download what the artists allow, three at a time.
	j.set(func() { j.Status = "downloading" })
	dir := filepath.Join(a.Cfg.MusicDir, "SoundCloud", soundcloud.SafeFilename(pl.Title))
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for i, e := range scp.Entries {
		step := j.Steps[i]
		if e.Status != "missing" || e.SC.Unavailable {
			continue
		}
		if !e.SC.CanDownload() {
			j.set(func() { step.State, step.Note = "skipped", noDownloadNote(e.SC) })
			continue
		}
		wg.Add(1)
		go func(e *SCEntry, step *JobStep) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			j.set(func() { step.State = "downloading" })
			path, original, err := a.SC.Download(e.SC, dir, a.Cfg.SCToken, func(done, total int64) {
				j.set(func() {
					if total > 0 {
						step.Progress = float64(done) / float64(total)
					} else {
						step.Progress = -1
					}
				})
			})
			if err != nil {
				j.set(func() { step.State, step.Note = "failed", err.Error() })
				e.Status, e.Note = "failed", err.Error()
				return
			}
			e.Status, e.File = "downloaded", path
			note := "MP3 stream"
			if original {
				note = "original file"
			}
			j.set(func() { step.State, step.Note, step.Progress = "downloaded", note, 1 })
		}(e, step)
	}
	wg.Wait()

	// One change: the playlist in a "SoundCloud" folder with everything we have.
	ch := &Change{ID: newID(), Label: "SoundCloud: " + pl.Title, Folder: "SoundCloud", Playlist: pl.Title, SCURL: link, CreatedAt: time.Now()}
	for _, e := range scp.Entries {
		switch {
		case e.TrackID != "":
			ch.Items = append(ch.Items, ChangeItem{TrackID: e.TrackID, SCID: e.SC.ID})
		case e.File != "":
			in, err := audio.Read(e.File)
			if err != nil {
				continue
			}
			ch.Items = append(ch.Items, ChangeItem{New: newTrack(in, e.SC.Title, scArtist(e.SC)), SCID: e.SC.ID})
		}
	}
	a.State.mu.Lock()
	if old := a.scByURL(link); old != nil {
		*old = *scp
		scp = old
	} else {
		a.State.SCPlaylists = append(a.State.SCPlaylists, scp)
	}
	a.State.save()
	a.State.mu.Unlock()

	j.set(func() { j.Status = "applying" })
	if err := a.applyChange(ch); errors.Is(err, rbdb.ErrRunning) {
		j.set(func() {
			j.Status, j.Message = "waiting", "Close rekordbox to add the playlist; SuperSync will finish as soon as you click Apply."
		})
		return
	} else if err != nil {
		fail(err)
		return
	}
	j.set(func() { j.Status, j.PlaylistID = "done", scp.PlaylistID })
}

func noDownloadNote(t *soundcloud.Track) string {
	switch {
	case t.Downloadable && !t.DownloadsLeft:
		return "SoundCloud download limit reached"
	case len(t.Links) > 0 && (t.Links[0].Kind == soundcloud.FreeDownload || t.Links[0].Free):
		return "Free download via " + t.Links[0].Label
	case len(t.Links) > 0:
		return "Available on " + t.Links[0].Label
	}
	return "Not offered for download"
}

// scArtist picks the artist for a SoundCloud track: from "Artist - Title",
// else the publisher's artist field, else the uploader.
func scArtist(t *soundcloud.Track) string {
	if keys := match.Parse("", t.Title); len(keys) > 0 && len(keys[0].Artist) > 0 {
		if a, _, ok := strings.Cut(t.Title, " - "); ok {
			return strings.TrimSpace(a)
		}
	}
	if t.Artist != "" {
		return t.Artist
	}
	return t.Uploader
}

// newTrack describes a file for the library, preferring its own tags.
func newTrack(in *audio.Info, title, artist string) *rbdb.NewTrack {
	n := &rbdb.NewTrack{Path: in.Path, Title: in.Title, Artist: in.Artist, Album: in.Album, Comment: in.Comment,
		Length: int(in.Duration + 0.5), BitRate: in.Bitrate, SampleRate: in.SampleRate, BitDepth: in.BitDepth}
	if n.Title == "" {
		n.Title = title
		if _, t, ok := strings.Cut(title, " - "); ok && artist != "" {
			n.Title = strings.TrimSpace(t)
		}
	}
	if n.Artist == "" {
		n.Artist = artist
	}
	return n
}

// applyChange writes a change to the library, or parks it until rekordbox closes.
func (a *App) applyChange(ch *Change) error {
	res, err := a.Src.Apply(ch)
	if errors.Is(err, rbdb.ErrRunning) {
		a.State.mu.Lock()
		kept := a.State.Pending[:0]
		for _, p := range a.State.Pending {
			if ch.SCURL == "" || p.SCURL != ch.SCURL {
				kept = append(kept, p)
			}
		}
		a.State.Pending = append(kept, ch)
		if sp := a.scByURL(ch.SCURL); sp != nil {
			sp.Pending = true
		}
		a.State.save()
		a.State.mu.Unlock()
		return err
	}
	if err != nil {
		return err
	}
	a.afterApply(ch, res)
	return nil
}

func (a *App) afterApply(ch *Change, res *Applied) {
	a.State.mu.Lock()
	if sp := a.scByURL(ch.SCURL); sp != nil {
		sp.PlaylistID, sp.Pending = res.PlaylistID, false
		ids := map[int64]string{}
		for i, it := range ch.Items {
			if it.SCID != 0 && i < len(res.TrackIDs) {
				ids[it.SCID] = res.TrackIDs[i]
			}
		}
		for _, e := range sp.Entries {
			if id := ids[e.SC.ID]; id != "" {
				e.TrackID = id
			}
		}
	}
	a.State.save()
	a.State.mu.Unlock()
	a.rebuild()
	go a.Scan(nil) // pick up new files' tags and quality
}

// PendingChanges lists changes waiting for rekordbox to close.
func (a *App) PendingChanges() []*Change {
	a.State.mu.Lock()
	defer a.State.mu.Unlock()
	return append([]*Change(nil), a.State.Pending...)
}

// ApplyPending writes every parked change (rekordbox must be closed).
func (a *App) ApplyPending() (int, error) {
	a.State.mu.Lock()
	pending := append([]*Change(nil), a.State.Pending...)
	a.State.mu.Unlock()
	n := 0
	for _, ch := range pending {
		res, err := a.Src.Apply(ch)
		if err != nil {
			return n, err
		}
		a.State.mu.Lock()
		for i, p := range a.State.Pending {
			if p == ch {
				a.State.Pending = append(a.State.Pending[:i], a.State.Pending[i+1:]...)
				break
			}
		}
		a.State.mu.Unlock()
		a.afterApply(ch, res)
		n++
		a.mu.Lock()
		for _, j := range a.jobs {
			j.set(func() {
				if j.Status == "waiting" && j.URL == ch.SCURL {
					j.Status = "done"
					if sp := a.scByURL(ch.SCURL); sp != nil {
						j.PlaylistID = sp.PlaylistID
					}
				}
			})
		}
		a.mu.Unlock()
	}
	return n, nil
}

// DiscardPending drops parked changes (downloaded files stay on disk).
func (a *App) DiscardPending() {
	a.State.mu.Lock()
	defer a.State.mu.Unlock()
	for _, ch := range a.State.Pending {
		if sp := a.scByURL(ch.SCURL); sp != nil && sp.PlaylistID == "" {
			sp.Pending = false
		}
	}
	a.State.Pending = nil
	a.State.save()
}

// ---------------- quality tiers ----------------

// Tier grades a track: low (<128 kbps), normal (128), hq (256), uhq (>256 or lossless).
// Files revealed as upscaled are graded by what they really are.
func (a *App) Tier(t *rbdb.Track) (tier string, kbps int, note string) {
	kbps, lossless := t.BitRate, t.FileType == 5 || t.FileType == 11 || t.FileType == 12
	if a.Lib != nil {
		if lt := a.Lib.ByPath(t.Path); lt != nil {
			kbps, lossless = lt.Bitrate, lt.Lossless
			if k := lt.TrueKbps(); k > 0 {
				kbps, lossless, note = k, false, "Upscaled: "+lt.QualityLabel()+", "+lt.Upscaled()
			}
		}
	}
	switch {
	case lossless:
		return "uhq", kbps, note
	case kbps >= 280:
		return "uhq", kbps, note
	case kbps >= 240:
		return "hq", kbps, note
	case kbps >= 120:
		return "normal", kbps, note
	case kbps > 0:
		return "low", kbps, note
	}
	return "", 0, note
}
