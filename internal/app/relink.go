package app

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"supersync/internal/audio"
	"supersync/internal/rbdb"
	"supersync/internal/soundcloud"
	"supersync/internal/youtube"
)

// RelinkResult says where a replacement download went.
type RelinkResult struct {
	PlaylistID string `json:"playlistId,omitempty"`
	File       string `json:"file"`
	Waiting    bool   `json:"waiting,omitempty"` // added once rekordbox closes
}

// Relink downloads a synced playlist's track from a link the user found
// (when SoundCloud wouldn't give it up): a SoundCloud track, a YouTube video
// or an audio file on the web. It goes in that track's place in the playlist,
// and later syncs count it as downloaded.
func (a *App) Relink(playlistURL string, scID int64, link string) (*RelinkResult, error) {
	if a.Cfg.MusicDir == "" {
		return nil, errors.New("choose a download folder in Settings first")
	}
	sp := a.scByURLLocked(playlistURL)
	if sp == nil {
		return nil, errors.New("that playlist isn't synced any more")
	}
	var entry *SCEntry
	a.State.mu.Lock()
	for _, e := range sp.Entries {
		if e.SC != nil && e.SC.ID == scID {
			entry = e
		}
	}
	a.State.mu.Unlock()
	if entry == nil {
		return nil, errors.New("that track isn't in the playlist any more")
	}
	folder := "SoundCloud"
	if sp.Source == "youtube" {
		folder = "YouTube"
	}
	dir := filepath.Join(a.Cfg.MusicDir, folder, soundcloud.SafeFilename(sp.Title))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path, err := a.downloadLink(strings.TrimSpace(link), dir, entry.SC)
	if err != nil {
		return nil, err
	}

	a.State.mu.Lock()
	entry.Status, entry.File, entry.TrackID, entry.Note = "downloaded", path, "", "from "+hostOf(link)
	if !strings.Contains(link, "://") {
		entry.Note = "from a file you chose"
	}
	entries := append([]*SCEntry(nil), sp.Entries...)
	a.State.save()
	a.State.mu.Unlock()

	res := &RelinkResult{File: path}
	err = a.applyChange(scChange(folder, sp.Title, sp.URL, entries, false))
	switch {
	case errors.Is(err, rbdb.ErrRunning):
		res.Waiting = true
	case err != nil:
		return nil, err
	}
	res.PlaylistID = sp.PlaylistID
	return res, nil
}

func hostOf(link string) string {
	if u, err := url.Parse(link); err == nil && u.Host != "" {
		return strings.TrimPrefix(u.Hostname(), "www.")
	}
	return "another link"
}

// downloadLink fetches a track from a SoundCloud, YouTube or direct audio link.
func (a *App) downloadLink(link string, dir string, want *soundcloud.Track) (string, error) {
	// A file on this computer (one the user downloaded from a download page):
	// move it into the playlist's folder.
	if st, err := os.Stat(link); err == nil && !st.IsDir() {
		if _, err := audio.Read(link); err != nil || !audio.IsAudio(link) {
			return "", errors.New("that file isn't audio SuperSync can read")
		}
		dst := freePath(filepath.Join(dir, filepath.Base(link)))
		if err := moveFile(link, dst); err != nil {
			return "", err
		}
		return dst, nil
	}
	if !strings.Contains(link, "://") {
		link = "https://" + link
	}
	u, err := url.Parse(link)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("that isn't a web link")
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case strings.HasSuffix(host, "soundcloud.com"):
		pl, err := a.SC.FetchPlaylist(link)
		if err != nil {
			return "", err
		}
		if len(pl.Tracks) != 1 {
			return "", errors.New("that's a SoundCloud playlist; paste the link to one track")
		}
		path, _, err := a.SC.Download(pl.Tracks[0], dir, a.Cfg.SCToken, nil)
		return path, err

	case youtube.IsLink(link):
		// A video link that's also "in" a playlist: just the video.
		if v := u.Query().Get("v"); v != "" {
			link = "https://www.youtube.com/watch?v=" + v
		}
		yt := youtube.New()
		pl, err := yt.List(link)
		if err != nil {
			return "", err
		}
		if len(pl.Entries) != 1 {
			return "", errors.New("that's a YouTube playlist; paste the link to one video")
		}
		return yt.Download(pl.Entries[0], dir, nil)
	}

	// Anything else must be the audio file itself.
	name := "download"
	if want != nil {
		name = want.FileName()
	}
	path, err := a.SC.DownloadURL(link, dir, name)
	if err != nil {
		return "", err
	}
	if _, err := audio.Read(path); err != nil || !audio.IsAudio(path) {
		os.Remove(path)
		return "", fmt.Errorf("that link is a web page, not an audio file. If it's a download page, download the file from it, then use Choose file… to pick what you downloaded")
	}
	return path, nil
}

