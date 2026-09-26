package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"supersync/internal/audio"
	"supersync/internal/rbdb"
	"supersync/internal/soundcloud"
	"supersync/internal/youtube"
)

// ---------------- persistent state ----------------

// State is SuperSync's own bookkeeping (state.json in the data folder):
// which library playlists came from SoundCloud, and changes waiting for
// rekordbox to close.
type State struct {
	mu          sync.Mutex
	SCPlaylists []*SCPlaylist `json:"scPlaylists"`
	Pending     []*Change     `json:"pending"`
	LastCleanup *LastCleanup  `json:"lastCleanup,omitempty"`
	LastSync    time.Time     `json:"lastSync,omitempty"`
}

// SCPlaylist links a SoundCloud playlist to a library playlist.
type SCPlaylist struct {
	URL        string     `json:"url"`
	Source     string     `json:"source,omitempty"` // "soundcloud" (default) or "youtube"
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
	Source     string     `json:"source,omitempty"`
}

type JobStep struct {
	N        int     `json:"n"`
	SCID     int64   `json:"scId,omitempty"`
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
	Source     string     `json:"source,omitempty"`
}

// Snapshot is a copy safe to serialize while the job runs.
func (j *Job) Snapshot() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	c := JobView{ID: j.ID, Title: j.Title, URL: j.URL, Status: j.Status, Message: j.Message, Started: j.Started, PlaylistID: j.PlaylistID, Source: j.Source}
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

// remote is a playlist on SoundCloud or YouTube, ready to import.
type remote struct {
	kind   string // "soundcloud" or "youtube"
	folder string // library folder for its playlists
	pl     *soundcloud.Playlist
	// canDownload and download act on pl.Tracks[i].
	canDownload func(i int) bool
	download    func(i int, dir string, progress soundcloud.Progress) (path string, original bool, err error)
}

func (a *App) fetchRemote(link string) (*remote, error) {
	if youtube.IsLink(link) {
		yt := youtube.New()
		ypl, err := yt.List(link)
		if err != nil {
			return nil, err
		}
		r := &remote{kind: "youtube", folder: "YouTube", pl: &soundcloud.Playlist{Title: ypl.Title, URL: link}}
		for _, e := range ypl.Entries {
			// The matcher and the playlist view speak SoundCloud tracks; a video fits.
			r.pl.Tracks = append(r.pl.Tracks, &soundcloud.Track{ID: videoID(e.ID), Title: e.Title,
				Uploader: e.Artist(), URL: e.URL(), DurationMS: int64(e.Duration * 1000)})
		}
		r.canDownload = func(int) bool { return true }
		r.download = func(i int, dir string, progress soundcloud.Progress) (string, bool, error) {
			p, err := yt.Download(ypl.Entries[i], dir, progress)
			return p, false, err
		}
		return r, nil
	}
	pl, err := a.SC.FetchPlaylist(link)
	if err != nil {
		return nil, err
	}
	return &remote{kind: "soundcloud", folder: "SoundCloud", pl: pl,
		canDownload: func(i int) bool { return pl.Tracks[i].CanDownload() },
		download: func(i int, dir string, progress soundcloud.Progress) (string, bool, error) {
			return a.SC.Download(pl.Tracks[i], dir, a.Cfg.SCToken, progress)
		}}, nil
}

// videoID turns a YouTube video ID into a stable number for bookkeeping
// (SoundCloud track IDs are numbers; these can't collide with them in practice).
func videoID(id string) int64 {
	h := fnv.New64a()
	h.Write([]byte(id))
	return int64(h.Sum64() >> 1)
}

// ImportPlaylist turns a SoundCloud or YouTube playlist into a library
// playlist in one go: tracks already in the library are linked, the rest are
// downloaded into the music folder and added, and anything that can't be
// downloaded is listed with its download/buy links. Running it again for the
// same link syncs: only what's new is downloaded and added.
func (a *App) ImportPlaylist(link string) (*Job, error) {
	if a.Src == nil {
		return nil, ErrNoSource
	}
	if a.Cfg.MusicDir == "" {
		return nil, errors.New("choose a download folder in Settings first")
	}
	link = strings.TrimSpace(link)
	if sp := a.scByURLLocked(link); sp != nil {
		link = sp.URL // same spelling as before, so the sync finds its playlist
	}
	a.mu.Lock()
	for _, j := range a.jobs {
		if j.URL == link && j.running() {
			a.mu.Unlock()
			return j, nil // already syncing: show that job
		}
	}
	j := &Job{ID: newID(), URL: link, Status: "fetching", Started: time.Now()}
	a.jobs[j.ID] = j
	a.mu.Unlock()
	go a.runImport(j, link)
	return j, nil
}

