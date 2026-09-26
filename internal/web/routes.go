package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"supersync/internal/analyze"
	"supersync/internal/anlz"
	"supersync/internal/app"
	"supersync/internal/audio"
	"supersync/internal/autostart"
	"supersync/internal/rbdb"
	"supersync/internal/soundcloud"
	"supersync/internal/update"
	"supersync/internal/window"
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
			AutoSync    *int    `json:"autoSyncHours"`
			AutoApply   *bool   `json:"autoApply"`
			AutoUpdate  *bool   `json:"autoUpdate"`
			KeepRunning *bool   `json:"keepRunning"`
			OpenAtLogin *bool   `json:"openAtLogin"`
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
		if err == nil && req.AutoSync != nil && *req.AutoSync >= 0 {
			a.Cfg.AutoSyncHours = *req.AutoSync
			err = a.Cfg.Save()
		}
		if err == nil && req.AutoApply != nil {
			a.Cfg.AutoApply = *req.AutoApply
			err = a.Cfg.Save()
		}
		if err == nil && req.KeepRunning != nil {
			a.Cfg.QuitOnClose = !*req.KeepRunning
			window.SetKeepRunning(*req.KeepRunning)
			err = a.Cfg.Save()
		}
		if err == nil && req.OpenAtLogin != nil {
			if err = autostart.Set(*req.OpenAtLogin); err == nil {
				a.Cfg.OpenAtLogin = *req.OpenAtLogin
				err = a.Cfg.Save()
			}
		}
		if err == nil && req.AutoUpdate != nil {
			a.Cfg.NoAutoUpdate = !*req.AutoUpdate
			if err = a.Cfg.Save(); err == nil && *req.AutoUpdate {
				go s.upd.Check(context.Background())
			}
		}
		reply(w, s.state(), err)
	})

	mux.HandleFunc("POST /api/focus", func(w http.ResponseWriter, r *http.Request) {
		if s.inWindow {
			window.Focus()
		}
		reply(w, map[string]bool{"window": s.inWindow}, nil)
	})
	mux.HandleFunc("POST /api/edit", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action string `json:"action"`
		}
		if !decode(w, r, &req) {
			return
		}
		if s.inWindow {
			window.Edit(req.Action)
		}
		reply(w, map[string]bool{"ok": s.inWindow}, nil)
	})
	mux.HandleFunc("POST /api/quit", func(w http.ResponseWriter, r *http.Request) {
		s.quit()
		reply(w, map[string]bool{"ok": true}, nil)
	})
	// Links from the page open in the default browser (the app window doesn't browse).
	mux.HandleFunc("POST /api/open", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL string `json:"url"`
		}
		if !decode(w, r, &req) {
			return
		}
		u, err := url.Parse(req.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			reply(w, nil, errors.New("not a web link"))
			return
		}
		open(u.String())
		reply(w, map[string]bool{"ok": true}, nil)
	})
	mux.HandleFunc("POST /api/update/check", func(w http.ResponseWriter, r *http.Request) {
		go s.upd.Check(context.Background())
		time.Sleep(150 * time.Millisecond) // usually enough to report "checking"
		reply(w, s.state(), nil)
	})
	mux.HandleFunc("POST /api/update/install", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]bool{"ok": true}, s.installUpdate())
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
		// "dl:<id>" plays a downloaded track that's waiting to join the library.
		if id, ok := strings.CutPrefix(r.PathValue("id"), "dl:"); ok {
			f := s.downloadedFile(id)
			if f == "" {
				http.Error(w, "that download isn't there any more", http.StatusNotFound)
				return
			}
			serveAudio(w, r, f)
			return
		}
		// "sc:<id>" plays a SoundCloud track that isn't in the library yet.
		if id, ok := strings.CutPrefix(r.PathValue("id"), "sc:"); ok {
			path, err := s.soundcloudStream(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			serveAudio(w, r, path)
			return
		}
		t := s.track(r.PathValue("id"))
		if t == nil {
			http.NotFound(w, r)
			return
		}
		path, err := s.audioPath(t)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		serveAudio(w, r, path)
	})

	mux.HandleFunc("GET /api/waveform/{id}", func(w http.ResponseWriter, r *http.Request) {
		var t *rbdb.Track
		var path string
		var err error
		if id, ok := strings.CutPrefix(r.PathValue("id"), "dl:"); ok {
			if path = s.downloadedFile(id); path == "" {
				err = errors.New("that download isn't there any more")
			}
		} else if id, ok := strings.CutPrefix(r.PathValue("id"), "sc:"); ok {
			path, err = s.soundcloudStream(id)
		} else if t = s.track(r.PathValue("id")); t == nil {
			err = errors.New("no such track")
		} else {
			path, err = s.audioPath(t)
		}
		if err != nil {
			reply(w, nil, err)
			return
		}
		wf, err := audio.ComputeWaveform(path, 900, filepath.Join(app.DataDir(), "waveforms"))
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
		if t == nil {
			// A SoundCloud stream: no rekordbox analysis.
		} else if an := a.Src.Analysis(t.ID); an != nil && len(an.Detail) > 0 {
			out.RB = make([]byte, 2*len(an.Detail))
			for i, v := range an.Detail {
				out.RB[2*i], out.RB[2*i+1] = byte(v>>8), byte(v)
			}
			out.RBRate = anlz.DetailRate
		}
		w.Header().Set("Cache-Control", "private, max-age=3600")
		reply(w, out, nil)
	})

	mux.HandleFunc("POST /api/playlists/edit", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			app.PlaylistEdit
			Restart bool `json:"restart"`
		}
		if !decode(w, r, &req) {
			return
		}
		var res *app.PlaylistEditResult
		err := a.WithRekordboxClosed(req.Restart, func() (err error) { res, err = a.EditPlaylists(req.PlaylistEdit); return })
		reply(w, res, err)
	})
	mux.HandleFunc("POST /api/playlists/undo", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Backup  string `json:"backup"`
			Restart bool   `json:"restart"`
		}
		if !decode(w, r, &req) {
			return
		}
		err := a.WithRekordboxClosed(req.Restart, func() error { return a.RestoreBackup(req.Backup) })
		reply(w, map[string]bool{"ok": true}, err)
	})
	mux.HandleFunc("POST /api/sc/maybe", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Playlist string `json:"playlist"`
			SCID     int64  `json:"scId"`
			Same     bool   `json:"same"`
			Restart  bool   `json:"restart"`
		}
		if !decode(w, r, &req) {
			return
		}
		err := a.WithRekordboxClosed(req.Restart, func() error { return a.ResolveMaybe(req.Playlist, req.SCID, req.Same) })
		reply(w, map[string]bool{"ok": true}, err)
	})
	mux.HandleFunc("POST /api/upgrade/swap", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID      string `json:"id"`
			Path    string `json:"path"`
			Restart bool   `json:"restart"`
		}
		if !decode(w, r, &req) {
			return
		}
		var res *app.CleanupResult
		err := a.WithRekordboxClosed(req.Restart, func() (err error) { res, err = a.SwapUpgrade(req.ID, req.Path); return })
		reply(w, res, err)
	})
	mux.HandleFunc("POST /api/upgrade/look", func(w http.ResponseWriter, r *http.Request) {
		a.FindBetterCopies()
		reply(w, map[string]bool{"ok": true}, nil)
	})
	// A plain-text report for tracking down tracks SuperSync can't open.
	mux.HandleFunc("GET /api/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		fmt.Fprintf(&b, "SuperSync %s on %s/%s\n", s.version, runtime.GOOS, runtime.GOARCH)
		if a.Src == nil {
			fmt.Fprintf(&b, "No library open: %s\n", a.SrcErr)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write([]byte(b.String()))
			return
		}
		info := a.Src.Info()
		fmt.Fprintf(&b, "Library: %s at %s\nDownload folder: %s\n", info.Kind, info.Path, a.Cfg.MusicDir)
		var total, streams, missing, noAccess, adjusted, cloud int
		var samples []string
		for _, t := range a.Src.Tracks() {
			total++
			if t.Stream != "" {
				streams++
				continue
			}
			if t.StoredPath != "" && t.StoredPath != t.Path {
				adjusted++
			}
			if t.Cloud {
				cloud++
			}
			_, err := os.Stat(t.Path)
			if err == nil {
				continue
			}
			if os.IsPermission(err) {
				noAccess++
			} else {
				missing++
			}
			if len(samples) < 8 {
				dir := filepath.Dir(filepath.FromSlash(t.Path))
				_, derr := os.Stat(dir)
				samples = append(samples, fmt.Sprintf("- stored: %q\n  looked at: %q\n  cloud: %v, rekordbox's local copy: %q\n  error: %v\n  folder exists: %v", t.StoredPath, t.Path, t.Cloud, t.CloudLocal, err, derr == nil))
			}
		}
		fmt.Fprintf(&b, "Tracks: %d (%d streaming, %d in Cloud Library Sync, %d can't be found, %d can't be read, %d found elsewhere than stored)\n", total, streams, cloud, missing, noAccess, adjusted)
		if len(samples) > 0 {
			b.WriteString("Examples:\n" + strings.Join(samples, "\n") + "\n")
		}
		// For a Cloud Library Sync track: what rekordbox records about it.
		if ci, ok := a.Src.(interface{ CloudInfo(id string) string }); ok {
			for _, t := range a.Src.Tracks() {
				if _, err := os.Stat(t.Path); t.Cloud && err != nil {
					b.WriteString("Cloud track " + t.ID + ":\n" + ci.CloudInfo(t.ID))
					break
				}
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(b.String()))
	})
	mux.HandleFunc("GET /api/missing/find", func(w http.ResponseWriter, r *http.Request) {
		m, err := a.FindMoved()
		if m == nil {
			m = []app.Moved{}
		}
		reply(w, m, err)
	})
	mux.HandleFunc("POST /api/missing/relocate", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Moves   map[string]string `json:"moves"`
			Restart bool              `json:"restart"`
		}
		if !decode(w, r, &req) {
			return
		}
		err := a.WithRekordboxClosed(req.Restart, func() error { return a.Relocate(req.Moves) })
		reply(w, map[string]int{"relocated": len(req.Moves)}, err)
	})
	mux.HandleFunc("POST /api/sc/stop", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL string `json:"url"`
		}
		if !decode(w, r, &req) {
			return
		}
		reply(w, map[string]bool{"ok": true}, a.StopSyncing(req.URL))
	})
	mux.HandleFunc("POST /api/sc/relink", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Playlist string `json:"playlist"`
			SCID     int64  `json:"scId"`
			URL      string `json:"url"`
		}
		if !decode(w, r, &req) {
			return
		}
		res, err := a.Relink(req.Playlist, req.SCID, req.URL)
		reply(w, res, err)
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
		var req struct {
			Restart bool `json:"restart"`
		}
		decodeOptional(r, &req)
		var n int
		err := a.WithRekordboxClosed(req.Restart, func() (err error) { n, err = a.ApplyPending(); return })
		reply(w, map[string]int{"applied": n}, err)
	})
	mux.HandleFunc("POST /api/sync/all", func(w http.ResponseWriter, r *http.Request) {
		go a.SyncAll()
		reply(w, map[string]bool{"ok": true}, nil)
	})
	mux.HandleFunc("POST /api/dupes/cleanup", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Groups  []app.CleanupGroup `json:"groups"`
			Restart bool               `json:"restart"`
		}
		if !decode(w, r, &req) {
			return
		}
		if s.busy() {
			reply(w, nil, errors.New("still scanning"))
			return
		}
		var res *app.CleanupResult
		err := a.WithRekordboxClosed(req.Restart, func() (err error) { res, err = a.CleanupDuplicates(req.Groups); return })
		reply(w, res, err)
	})
	mux.HandleFunc("POST /api/dupes/undo-cleanup", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Restart bool `json:"restart"`
		}
		decodeOptional(r, &req)
		reply(w, map[string]bool{"ok": true}, a.WithRekordboxClosed(req.Restart, a.UndoCleanup))
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
		// Only links confirmed on the track's SoundCloud page (its download
		// button, its description, its buy button): no store searches.
		type upRow struct {
			*analyze.Upgrade
			SC     *soundcloud.Track `json:"sc,omitempty"`
			ID     string            `json:"id,omitempty"` // the library track
			Better *app.BetterCopy   `json:"better,omitempty"`
		}
		sc := a.SCTracksByPath()
		rows := []upRow{}
		for _, x := range u {
			row := upRow{Upgrade: x, SC: sc[filepath.Clean(x.Path)]}
			if t := a.TrackByPath(x.Path); t != nil {
				row.ID, row.Better = t.ID, a.BetterFor(t.ID)
			}
			rows = append(rows, row)
		}
		reply(w, rows, err)
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

