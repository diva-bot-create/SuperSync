package app

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"supersync/internal/soundcloud"
)

// wav writes a few seconds of a tone as a 16-bit mono WAV.
func wav(t *testing.T, path string, hz float64, secs int) {
	const rate = 8000
	n := rate * secs
	b := make([]byte, 44+2*n)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*n))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], rate)
	binary.LittleEndian.PutUint32(b[28:], rate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*n))
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(int16(8000*math.Sin(2*math.Pi*hz*float64(i)/rate))))
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeSC is a SoundCloud with one playlist (id 5) owned by user 77.
type fakeSC struct {
	mu     sync.Mutex
	tracks []int64
	puts   int
}

func (f *fakeSC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	track := func(id int64) map[string]any {
		return map[string]any{"id": id, "title": fmt.Sprintf("Song %d", id), "duration": 20000 + id*1000,
			"permalink_url": fmt.Sprintf("https://soundcloud.com/a/song-%d", id), "user": map[string]any{"id": 100 + id, "username": fmt.Sprintf("Artist %d", id)}}
	}
	authed := r.Header.Get("Authorization") == "OAuth 2-secret"
	switch {
	case r.URL.Path == "/resolve":
		var ts []map[string]any
		for _, id := range f.tracks {
			ts = append(ts, track(id))
		}
		json.NewEncoder(w).Encode(map[string]any{"kind": "playlist", "id": 5, "title": "Mine", "permalink_url": "https://soundcloud.com/dj/sets/mine",
			"user": map[string]any{"id": 77, "username": "dj"}, "tracks": ts})
	case r.URL.Path == "/me" && authed:
		w.Write([]byte(`{"id":77,"username":"dj"}`))
	case r.URL.Path == "/playlists/5" && authed && r.Method == "GET":
		var ts []map[string]int64
		for _, id := range f.tracks {
			ts = append(ts, map[string]int64{"id": id})
		}
		json.NewEncoder(w).Encode(map[string]any{"tracks": ts})
	case r.URL.Path == "/playlists/5" && authed && r.Method == "PUT":
		var body struct {
			Playlist struct {
				Tracks []int64 `json:"tracks"`
			} `json:"playlist"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.tracks = body.Playlist.Tracks
		f.puts++
		w.Write([]byte(`{}`))
	default:
		w.WriteHeader(404)
	}
}

func (f *fakeSC) list() []int64 { f.mu.Lock(); defer f.mu.Unlock(); return slices.Clone(f.tracks) }

func TestTwoWayRemovals(t *testing.T) {
	home := t.TempDir()
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(k, home)
	}
	cache, _ := os.UserCacheDir()
	os.MkdirAll(filepath.Join(cache, "SuperSync"), 0o755)
	os.WriteFile(filepath.Join(cache, "SuperSync", "soundcloud-client-id"), []byte("0123456789abcdef0123456789abcdef"), 0o644)
	fake := &fakeSC{tracks: []int64{1, 2, 3, 4, 5, 6}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	defer soundcloud.UseAPI(srv.URL)()

	music := filepath.Join(home, "music")
	os.MkdirAll(music, 0o755)
	for i := 1; i <= 6; i++ {
		wav(t, filepath.Join(music, fmt.Sprintf("Artist %d - Song %d.wav", i, i)), 200+float64(i)*50, 20+i)
	}
	os.MkdirAll(DataDir(), 0o755)
	cfg, _ := json.Marshal(map[string]any{"musicDir": music, "scToken": "2-secret"})
	os.WriteFile(filepath.Join(DataDir(), "config.json"), cfg, 0o600)

	a := New()
	if a.Src == nil {
		t.Fatal("no library: ", a.SrcErr)
	}
	if n, err := a.AddFolder(nil); err != nil || n != 6 {
		t.Fatalf("add folder: %d %v", n, err)
	}
	link := "https://soundcloud.com/dj/sets/mine"
	sync := func() {
		j, err := a.ImportPlaylist(link)
		if err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(20 * time.Second); j.running(); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("sync didn't finish")
			}
		}
		if v := j.Snapshot(); v.Status != "done" {
			t.Fatalf("sync: %s %s", v.Status, v.Message)
		}
	}
	playlist := func() []string {
		sp := a.scByURLLocked(link)
		var names []string
		for _, id := range a.Src.PlaylistTrackIDs(sp.PlaylistID) {
			base := strings.TrimSuffix(filepath.Base(a.Src.Track(id).Path), ".wav")
			names = append(names, base[strings.LastIndex(base, " ")+1:])
		}
		return names
	}
	idsOf := func(songs ...int) []string {
		sp := a.scByURLLocked(link)
		var out []string
		for _, e := range sp.Entries {
			if slices.Contains(songs, int(e.SC.ID)) {
				out = append(out, e.TrackID)
			}
		}
		return out
	}

	sync()
	sp := a.scByURLLocked(link)
	if !sp.Mine || sp.SCID != 5 || strings.Join(playlist(), ",") != "1,2,3,4,5,6" {
		t.Fatalf("first sync: mine=%v id=%d playlist %v", sp.Mine, sp.SCID, playlist())
	}

	// Two songs taken off the library playlist: off SoundCloud too.
	if _, err := a.EditPlaylists(PlaylistEdit{Op: "remove", ID: sp.PlaylistID, TrackIDs: idsOf(2, 4)}); err != nil {
		t.Fatal(err)
	}
	a.checkDropped("")
	if got := fake.list(); !slices.Equal(got, []int64{1, 3, 5, 6}) {
		t.Fatalf("SoundCloud after taking 2 and 4 off: %v", got)
	}
	sync()
	if got := strings.Join(playlist(), ","); got != "1,3,5,6" {
		t.Fatalf("re-sync put songs back: %s", got)
	}

	// Undo from History: back on SoundCloud and in the playlist.
	h := a.History()
	var scUndo string
	for _, e := range h {
		if strings.HasSuffix(e.Label, "” on SoundCloud") {
			scUndo = e.ID
			if !e.Undoable {
				t.Fatal("SoundCloud change not undoable")
			}
			break
		}
	}
	if scUndo == "" {
		t.Fatalf("no SoundCloud change in history: %+v", h)
	}
	if _, err := a.UndoTo(scUndo); err != nil {
		t.Fatal(err)
	}
	if got := fake.list(); !slices.Equal(got, []int64{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("SoundCloud after undo: %v", got)
	}
	if got := strings.Join(playlist(), ","); got != "1,2,3,4,5,6" {
		t.Fatalf("playlist after undo: %s", got)
	}

	// Most of the playlist at once: asks first.
	puts := fake.puts
	if _, err := a.EditPlaylists(PlaylistEdit{Op: "remove", ID: sp.PlaylistID, TrackIDs: idsOf(1, 2, 3, 4)}); err != nil {
		t.Fatal(err)
	}
	a.checkDropped("")
	sp = a.scByURLLocked(link)
	if fake.puts != puts || len(sp.AskDrop) != 4 {
		t.Fatalf("cleared most of the playlist: puts %d→%d, asking %v", puts, fake.puts, sp.AskDrop)
	}
	if err := a.ConfirmDrops(link, true); err != nil {
		t.Fatal(err)
	}
	if got := fake.list(); !slices.Equal(got, []int64{5, 6}) {
		t.Fatalf("SoundCloud after OK: %v", got)
	}

	// Put one back here: it stays off SoundCloud, but it's in the playlist.
	a.State.mu.Lock()
	var e1 *SCEntry
	for _, e := range sp.Entries {
		if e.SC.ID == 1 {
			e1 = e
		}
	}
	a.State.mu.Unlock()
	if e1 == nil {
		t.Fatal("entry 1 gone before a sync")
	}
	if err := a.Undrop(link, 1); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(playlist(), ","); !strings.Contains(","+got+",", ",1,") {
		t.Fatalf("put back: %s", got)
	}
}