// Syncing lists the links being imported or synced right now.
func (a *App) Syncing() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, j := range a.jobs {
		if j.running() {
			out = append(out, j.URL)
		}
	}
	return out
}

func (j *Job) running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	switch j.Status {
	case "done", "error", "waiting":
		return false
	}
	return true
}

func (a *App) runImport(j *Job, link string) {
	fail := func(err error) { j.set(func() { j.Status, j.Message = "error", err.Error() }) }
	rm, err := a.fetchRemote(link)
	if err != nil {
		fail(err)
		return
	}
	pl := rm.pl
	j.set(func() {
		j.Title, j.Status, j.Source = pl.Title, "matching", rm.kind
		for i, t := range pl.Tracks {
			j.Steps = append(j.Steps, &JobStep{N: i + 1, SCID: t.ID, Title: t.Title, State: "queued"})
		}
	})
	lib, err := a.Library(nil)
	if err != nil {
		fail(err)
		return
	}
	res := a.Compare(pl, lib)
	scp := &SCPlaylist{URL: link, Source: rm.kind, Title: pl.Title, Owner: pl.Owner, ImportedAt: time.Now()}
	for _, t := range pl.Tracks {
		if t.Artwork != "" {
			scp.Artwork = t.Artwork
			break
		}
	}
	for i, row := range res.Rows {
		e := &SCEntry{SC: row.SC}
		step := j.Steps[i]
		switch {
		case row.SC.Unavailable:
			e.Status, e.Note = "unavailable", "Removed or made private on SoundCloud"
		case row.Status == Have && !exists(row.Match.Path):
			// The scan remembered a file that has since been moved or deleted.
			e.Status = "missing"
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
			step.State = map[string]string{"have": "have", "maybe": "maybe", "unavailable": "skipped"}[e.Status]
			step.Note = e.Note
			if step.State == "" {
				step.State = "queued"
			}
		})
		scp.Entries = append(scp.Entries, e)
	}

	// Remember the playlist now, and create it in the library straight away
	// with the tracks already there, so it can be opened (and played from)
	// while the rest download.
	a.State.mu.Lock()
	if old := a.scByURL(link); old != nil {
		scp.PlaylistID = old.PlaylistID // keep the link to the library playlist while syncing
		*old = *scp
		scp = old
	} else {
		a.State.SCPlaylists = append(a.State.SCPlaylists, scp)
	}
	a.State.save()
	a.State.mu.Unlock()

	change := func(entries []*SCEntry, interim bool) *Change {
		return scChange(rm.folder, pl.Title, link, entries, interim)
	}
	var mu sync.Mutex // guards scp.Entries' fields while downloads run
	snapshot := func() []*SCEntry {
		mu.Lock()
		defer mu.Unlock()
		out := make([]*SCEntry, len(scp.Entries))
		for i, e := range scp.Entries {
			c := *e
			out[i] = &c
		}
		return out
	}
	waiting := false
	apply := func(interim bool) error {
		err := a.applyChange(change(snapshot(), interim))
		if errors.Is(err, rbdb.ErrRunning) {
			waiting = true
			return nil
		}
		if err == nil {
			mu.Lock()
			if sp := a.scByURLLocked(link); sp != nil {
				j.set(func() { j.PlaylistID = sp.PlaylistID })
			}
			mu.Unlock()
		}
		return err
	}
	if err := apply(true); err != nil {
		fail(err)
		return
	}

	// Download the rest, three at a time, adding finished tracks to the
	// playlist every 20 seconds or so.
	j.set(func() { j.Status = "downloading" })
	dir := filepath.Join(a.Cfg.MusicDir, rm.folder, soundcloud.SafeFilename(pl.Title))
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	fresh := make(chan struct{}, 64)
	for i, e := range scp.Entries {
		step := j.Steps[i]
		if e.Status != "missing" {
			continue
		}
		if !rm.canDownload(i) {
			// Nothing to download: the same status as a download that turns
			// out to be impossible (e.g. copy-protected), below.
			note := noDownloadNote(e.SC)
			e.Status, e.Note = "unavailable", note
			j.set(func() { step.State, step.Note = "skipped", note })
			continue
		}
		wg.Add(1)
		go func(i int, e *SCEntry, step *JobStep) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			j.set(func() { step.State = "downloading" })
			var path string
			var original bool
			var err error
			for attempt := 0; attempt < 3; attempt++ {
				if attempt > 0 {
					j.set(func() { step.Note, step.Progress = "trying again", 0 })
					time.Sleep(time.Duration(attempt) * 3 * time.Second)
				}
				path, original, err = rm.download(i, dir, func(done, total int64) {
					j.set(func() {
						if total > 0 {
							step.Progress = float64(done) / float64(total)
						} else {
							step.Progress = -1
						}
					})
				})
				if err == nil || !soundcloud.Retryable(err) {
					break
				}
			}
			if err != nil {
				// Retrying won't help (copy-protected, Go+ only, gone): that's
				// "no download", like tracks that offer none. Otherwise it's a
				// failure the next sync tries again.
				state, status := "failed", "failed"
				if !soundcloud.Retryable(err) {
					state, status = "skipped", "unavailable"
				}
				j.set(func() { step.State, step.Note = state, err.Error() })
				mu.Lock()
				e.Status, e.Note = status, err.Error()
				mu.Unlock()
				return
			}
			mu.Lock()
			e.Status, e.File = "downloaded", path
			mu.Unlock()
			note := "stream copy"
			if original {
				note = "original file"
			}
			j.set(func() { step.State, step.Note, step.Progress = "downloaded", note, 1 })
			select {
			case fresh <- struct{}{}:
			default:
			}
		}(i, e, step)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	pending, last := 0, time.Now()
	for finished := false; !finished; {
		select {
		case <-fresh:
			pending++
		case <-done:
			finished = true
		case <-time.After(5 * time.Second):
		}
		if !finished && pending > 0 && !waiting && time.Since(last) > 20*time.Second {
			if err := apply(true); err != nil {
				log.Printf("adding downloaded tracks to %s: %v", pl.Title, err)
			}
			pending, last = 0, time.Now()
		}
	}

	// Everything, in SoundCloud's order.
	a.State.mu.Lock()
	a.State.save()
	a.State.mu.Unlock()
	j.set(func() { j.Status = "applying" })
	waiting = false
	if err := apply(false); err != nil {
		fail(err)
		return
	}
	if waiting {
		j.set(func() {
			j.Status, j.Message = "waiting", "Close rekordbox to add the playlist; SuperSync will finish as soon as you click Apply."
		})
		return
	}
	j.set(func() { j.Status = "done" })
}

