package soundcloud

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestRemoveFromPlaylist(t *testing.T) {
	tracks := []int64{1, 2, 3, 4}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "OAuth 2-secret" || r.URL.Query().Get("client_id") == "" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/me":
			w.Write([]byte(`{"id":77,"username":"dj"}`))
		case r.URL.Path == "/playlists/9":
			w.WriteHeader(403) // someone else's
		case r.Method == "GET" && r.URL.Path == "/playlists/5":
			var ts []map[string]int64
			for _, id := range tracks {
				ts = append(ts, map[string]int64{"id": id})
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 5, "tracks": ts})
		case r.Method == "PUT" && r.URL.Path == "/playlists/5":
			var body struct {
				Playlist struct {
					Tracks []int64 `json:"tracks"`
				} `json:"playlist"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || r.Header.Get("Content-Type") != "application/json" {
				w.WriteHeader(400)
				return
			}
			tracks = body.Playlist.Tracks
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := api
	api = srv.URL
	defer func() { api = old }()
	c := New()
	c.clientID = "0123456789abcdef0123456789abcdef"

	if id, name, err := c.Me("OAuth 2-secret"); err != nil || id != 77 || name != "dj" {
		t.Fatalf("me: %d %q %v", id, name, err)
	}
	before, err := c.RemoveFromPlaylist("2-secret", 5, []int64{2, 4})
	if err != nil || !slices.Equal(before, []int64{1, 2, 3, 4}) || !slices.Equal(tracks, []int64{1, 3}) {
		t.Fatalf("remove: before %v now %v err %v", before, tracks, err)
	}
	if err := c.SetPlaylistTracks("2-secret", 5, before); err != nil || !slices.Equal(tracks, before) {
		t.Fatalf("put back: %v %v", tracks, err)
	}
	if _, err := c.RemoveFromPlaylist("2-secret", 9, []int64{1}); !errors.Is(err, ErrNotMine) {
		t.Fatalf("someone else's playlist: %v", err)
	}
	if _, _, err := c.Me("wrong"); !errors.Is(err, errUnauthorized) {
		t.Fatalf("bad token: %v", err)
	}
}
