package app

import (
	"errors"
	"path/filepath"
	"strings"

	"supersync/internal/soundcloud"
)

// EditPlaylists changes the library's playlists (see PlaylistEdit). Deleting
// a synced playlist also stops syncing it.
func (a *App) EditPlaylists(e PlaylistEdit) (*PlaylistEditResult, error) {
	if a.Src == nil {
		return nil, ErrNoSource
	}
	e.Name = strings.TrimSpace(e.Name)
	if (e.Op == "create" || e.Op == "rename") && e.Name == "" {
		return nil, errors.New("give it a name")
	}
	res, err := a.editPlaylists(e, "")
	if err != nil {
		return nil, err
	}
	if e.Op == "remove" {
		go a.checkDropped("") // a synced playlist's songs taken off here
	}
	a.State.mu.Lock()
	if len(res.Deleted) > 0 {
		gone := map[string]bool{}
		for _, id := range res.Deleted {
			gone[id] = true
		}
		kept := a.State.SCPlaylists[:0]
		for _, p := range a.State.SCPlaylists {
			if !gone[p.PlaylistID] {
				kept = append(kept, p)
			}
		}
		a.State.SCPlaylists = kept
	}
	a.remapSCLocked()
	a.State.save()
	a.State.mu.Unlock()
	a.rebuild()
	return res, nil
}

// StopSyncing forgets a playlist's SoundCloud/YouTube link; the playlist
// itself stays in the library.
func (a *App) StopSyncing(url string) error {
	a.State.mu.Lock()
	defer a.State.mu.Unlock()
	for i, p := range a.State.SCPlaylists {
		if p.URL == url {
			a.State.SCPlaylists = append(a.State.SCPlaylists[:i], a.State.SCPlaylists[i+1:]...)
			pend := a.State.Pending[:0]
			for _, c := range a.State.Pending {
				if c.SCURL != url {
					pend = append(pend, c)
				}
			}
			a.State.Pending = pend
			a.State.save()
			return nil
		}
	}
	return errors.New("that playlist isn't synced")
}

// remapSCLocked re-links synced playlists to their library playlists by
// folder and name, for libraries whose playlist IDs can change (SuperSync's
// own XML library renumbers after a delete).
func (a *App) remapSCLocked() {
	if a.Src == nil || a.Src.Info().Kind == "rekordbox" {
		return
	}
	for _, p := range a.State.SCPlaylists {
		folder := "SoundCloud"
		if p.Source == "youtube" {
			folder = "YouTube"
		}
		for _, top := range a.Src.Playlists() {
			if top.Kind == "folder" && top.Name == folder {
				for _, c := range top.Children {
					if c.Name == p.Title && c.Kind != "folder" {
						p.PlaylistID = c.ID
					}
				}
			}
		}
	}
}

// RestoreBackup undoes a playlist change by restoring the backup taken just
// before it. Only offered straight after the change.
func (a *App) RestoreBackup(backup string) error {
	if backup == "" || a.Src == nil {
		return errors.New("nothing to undo")
	}
	if err := a.Src.Restore(backup); err != nil {
		return err
	}
	dropHistoryFrom(backup)
	a.rebuild()
	return nil
}

// SCTracksByPath maps library files to the SoundCloud tracks they were
// downloaded or matched for (from synced playlists), for their download and
// buy links.
func (a *App) SCTracksByPath() map[string]*soundcloud.Track {
	out := map[string]*soundcloud.Track{}
	a.State.mu.Lock()
	defer a.State.mu.Unlock()
	for _, p := range a.State.SCPlaylists {
		for _, e := range p.Entries {
			if e.SC == nil {
				continue
			}
			path := e.File
			if path == "" && e.TrackID != "" && a.Src != nil {
				if t := a.Src.Track(e.TrackID); t != nil {
					path = t.Path
				}
			}
			if path != "" {
				out[filepath.Clean(path)] = e.SC
			}
		}
	}
	return out
}
