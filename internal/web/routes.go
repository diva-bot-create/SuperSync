package web

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"supersync/internal/analyze"
	"supersync/internal/anlz"
	"supersync/internal/app"
	"supersync/internal/audio"
	"supersync/internal/rbdb"
	"supersync/internal/soundcloud"
)

func (s *server) routes(mux *http.ServeMux) {
	a := s.app
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) { reply(w, s.state(), nil) })

	mux.HandleFunc("POST /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MusicDir    *string `json:"musicDir"`
			RekordboxDB *string `json:"rekordboxDb"`
			SCToken     *string `json:"scToken"`
			MinKbps     *int    `json:"minKbps"`
		}
		if !decode(w, r, &req) {
			return
		}
		var err error
		if req.MusicDir != nil {
			err = a.SetMusicDir(*req.MusicDir)
		}
		if err == nil && req.RekordboxDB != nil {
			err = a.SetRekordboxDB(*req.RekordboxDB)
			if err == nil {
				s.startScan()
			}
		}
		if err == nil && req.SCToken != nil {
			a.Cfg.SCToken = strings.TrimSpace(*req.SCToken)
			err = a.Cfg.Save()
		}
		if err == nil && req.MinKbps != nil && *req.MinKbps > 0 {
			a.Cfg.MinKbps = *req.MinKbps
			err = a.Cfg.Save()
		}
		reply(w, s.state(), err)
	})

	mux.HandleFunc("POST /api/scan", func(w http.ResponseWriter, r *http.Request) {
		s.startScan()
		reply(w, s.state(), nil)
	})

	// ---- library browsing ----
	mux.HandleFunc("GET /api/library", func(w http.ResponseWriter, r *http.Request) {
		if a.Src == nil {
			reply(w, nil, errors.New(orStr(a.SrcErr, "no library is open")))
			return
		}
		a.Refresh()
		sc := map[string]*app.SCPlaylist{}
		var pendingSC []map[string]any
		for _, p := range a.SCPlaylists() {
			if p.PlaylistID != "" {
				sc[p.PlaylistID] = p
			} else if p.Pending {
				pendingSC = append(pendingSC, map[string]any{"url": p.URL, "title": p.Title, "count": len(p.Entries), "source": source(p)})
			}
		}
		type node struct {
			*rbdb.Playlist
			SC       string  `json:"sc,omitempty"`     // SoundCloud/YouTube link it syncs from
			Source   string  `json:"source,omitempty"` // "soundcloud" or "youtube"
			Children []*node `json:"children,omitempty"`
		}
		var conv func(ps []*rbdb.Playlist) []*node
		conv = func(ps []*rbdb.Playlist) []*node {
			var out []*node
			for _, p := range ps {
				n := &node{Playlist: p, Children: conv(p.Children)}
				if l := sc[p.ID]; l != nil {
					n.SC, n.Source = l.URL, source(l)
				}
				out = append(out, n)
			}
			return out
		}
		reply(w, map[string]any{"source": a.Src.Info(), "playlists": conv(a.Src.Playlists()), "pendingSC": pendingSC}, nil)
	})

	mux.HandleFunc("GET /api/library/tracks", func(w http.ResponseWriter, r *http.Request) {
		if a.Src == nil {
			reply(w, nil, errors.New("no library is open"))
			return
		}
		reply(w, s.trackList(r.URL.Query().Get("playlist")), nil)
	})

	mux.HandleFunc("GET /api/track/{id}", func(w http.ResponseWriter, r *http.Request) {
		t := s.track(r.PathValue("id"))
		if t == nil {
			reply(w, nil, errors.New("no such track"))
			return
		}
		row := s.row(t)
		// The beatgrid as [ms, beat-in-bar, BPM×100] triples (compact for long tracks).
		grid := [][3]int{}
		if an := a.Src.Analysis(t.ID); an != nil {
			for _, b := range an.Grid {
				grid = append(grid, [3]int{b.Ms, b.Bar, int(b.BPM*100 + 0.5)})
			}
		}
		reply(w, map[string]any{"track": row, "cues": a.Src.Cues(t.ID), "grid": grid, "playlists": a.Src.TrackPlaylists()[t.ID]}, nil)
	})

	mux.HandleFunc("GET /api/audio/{id}", func(w http.ResponseWriter, r *http.Request) {
		t := s.track(r.PathValue("id"))
		if t == nil {
			http.NotFound(w, r)
			return
		}
		serveAudio(w, r, t.Path)
	})

	mux.HandleFunc("GET /api/waveform/{id}", func(w http.ResponseWriter, r *http.Request) {
		t := s.track(r.PathValue("id"))
		if t == nil {
			reply(w, nil, errors.New("no such track"))
			return
		}
		wf, err := audio.ComputeWaveform(t.Path, 900, filepath.Join(app.DataDir(), "waveforms"))
		if err != nil {
			reply(w, nil, err)
			return
		}
		out := struct {
			*audio.Waveform
			DRate  int    `json:"drate"`
			RB     []byte `json:"rb,omitempty"` // rekordbox's colour waveform, 2 bytes per column (big-endian)
			RBRate int    `json:"rbRate,omitempty"`
		}{Waveform: wf, DRate: audio.DetailRate}
		if an := a.Src.Analysis(t.ID); an != nil && len(an.Detail) > 0 {
			out.RB = make([]byte, 2*len(an.Detail))
			for i, v := range an.Detail {
				out.RB[2*i], out.RB[2*i+1] = byte(v>>8), byte(v)
			}
			out.RBRate = anlz.DetailRate
		}
		w.Header().Set("Cache-Control", "private, max-age=3600")
		reply(w, out, nil)
	})

	// ---- SoundCloud import ----
	mux.HandleFunc("POST /api/sc/import", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL string `json:"url"`
		}
		if !decode(w, r, &req) {
			return
		}
		if s.busy() {
			reply(w, nil, errors.New("still scanning your library — try again in a moment"))
			return
		}
		j, err := a.ImportPlaylist(req.URL)
		if err != nil {
			reply(w, nil, err)
			return
		}
		reply(w, j.Snapshot(), nil)
	})
	mux.HandleFunc("GET /api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		j := a.Job(r.PathValue("id"))
		if j == nil {
			reply(w, nil, errors.New("no such job"))
			return
		}
		reply(w, j.Snapshot(), nil)
	})
	mux.HandleFunc("POST /api/pending/apply", func(w http.ResponseWriter, r *http.Request) {
		n, err := a.ApplyPending()
		reply(w, map[string]int{"applied": n}, err)
	})
	mux.HandleFunc("POST /api/pending/discard", func(w http.ResponseWriter, r *http.Request) {
		a.DiscardPending()
		reply(w, map[string]bool{"ok": true}, nil)
	})
	mux.HandleFunc("POST /api/addfolder", func(w http.ResponseWriter, r *http.Request) {
		n, err := a.AddFolder(nil)
		reply(w, map[string]int{"added": n}, err)
	})

	// ---- duplicates / upgrades / cues (unchanged behaviour) ----
	mux.HandleFunc("POST /api/check", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL string `json:"url"`
		}
		if !decode(w, r, &req) {
			return
		}
		if s.busy() {
			reply(w, nil, errors.New("still scanning your library — try again in a moment"))
			return
		}
		res, err := a.Check(req.URL, nil)
		reply(w, res, err)
	})
	mux.HandleFunc("GET /api/dupes", func(w http.ResponseWriter, r *http.Request) {
		if s.busy() {
			reply(w, nil, errors.New("still scanning"))
			return
		}
		g, err := a.Duplicates()
		if g == nil {
			g = []*analyze.Group{}
		}
		reply(w, g, err)
	})
	mux.HandleFunc("GET /api/upgrades", func(w http.ResponseWriter, r *http.Request) {
		if s.busy() {
			reply(w, nil, errors.New("still scanning"))
			return
		}
		u, err := a.Upgrades()
		if u == nil {
			u = []*analyze.Upgrade{}
		}
		reply(w, u, err)
	})
	mux.HandleFunc("POST /api/quarantine", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Paths []string `json:"paths"`
		}
		if !decode(w, r, &req) {
			return
		}
		lib := a.Lib
		if lib == nil {
			reply(w, nil, app.ErrNoSource)
			return
		}
		for _, p := range req.Paths {
			if lib.ByPath(p) == nil {
				reply(w, nil, fmt.Errorf("not a file in your library: %s", p))
				return
			}
		}
		moves, err := a.Quarantine(req.Paths)
		reply(w, map[string]int{"moved": len(moves)}, err)
	})
	mux.HandleFunc("POST /api/undo", func(w http.ResponseWriter, r *http.Request) {
		n, err := a.Undo()
		reply(w, map[string]int{"restored": n}, err)
	})
	mux.HandleFunc("POST /api/cues/plan", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Pairs []analyze.Pair `json:"pairs"`
		}
		if !decode(w, r, &req) {
			return
		}
		if s.busy() {
			reply(w, nil, errors.New("still scanning"))
			return
		}
		plans, err := a.PlanCues(req.Pairs)
		reply(w, plans, err)
	})
	mux.HandleFunc("POST /api/cues/xml", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Pairs []analyze.Pair `json:"pairs"`
		}
		if !decode(w, r, &req) {
			return
		}
		var buf bytes.Buffer
		if _, err := a.CueXML(&buf, req.Pairs); err != nil {
			reply(w, nil, err)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.Header().Set("Content-Disposition", disposition("SuperSync cues "+time.Now().Format("2006-01-02")+".xml"))
		w.Write(buf.Bytes())
	})
	mux.HandleFunc("POST /api/reveal", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
			ID   string `json:"id"`
		}
		if !decode(w, r, &req) {
			return
		}
		if req.ID != "" {
			if t := s.track(req.ID); t != nil {
				req.Path = t.Path
			}
		}
		ok := a.Col.Lookup(req.Path) != nil || (a.Lib != nil && a.Lib.ByPath(req.Path) != nil)
		if !ok {
			reply(w, nil, errors.New("not a file in your library"))
			return
		}
		reply(w, map[string]bool{"ok": true}, reveal(req.Path))
	})
	mux.HandleFunc("GET /api/browse", func(w http.ResponseWriter, r *http.Request) {
		l, err := browse(r.URL.Query().Get("dir"), r.URL.Query().Get("ext"))
		reply(w, l, err)
	})
}

