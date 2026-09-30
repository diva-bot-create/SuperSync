package soundcloud

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Changing the user's own playlists, with their login token (the "oauth_token"
// cookie from soundcloud.com, as for original-file downloads).

// ErrNotMine means the playlist belongs to someone else.
var ErrNotMine = errors.New("that playlist isn't yours on SoundCloud")

// authed calls the API as the logged-in user.
func (c *Client) authed(method, path, token string, body, out any) error {
	token = strings.TrimPrefix(strings.TrimSpace(token), "OAuth ")
	if token == "" {
		return errors.New("add your SoundCloud login in Settings first")
	}
	id, err := c.id(false)
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, api+path+"?"+url.Values{"client_id": {id}}.Encode(), rd)
	req.Header.Set("Authorization", "OAuth "+token)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach SoundCloud: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	switch {
	case resp.StatusCode == 401:
		return errUnauthorized
	case resp.StatusCode == 403:
		return ErrNotMine
	case resp.StatusCode == 404:
		return errors.New("SoundCloud says that playlist doesn't exist any more")
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("SoundCloud returned HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// Me is the account the login token belongs to.
func (c *Client) Me(token string) (id int64, username string, err error) {
	var u rawUser
	if err := c.authed("GET", "/me", token, nil, &u); err != nil {
		return 0, "", err
	}
	return u.ID, u.Username, nil
}

// PlaylistTrackIDs is a playlist's tracks, in order, as its owner sees it.
func (c *Client) PlaylistTrackIDs(token string, playlistID int64) ([]int64, error) {
	var p struct {
		TrackCount *int `json:"track_count"`
		Tracks     []struct {
			ID int64 `json:"id"`
		} `json:"tracks"`
	}
	if err := c.authed("GET", "/playlists/"+strconv.FormatInt(playlistID, 10), token, nil, &p); err != nil {
		return nil, err
	}
	// Changing a playlist means sending its whole list back: never work
	// from a list that's missing some.
	if p.TrackCount != nil && *p.TrackCount != len(p.Tracks) {
		return nil, fmt.Errorf("SoundCloud sent %d of the playlist's %d tracks, so SuperSync left it alone", len(p.Tracks), *p.TrackCount)
	}
	ids := make([]int64, len(p.Tracks))
	for i, t := range p.Tracks {
		ids[i] = t.ID
	}
	return ids, nil
}

// SetPlaylistTracks replaces a playlist's tracks with ids, in that order,
// then reads the playlist back to make sure SoundCloud took the change.
func (c *Client) SetPlaylistTracks(token string, playlistID int64, ids []int64) error {
	if ids == nil {
		ids = []int64{}
	}
	body := map[string]any{"playlist": map[string]any{"tracks": ids}}
	if err := c.authed("PUT", "/playlists/"+strconv.FormatInt(playlistID, 10), token, body, nil); err != nil {
		return err
	}
	got, err := c.PlaylistTrackIDs(token, playlistID)
	if err != nil {
		return fmt.Errorf("changed the playlist, but couldn't check it: %w", err)
	}
	if !slices.Equal(got, ids) {
		return errors.New("SoundCloud didn't save the change to the playlist")
	}
	return nil
}

// RemoveFromPlaylist takes tracks off one of the user's playlists (keeping
// the rest in order) and returns the list from just before, for undo.
func (c *Client) RemoveFromPlaylist(token string, playlistID int64, remove []int64) (before []int64, err error) {
	before, err = c.PlaylistTrackIDs(token, playlistID)
	if err != nil {
		return nil, err
	}
	drop := map[int64]bool{}
	for _, id := range remove {
		drop[id] = true
	}
	var keep []int64
	for _, id := range before {
		if !drop[id] {
			keep = append(keep, id)
		}
	}
	if len(keep) == len(before) {
		return before, nil // already gone
	}
	return before, c.SetPlaylistTracks(token, playlistID, keep)
}

// UseAPI points every client at another API address (a test's fake
// SoundCloud) and returns a function that puts it back.
func UseAPI(base string) (restore func()) {
	old := api
	api = base
	return func() { api = old }
}