// scChange is the library change for a synced playlist: every entry that's
// in the library or downloaded, in the playlist's order (unless interim).
func scChange(folder, title, link string, entries []*SCEntry, interim bool) *Change {
	ch := &Change{ID: newID(), Label: folder + ": " + title, Folder: folder, Playlist: title,
		SCURL: link, CreatedAt: time.Now(), Interim: interim, Ordered: !interim}
	for _, e := range entries {
		switch {
		case e.TrackID != "":
			ch.Items = append(ch.Items, ChangeItem{TrackID: e.TrackID, SCID: e.SC.ID})
		case e.File != "":
			in, err := audio.Read(e.File)
			if err != nil {
				continue
			}
			artist, title := e.SC.ArtistTitle()
			ch.Items = append(ch.Items, ChangeItem{New: newTrack(in, title, artist), SCID: e.SC.ID})
		}
	}
	return ch
}

func (a *App) scByURLLocked(u string) *SCPlaylist {
	a.State.mu.Lock()
	defer a.State.mu.Unlock()
	for _, p := range a.State.SCPlaylists {
		if strings.EqualFold(strings.TrimRight(p.URL, "/"), strings.TrimRight(u, "/")) {
			return p
		}
	}
	return nil
}

func noDownloadNote(t *soundcloud.Track) string {
	switch {
	case t.GoPlus && !t.Downloadable:
		return "SoundCloud Go+ only: without a subscription SoundCloud plays just a 30-second preview"
	case t.Protected && !t.Downloadable:
		return "SoundCloud only streams this copy-protected"
	case t.Downloadable && !t.DownloadsLeft:
		return "SoundCloud download limit reached"
	case len(t.Links) > 0 && (t.Links[0].Kind == soundcloud.FreeDownload || t.Links[0].Free):
		return "Free download via " + t.Links[0].Label
	case len(t.Links) > 0:
		return "Available on " + t.Links[0].Label
	}
	return "Not offered for download"
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

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