func source(p *app.SCPlaylist) string {
	if p.Source == "" {
		return "soundcloud"
	}
	return p.Source
}

func orStr(s, d string) string {
	if s != "" {
		return s
	}
	return d
}

func (s *server) track(id string) *rbdb.Track {
	if s.app.Src == nil {
		return nil
	}
	return s.app.Src.Track(id)
}

// Row is a track as the library table shows it.
type Row struct {
	ID       string  `json:"id,omitempty"`
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	Album    string  `json:"album,omitempty"`
	Genre    string  `json:"genre,omitempty"`
	Key      string  `json:"key,omitempty"`
	BPM      float64 `json:"bpm,omitempty"`
	Length   int     `json:"length,omitempty"`
	Kbps     int     `json:"kbps,omitempty"`
	Format   string  `json:"format,omitempty"`
	Tier     string  `json:"tier,omitempty"`
	TierNote string  `json:"tierNote,omitempty"`
	Rating   int     `json:"rating,omitempty"`
	Color    string  `json:"color,omitempty"`
	Cues     int     `json:"cues,omitempty"`
	Added    string  `json:"added,omitempty"`
	Path     string  `json:"path,omitempty"`
	Missing  bool    `json:"missing,omitempty"` // file not on disk

	// Rows of SoundCloud-imported playlists.
	SC     *soundcloud.Track `json:"sc,omitempty"`
	Status string            `json:"status,omitempty"` // have, downloaded, maybe, missing, failed
	Note   string            `json:"note,omitempty"`
}