// decodeOptional reads a JSON body if there is one.
func decodeOptional(r *http.Request, v any) {
	if r.ContentLength != 0 {
		json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
	}
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
	// NoAccess: the file may well be there, but the system won't let SuperSync read it.
	NoAccess bool `json:"noAccess,omitempty"`
	// Better is a better-quality copy found on disk (not in the library yet).
	Better     *app.BetterCopy `json:"better,omitempty"`
	MissingWhy string          `json:"missingWhy,omitempty"`
	StatErr    string          `json:"statErr,omitempty"` // what the system said when SuperSync looked for the file
	Stream     string          `json:"stream,omitempty"`  // streaming service, for tracks with no file

	// Rows of SoundCloud-imported playlists.
	SC     *soundcloud.Track `json:"sc,omitempty"`
	Status string            `json:"status,omitempty"` // have, downloaded, maybe, missing, failed
	Note   string            `json:"note,omitempty"`
	Maybe  string            `json:"maybe,omitempty"` // a file that might be this track
	File   string            `json:"file,omitempty"`  // downloaded, waiting to join the library
}

func (s *server) row(t *rbdb.Track) *Row {
	tier, kbps, note := s.app.Tier(t)
	r := &Row{ID: t.ID, Title: t.Title, Artist: t.Artist, Album: t.Album, Genre: t.Genre, Key: t.Key, BPM: t.BPM,
		Length: t.Length, Kbps: kbps, Format: rbdb.FileTypes[t.FileType], Tier: tier, TierNote: note,
		Rating: t.Rating, Color: t.Color, Cues: t.Cues, Added: t.Added, Path: t.Path}
	if r.Title == "" {
		r.Title = strings.TrimSuffix(filepath.Base(t.Path), filepath.Ext(t.Path))
	}
	r.Better = s.app.BetterFor(t.ID)
	switch {
	case t.Stream != "":
		r.Stream, r.Tier, r.Kbps, r.Format = t.Stream, "stream", 0, ""
		if r.Title == "" || r.Title == strings.TrimSuffix(filepath.Base(t.Path), filepath.Ext(t.Path)) {
			r.Title = t.Title
		}
	default:
		if _, err := os.Stat(t.Path); err != nil {
			r.Missing = true
			r.NoAccess = os.IsPermission(err)
			if !r.NoAccess {
				r.MissingWhy = app.MissingReason(t.Path)
			}
			// The system's own words help tell a missing file from one that
			// can't be read (cloud placeholders, locked drives...).
			if pe, ok := err.(*os.PathError); ok {
				r.StatErr = pe.Err.Error()
			} else {
				r.StatErr = err.Error()
			}
		}
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

// downloadedFile is the file downloaded for a synced playlist's SoundCloud
// track (by its SoundCloud id), if it's on disk.
func (s *server) downloadedFile(scID string) string {
	for _, p := range s.app.SCPlaylists() {
		for _, e := range p.Entries {
			if e.SC != nil && strconv.FormatInt(e.SC.ID, 10) == scID && e.File != "" {
				if _, err := os.Stat(e.File); err == nil {
					return e.File
				}
			}
		}
	}
	return ""
}

func (s *server) scRows(sp *app.SCPlaylist, add func(*Row)) {
	for _, e := range sp.Entries {
		var r *Row
		if t := s.track(e.TrackID); t != nil {
			r = s.row(t)
		} else {
			r = &Row{Title: e.SC.Title, Artist: e.SC.Uploader, Length: int(e.SC.DurationMS / 1000)}
			// Downloaded (or already on disk) but not in the library yet,
			// usually because rekordbox is open: it plays from the file.
			if e.File != "" {
				if _, err := os.Stat(e.File); err == nil {
					r.File, r.Path = e.File, e.File
				}
			}
		}
		r.SC, r.Status, r.Note, r.Maybe = e.SC, e.Status, e.Note, e.Maybe
		if r.ID != "" && r.Status != "downloaded" {
			r.Status = "have"
		}
		add(r)
	}
}

// serveAudio streams a file with range support, re-wrapping AIFF as WAV
// because Chromium browsers can't play AIFF.
// audioPath is the file to play for a track: its own file, or for a track
// rekordbox streams from SoundCloud, the stream saved once into a cache.
func (s *server) audioPath(t *rbdb.Track) (string, error) {
	switch t.Stream {
	case "":
		return t.Path, nil
	case "soundcloud":
		if t.StreamID != "" {
			return s.soundcloudStream(t.StreamID)
		}
	}
	return "", fmt.Errorf("this track streams from %s inside rekordbox; SuperSync can't play it", serviceName(t.Stream))
}

func serviceName(s string) string {
	switch s {
	case "soundcloud":
		return "SoundCloud"
	case "beatport", "beatsource":
		return strings.ToUpper(s[:1]) + s[1:]
	case "tidal":
		return "TIDAL"
	}
	return s
}

var streamLocks sync.Map // SoundCloud track id -> *sync.Mutex

// soundcloudStream returns the cached stream for a SoundCloud track id,
// fetching it the first time. The cache keeps the 60 most recently played.
func (s *server) soundcloudStream(id string) (string, error) {
	mu, _ := streamLocks.LoadOrStore(id, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	root := filepath.Join(app.CacheDir(), "streams")
	dir := filepath.Join(root, id)
	if es, _ := os.ReadDir(dir); len(es) > 0 {
		now := time.Now()
		os.Chtimes(dir, now, now)
		return filepath.Join(dir, es[0].Name()), nil
	}
	t, err := s.app.SC.TrackByID(id)
	if err != nil {
		return "", fmt.Errorf("couldn't get this track from SoundCloud: %w", err)
	}
	tmp, err := os.MkdirTemp(root+"-tmp", id+"-")
	if err != nil {
		if err = os.MkdirAll(root+"-tmp", 0o755); err == nil {
			tmp, err = os.MkdirTemp(root+"-tmp", id+"-")
		}
		if err != nil {
			return "", err
		}
	}
	defer os.RemoveAll(tmp)
	p, err := s.app.SC.SaveStream(t, tmp)
	if err != nil {
		return "", err
	}
	os.MkdirAll(root, 0o755)
	if err := os.Rename(tmp, dir); err != nil {
		return "", err
	}
	pruneStreams(root, 60)
	return filepath.Join(dir, filepath.Base(p)), nil
}

func pruneStreams(root string, keep int) {
	es, _ := os.ReadDir(root)
	if len(es) <= keep {
		return
	}
	type d struct {
		p string
		t time.Time
	}
	var ds []d
	for _, e := range es {
		if st, err := os.Stat(filepath.Join(root, e.Name())); err == nil {
			ds = append(ds, d{filepath.Join(root, e.Name()), st.ModTime()})
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].t.After(ds[j].t) })
	for _, x := range ds[min(keep, len(ds)):] {
		os.RemoveAll(x.p)
	}
}

func noAccessHelp() string {
	if runtime.GOOS == "darwin" {
		return "Allow SuperSync in System Settings → Privacy & Security → Files & Folders (or Full Disk Access)."
	}
	return "Check the folder's permissions."
}

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
		msg := "The file isn't there any more: " + path
		if os.IsPermission(err) {
			msg = "SuperSync isn't allowed to read this file's folder. " + noAccessHelp()
		}
		http.Error(w, msg, http.StatusNotFound)
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
	AutoSync    int             `json:"autoSyncHours"`
	AutoApply   bool            `json:"autoApply"`
	LastSync    time.Time       `json:"lastSync,omitzero"`
	NextSync    time.Time       `json:"nextSync,omitzero"`
	LastCleanup string          `json:"lastCleanup,omitempty"` // summary, when it can be undone
	Update      update.Status   `json:"update"`
	App         bool            `json:"app"` // in SuperSync's own window
	KeepRunning bool            `json:"keepRunning"`
	OpenAtLogin bool            `json:"openAtLogin"`
	CanAutorun  bool            `json:"canAutorun"`
	AutoUpdate  bool            `json:"autoUpdate"`
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
	st.AutoSync, st.AutoApply, st.NextSync = a.Cfg.AutoSyncHours, a.Cfg.AutoApply, a.NextSync()
	if lc := a.State.Last(); lc != nil {
		st.LastCleanup = lc.Summary
	}
	st.LastSync = a.State.LastSyncTime()
	st.Update, st.AutoUpdate, st.App = s.upd.Status(), !a.Cfg.NoAutoUpdate, s.inWindow
	st.KeepRunning, st.OpenAtLogin, st.CanAutorun = !a.Cfg.QuitOnClose, a.Cfg.OpenAtLogin, autostart.Supported()
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