// ResolveMaybe settles a track SuperSync wasn't sure about. Same: the similar
// file is this track, and it joins the playlist. Different: it isn't, and the
// next sync downloads the track. The answer is remembered for later syncs.
func (a *App) ResolveMaybe(playlistURL string, scID int64, same bool) (*LinkResult, error) {
	sp := a.scByURLLocked(playlistURL)
	if sp == nil {
		return nil, errors.New("that playlist isn't synced any more")
	}
	a.State.mu.Lock()
	var entry *SCEntry
	for _, e := range sp.Entries {
		if e.SC != nil && e.SC.ID == scID {
			entry = e
		}
	}
	a.State.mu.Unlock()
	if entry == nil {
		return nil, errors.New("that track isn't in the playlist any more")
	}
	if !same {
		if err := a.Decide(scID, ""); err != nil {
			return nil, err
		}
		// A track that was matched to the wrong file: take that file out of
		// the synced playlist (it stays in the collection).
		a.State.mu.Lock()
		wrong := entry.TrackID
		entry.Status, entry.Maybe, entry.Note, entry.TrackID, entry.File = "missing", "", "", "", ""
		a.State.save()
		pid := sp.PlaylistID
		a.State.mu.Unlock()
		if wrong != "" && pid != "" {
			if _, err := a.Src.EditPlaylists(PlaylistEdit{Op: "remove", ID: pid, TrackIDs: []string{wrong}}); err != nil {
				return nil, err
			}
			a.rebuild()
		}
		return &LinkResult{}, nil
	}
	path := entry.Maybe
	if path == "" {
		return nil, errors.New("there's no similar file to use")
	}
	if err := a.Decide(scID, path); err != nil {
		return nil, err
	}
	// If this playlist entry already has its own copy (SuperSync downloaded
	// it), that's the same song: keep the better copy and fold in the other,
	// like a duplicate clean-up.
	res := &LinkResult{}
	a.State.mu.Lock()
	current, file := entry.TrackID, entry.File
	a.State.mu.Unlock()
	if tr := a.TrackByPath(path); tr != nil && current != "" && current != tr.ID {
		kept, err := a.foldSame(current, tr.ID)
		if err != nil {
			return nil, err
		}
		res.Folded = true
		if kt := a.Src.Track(kept); kt != nil {
			path = kt.Path
		}
	} else if current == "" && file != "" && file != path && exists(file) {
		a.Quarantine([]string{file}) // a download waiting for rekordbox: the library copy wins
		res.Removed = true
	}
	a.State.mu.Lock()
	entry.Status, entry.Note, entry.File, entry.Maybe = "have", "", "", ""
	if tr := a.TrackByPath(path); tr != nil {
		entry.TrackID = tr.ID
	} else {
		entry.File = path
	}
	entries := append([]*SCEntry(nil), sp.Entries...)
	a.State.save()
	a.State.mu.Unlock()
	folder := "SoundCloud"
	if sp.Source == "youtube" {
		folder = "YouTube"
	}
	err := a.applyChange(scChange(folder, sp.Title, sp.URL, entries, false))
	if errors.Is(err, rbdb.ErrRunning) {
		return res, nil // added when rekordbox closes (it's waiting in the sidebar)
	}
	return res, err
}