func (s *server) row(t *rbdb.Track) *Row {
	tier, kbps, note := s.app.Tier(t)
	r := &Row{ID: t.ID, Title: t.Title, Artist: t.Artist, Album: t.Album, Genre: t.Genre, Key: t.Key, BPM: t.BPM,
		Length: t.Length, Kbps: kbps, Format: rbdb.FileTypes[t.FileType], Tier: tier, TierNote: note,
		Rating: t.Rating, Color: t.Color, Cues: t.Cues, Added: t.Added, Path: t.Path}
	if r.Title == "" {
		r.Title = strings.TrimSuffix(filepath.Base(t.Path), filepath.Ext(t.Path))
	}
	if _, err := os.Stat(t.Path); err != nil {
		r.Missing = true
	}
	return r
}

type trackList struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	SC    *app.SCPlaylist `json:"sc,omitempty"`
	Rows  []*Row          `json:"rows"`
	Tiers map[string]int  `json:"tiers"`
}

func (s *server) trackList(pid string) *trackList {
	a := s.app
	out := &trackList{ID: pid, Tiers: map[string]int{}}
	add := func(r *Row) {
		out.Rows = append(out.Rows, r)
		if r.Tier != "" {
			out.Tiers[r.Tier]++
		}
	}
	if pid == "" || pid == "all" {
		out.ID, out.Name = "all", "Collection"
		tracks := append([]*rbdb.Track(nil), a.Src.Tracks()...)
		sort.SliceStable(tracks, func(i, j int) bool { return tracks[i].Added > tracks[j].Added })
		for _, t := range tracks {
			add(s.row(t))
		}
		return out
	}
	if strings.HasPrefix(pid, "sc:") { // imported but waiting for rekordbox to close
		for _, p := range a.SCPlaylists() {
			if p.URL == strings.TrimPrefix(pid, "sc:") {
				out.Name, out.SC = p.Title, p
				s.scRows(p, add)
			}
		}
		return out
	}
	name := pid
	var find func(ps []*rbdb.Playlist)
	find = func(ps []*rbdb.Playlist) {
		for _, p := range ps {
			if p.ID == pid {
				name = p.Name
			}
			find(p.Children)
		}
	}
	find(a.Src.Playlists())
	out.Name = name
	if sp := a.SCFor(pid); sp != nil {
		out.SC = sp
		s.scRows(sp, add)
		// Tracks added to the playlist in rekordbox by hand come after the SoundCloud order.
		seen := map[string]bool{}
		for _, r := range out.Rows {
			seen[r.ID] = true
		}
		for _, id := range a.Src.PlaylistTrackIDs(pid) {
			if t := a.Src.Track(id); t != nil && !seen[id] {
				add(s.row(t))
			}
		}
		return out
	}
	for _, id := range a.Src.PlaylistTrackIDs(pid) {
		if t := a.Src.Track(id); t != nil {
			add(s.row(t))
		}
	}
	return out
}

