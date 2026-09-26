package rbdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"supersync/internal/sqlcipher"
)

// Uses pyrekordbox's real rekordbox 6 test library (MIT). Set
// REKORDBOX_TESTDATA to the folder with master_locked.db and masterPlaylists6.xml.
func library(t *testing.T) *Location {
	src := os.Getenv("REKORDBOX_TESTDATA")
	if src == "" {
		t.Skip("REKORDBOX_TESTDATA not set")
	}
	dir := t.TempDir()
	copyFile(filepath.Join(src, "master_locked.db"), filepath.Join(dir, "master.db"))
	copyFile(filepath.Join(src, "masterPlaylists6.xml"), filepath.Join(dir, "masterPlaylists6.xml"))
	loc, err := Find(filepath.Join(dir, "master.db"))
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestReadAndWrite(t *testing.T) {
	loc := library(t)
	db, err := Open(loc)
	if err != nil {
		t.Fatal(err)
	}
	pls, _ := db.Playlists()
	if len(pls) != 1 || pls[0].Name != "Trial playlist - Cloud Library Sync" {
		t.Fatalf("playlists: %+v", pls)
	}
	db.Close()

	music := t.TempDir()
	var files []string
	for _, n := range []string{"Artist A - Tune One.mp3", "Artist B - Tune Two.wav"} {
		p := filepath.Join(music, n)
		os.WriteFile(p, []byte("not really audio"), 0o644)
		files = append(files, p)
	}

	tx, err := Begin(loc)
	if err != nil {
		t.Fatal(err)
	}
	folder, err := tx.CreatePlaylist("SoundCloud", "root", true)
	if err != nil {
		t.Fatal(err)
	}
	pl, err := tx.CreatePlaylist("Friday Night", folder, false)
	if err != nil {
		t.Fatal(err)
	}
	a, err := tx.AddTrack(NewTrack{Path: files[0], Title: "Tune One", Artist: "Artist A", Length: 300, BitRate: 320, SampleRate: 44100})
	if err != nil {
		t.Fatal(err)
	}
	b, err := tx.AddTrack(NewTrack{Path: files[1], Title: "Tune Two", Artist: "Artist B", Genre: "House", Length: 360, BitRate: 1411, BitDepth: 16, SampleRate: 44100})
	if err != nil {
		t.Fatal(err)
	}
	again, _ := tx.AddTrack(NewTrack{Path: files[0]})
	if again != a {
		t.Errorf("re-adding a file made a new entry (%s vs %s)", again, a)
	}
	if err := tx.AddToPlaylist(pl, a, b, a); err != nil {
		t.Fatal(err)
	}
	backups := t.TempDir()
	bdir, err := tx.Commit(backups)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bdir, "master.db")); err != nil {
		t.Errorf("no backup: %v", err)
	}

	if out := os.Getenv("KEEP_WRITTEN"); out != "" {
		copyFile(loc.DB, out)
	}
	// The written file is still a valid SQLCipher database, in WAL mode, with 80 reserved bytes.
	plain, _, err := sqlcipher.Decrypt(loc.DB, Passphrase)
	if err != nil {
		t.Fatal(err)
	}
	if plain[18] != 2 || plain[19] != 2 || plain[20] != 80 {
		t.Errorf("header changed: %v", plain[18:21])
	}

	db, err = Open(loc)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pls, _ = db.Playlists()
	var sc *Playlist
	for _, p := range pls {
		if p.Name == "SoundCloud" {
			sc = p
		}
	}
	if sc == nil || sc.Kind != "folder" || len(sc.Children) != 1 || sc.Children[0].Name != "Friday Night" || sc.Children[0].Count != 2 {
		t.Fatalf("tree: %+v", pls)
	}
	ids, _ := db.PlaylistTrackIDs(pl)
	if len(ids) != 2 || ids[0] != a || ids[1] != b {
		t.Errorf("playlist order: %v", ids)
	}
	tracks, _ := db.Tracks()
	found := 0
	for _, tr := range tracks {
		if tr.ID == a || tr.ID == b {
			found++
		}
		if tr.ID == b && (tr.Artist != "Artist B" || tr.Genre != "House" || tr.FileType != 11 || tr.Length != 360) {
			t.Errorf("track b: %+v", tr)
		}
	}
	if found != 2 {
		t.Errorf("new tracks found: %d of %d", found, len(tracks))
	}
	var usn int
	db.SQL.QueryRow(`SELECT int_1 FROM agentRegistry WHERE registry_id='localUpdateCount'`).Scan(&usn)
	if usn <= 249 {
		t.Errorf("update counter not advanced: %d", usn)
	}
	x, _ := os.ReadFile(loc.PlaylistXML)
	if !strings.Contains(string(x), `Id="`+hexID(pl)+`" ParentId="`+hexID(folder)+`" Attribute="0"`) ||
		!strings.Contains(string(x), `Id="`+hexID(folder)+`" ParentId="0" Attribute="1"`) {
		t.Errorf("masterPlaylists6.xml:\n%s", x)
	}
}