func (s *server) scRows(sp *app.SCPlaylist, add func(*Row)) {
	for _, e := range sp.Entries {
		var r *Row
		if t := s.track(e.TrackID); t != nil {
			r = s.row(t)
		} else {
			r = &Row{Title: e.SC.Title, Artist: e.SC.Uploader, Length: int(e.SC.DurationMS / 1000)}
		}
		r.SC, r.Status, r.Note = e.SC, e.Status, e.Note
		if r.ID != "" && r.Status != "downloaded" {
			r.Status = "have"
		}
		add(r)
	}
}

// serveAudio streams a file with range support, re-wrapping AIFF as WAV
// because Chromium browsers can't play AIFF.
func serveAudio(w http.ResponseWriter, r *http.Request, path string) {
	ext := strings.ToLower(filepath.Ext(path))
	w.Header().Set("Cache-Control", "no-store")
	if ext == ".aif" || ext == ".aiff" {
		rs, err := audio.AIFFAsWAV(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
			return
		}
		defer rs.Close()
		w.Header().Set("Content-Type", "audio/wav")
		http.ServeContent(w, r, "audio.wav", time.Time{}, rs)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	types := map[string]string{".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".mp4": "audio/mp4", ".aac": "audio/aac",
		".flac": "audio/flac", ".wav": "audio/wav", ".ogg": "audio/ogg", ".opus": "audio/ogg"}
	if t := types[ext]; t != "" {
		w.Header().Set("Content-Type", t)
	}
	http.ServeContent(w, r, filepath.Base(path), st.ModTime(), f)
}

// ---- state ----

type state struct {
	Version     string          `json:"version"`
	Source      *app.SourceInfo `json:"source,omitempty"`
	SourceErr   string          `json:"sourceErr,omitempty"`
	MusicDir    string          `json:"musicDir"`
	HasToken    bool            `json:"hasToken"`
	MinKbps     int             `json:"minKbps"`
	Tracks      int             `json:"tracks"`
	Lossless    int             `json:"lossless"`
	Upscaled    int             `json:"upscaled"`
	ScannedAt   time.Time       `json:"scannedAt,omitzero"`
	Scanning    *progress       `json:"scanning,omitempty"`
	ScanErr     string          `json:"scanErr,omitempty"`
	Quarantined int             `json:"quarantined"`
	Pending     []*app.Change   `json:"pending"`
	Syncing     []string        `json:"syncing"` // links being imported/synced now
}

func (s *server) state() state {
	a := s.app
	st := state{Version: s.version, MusicDir: a.Cfg.MusicDir, HasToken: a.Cfg.SCToken != "", MinKbps: a.Cfg.MinKbps,
		SourceErr: a.SrcErr, Quarantined: a.Quarantined(), Pending: a.PendingChanges()}
	if st.Pending == nil {
		st.Pending = []*app.Change{}
	}
	if st.Syncing = a.Syncing(); st.Syncing == nil {
		st.Syncing = []string{}
	}
	if a.Src != nil {
		info := a.Src.Info()
		st.Source = &info
	}
	if lib := a.Lib; lib != nil {
		st.Tracks, st.ScannedAt = len(lib.Tracks), lib.ScannedAt
		for _, t := range lib.Tracks {
			if t.Lossless {
				st.Lossless++
			}
			if t.Upscaled() != "" {
				st.Upscaled++
			}
		}
	}
	s.mu.Lock()
	if s.scanning != nil {
		p := *s.scanning
		st.Scanning = &p
	}
	st.ScanErr = s.scanErr
	s.mu.Unlock()
	return st
}

// busy is true while the file scan runs (not during the background quality check).
func (s *server) busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scanning != nil && s.scanning.Phase == "scan"
}

func (s *server) startScan() {
	s.mu.Lock()
	if s.scanning != nil {
		s.mu.Unlock()
		return
	}
	s.scanning, s.scanErr = &progress{Phase: "scan"}, ""
	s.mu.Unlock()
	go func() {
		report := func(done, total int) {
			s.mu.Lock()
			s.scanning.Done, s.scanning.Total = done, total
			s.mu.Unlock()
		}
		err := s.app.Scan(report)
		if err == nil && s.app.QualityCheckPending() > 0 {
			s.mu.Lock()
			s.scanning = &progress{Phase: "quality", Total: s.app.QualityCheckPending()}
			s.mu.Unlock()
			err = s.app.CheckQuality(report)
		}
		s.mu.Lock()
		s.scanning = nil
		if err != nil {
			s.scanErr = err.Error()
		}
		s.mu.Unlock()
	}()
}
