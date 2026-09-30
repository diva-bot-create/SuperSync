package rbdb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

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

func TestMerge(t *testing.T) {
	loc := library(t)
	music := t.TempDir()
	rip, master := filepath.Join(music, "rip.mp3"), filepath.Join(music, "master.wav")
	os.WriteFile(rip, []byte("x"), 0o644)
	os.WriteFile(master, []byte("x"), 0o644)

	tx, err := Begin(loc)
	if err != nil {
		t.Fatal(err)
	}
	extra, _ := tx.AddTrack(NewTrack{Path: rip, Title: "Tune", Artist: "A"})
	keep, _ := tx.AddTrack(NewTrack{Path: master, Title: "Tune", Artist: "A"})
	p1, _ := tx.CreatePlaylist("Only rip", "root", false)
	p2, _ := tx.CreatePlaylist("Both", "root", false)
	tx.AddToPlaylist(p1, extra)
	tx.AddToPlaylist(p2, keep, extra)
	tx.tx.Exec(`UPDATE djmdContent SET DJPlayCount = 5, Rating = 204, Commnt = 'banger' WHERE ID = ?`, extra)
	tx.tx.Exec(`UPDATE djmdContent SET DJPlayCount = 2 WHERE ID = ?`, keep)
	for _, c := range []map[string]any{
		{"InMsec": 16825, "OutMsec": -1, "Kind": 1, "ColorTableIndex": 5, "Comment": "Drop"},
		{"InMsec": 80825, "OutMsec": 88825, "Kind": 0, "ColorTableIndex": 0, "Comment": "Loop"},
		{"InMsec": 300, "OutMsec": -1, "Kind": 2, "Comment": "too early"},
	} {
		c["ID"], c["ContentID"], c["UUID"], c["created_at"], c["updated_at"] = uuidLike(), extra, uuidLike(), tx.now, tx.now
		if err := tx.insert("djmdCue", c); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.insert("djmdSongHistory", map[string]any{"ID": uuidLike(), "HistoryID": "1", "ContentID": extra, "TrackNo": 1, "UUID": uuidLike(), "created_at": tx.now, "updated_at": tx.now}); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Commit(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	tx, err = Begin(loc)
	if err != nil {
		t.Fatal(err)
	}
	shift := -0.825
	if err := tx.Merge(keep, extra, &shift, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Commit(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	db, err := Open(loc)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	db.SQL.QueryRow(`SELECT COUNT(*) FROM djmdContent WHERE ID = ?`, extra).Scan(&n)
	if n != 0 {
		t.Error("duplicate still in the collection")
	}
	if ids, _ := db.PlaylistTrackIDs(p1); len(ids) != 1 || ids[0] != keep {
		t.Errorf("playlist with only the rip: %v", ids)
	}
	if ids, _ := db.PlaylistTrackIDs(p2); len(ids) != 1 || ids[0] != keep {
		t.Errorf("playlist with both: %v", ids)
	}
	var tn int
	db.SQL.QueryRow(`SELECT TrackNo FROM djmdSongPlaylist WHERE PlaylistID = ?`, p2).Scan(&tn)
	if tn != 1 {
		t.Errorf("track numbers not closed up: %d", tn)
	}
	cues, _ := db.Cues(keep)
	if len(cues) != 2 || cues[0].InMs != 16000 || cues[0].Hot != 1 || cues[0].Comment != "Drop" || cues[0].Color != 5 ||
		cues[1].InMs != 80000 || cues[1].OutMs != 88000 {
		t.Errorf("cues on kept track: %+v", cues)
	}
	var plays, rating int
	var comment string
	db.SQL.QueryRow(`SELECT DJPlayCount, Rating, Commnt FROM djmdContent WHERE ID = ?`, keep).Scan(&plays, &rating, &comment)
	if plays != 7 || rating != 204 || comment != "banger" {
		t.Errorf("merged stats: plays %d rating %d comment %q", plays, rating, comment)
	}
	db.SQL.QueryRow(`SELECT COUNT(*) FROM djmdSongHistory WHERE ContentID = ?`, keep).Scan(&n)
	if n != 1 {
		t.Errorf("history not moved")
	}
	db.SQL.QueryRow(`SELECT COUNT(*) FROM djmdCue WHERE ContentID = ?`, extra).Scan(&n)
	if n != 0 {
		t.Errorf("duplicate's cues left behind")
	}
}

func uuidLike() string { return uuid.NewString() }

// Two SuperSync writes must take turns (not overwrite each other), and a
// write must refuse if something else changed the library meanwhile.
func TestWritesTakeTurns(t *testing.T) {
	loc := library(t)
	tx, err := Begin(loc)
	if err != nil {
		t.Fatal(err)
	}
	second := make(chan *Tx)
	go func() { tx2, _ := Begin(loc); second <- tx2 }()
	select {
	case <-second:
		t.Fatal("a second write began while the first was open")
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := tx.CreatePlaylist("First", "root", false); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Commit(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	tx2 := <-second
	if tx2 == nil {
		t.Fatal("second write didn't begin")
	}
	if id, _ := tx2.FindPlaylist("First", "root", false); id == "" {
		t.Fatal("second write didn't see the first one's change")
	}
	// Something else writes the library while tx2 is open.
	later := time.Now().Add(5 * time.Second)
	os.Chtimes(loc.DB, later, later)
	if _, err := tx2.Commit(t.TempDir()); !errors.Is(err, ErrChanged) {
		t.Fatalf("commit over an outside change: got %v, want ErrChanged", err)
	}
}

func TestParseStream(t *testing.T) {
	for _, c := range []struct{ in, svc, id string }{
		{"soundcloud:tracks:227350566", "soundcloud", "227350566"},
		{"beatport:tracks:17289123", "beatport", "17289123"},
		{"tidal:tracks:1234", "tidal", "1234"},
		{"https://soundcloud.com/x/123", "https", "123"},
		{"/Users/dj/Music/a.mp3", "", ""},
		{"C:/Users/dj/Music/a.mp3", "", ""},
		{`\\nas\music\a.mp3`, "", ""},
		{"/Volumes/USB/Contents/tracks:1.mp3", "", ""},
	} {
		svc, id := parseStream(c.in)
		if svc != c.svc || id != c.id {
			t.Errorf("parseStream(%q) = %q, %q; want %q, %q", c.in, svc, id, c.svc, c.id)
		}
	}
}

func TestPlaylistEdits(t *testing.T) {
	loc := library(t)
	db, err := Open(loc)
	if err != nil {
		t.Fatal(err)
	}
	tracks, _ := db.Tracks()
	db.Close()
	if len(tracks) < 2 {
		t.Skip("test library has too few tracks")
	}
	tx, err := Begin(loc)
	if err != nil {
		t.Fatal(err)
	}
	folder, _ := tx.CreatePlaylist("Gigs", "root", true)
	pl, _ := tx.CreatePlaylist("Friday", folder, false)
	keep, _ := tx.CreatePlaylist("Keep me", "root", false)
	if err := tx.AddToPlaylist(pl, tracks[0].ID, tracks[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.RenamePlaylist(keep, "Kept"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Commit(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	xmlBefore, _ := os.ReadFile(loc.PlaylistXML)
	if !strings.Contains(string(xmlBefore), hexID(pl)) {
		t.Fatal("new playlist missing from masterPlaylists6.xml")
	}

	tx, _ = Begin(loc)
	if err := tx.RemoveFromPlaylist(pl, tracks[0].ID); err != nil {
		t.Fatal(err)
	}
	gone, err := tx.DeletePlaylist(folder)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 2 {
		t.Fatalf("deleted %v, want the folder and its playlist", gone)
	}
	if _, err := tx.Commit(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	db, _ = Open(loc)
	defer db.Close()
	pls, _ := db.Playlists()
	var names []string
	var walk func([]*Playlist)
	walk = func(ps []*Playlist) {
		for _, p := range ps {
			names = append(names, p.Name)
			walk(p.Children)
		}
	}
	walk(pls)
	got := strings.Join(names, ",")
	if strings.Contains(got, "Gigs") || strings.Contains(got, "Friday") || !strings.Contains(got, "Kept") {
		t.Fatalf("playlists after delete: %s", got)
	}
	after, _ := db.Tracks()
	if len(after) != len(tracks) {
		t.Fatalf("collection changed: %d tracks, want %d", len(after), len(tracks))
	}
	xmlAfter, _ := os.ReadFile(loc.PlaylistXML)
	if strings.Contains(string(xmlAfter), hexID(pl)) || strings.Contains(string(xmlAfter), hexID(folder)) || !strings.Contains(string(xmlAfter), hexID(keep)) {
		t.Fatal("masterPlaylists6.xml not updated")
	}
}

func TestLocalPath(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "Café - Track.mp3")
	os.WriteFile(f, []byte("x"), 0o644)
	if got := localPath(f); got != f {
		t.Fatalf("existing path changed: %q", got)
	}
	// rekordbox writes file://localhost/C:/… on Windows, file://localhost/Users/… on a Mac.
	if got := localPath("file://localhost/" + strings.TrimPrefix(filepath.ToSlash(f), "/")); got != f {
		t.Fatalf("file:// address not resolved: %q", got)
	}
	if got := localPath("/nowhere/at/all.mp3"); got != "/nowhere/at/all.mp3" {
		t.Fatalf("unknown path changed: %q", got)
	}
}

// When both copies have cues: keep's, the other's, or both combined.
func TestMergeCueModes(t *testing.T) {
	for _, mode := range []CueMode{CuesKeep, CuesOther, CuesBoth} {
		t.Run(string(mode), func(t *testing.T) {
			loc := library(t)
			music := t.TempDir()
			a, b := filepath.Join(music, "a.mp3"), filepath.Join(music, "b.wav")
			os.WriteFile(a, []byte("x"), 0o644)
			os.WriteFile(b, []byte("x"), 0o644)
			tx, err := Begin(loc)
			if err != nil {
				t.Fatal(err)
			}
			extra, _ := tx.AddTrack(NewTrack{Path: a, Title: "Tune"})
			keep, _ := tx.AddTrack(NewTrack{Path: b, Title: "Tune"})
			add := func(id string, kind, in int, comment string) {
				c := map[string]any{"ID": uuidLike(), "ContentID": id, "UUID": uuidLike(), "Kind": kind, "InMsec": in, "OutMsec": -1,
					"Comment": comment, "created_at": tx.now, "updated_at": tx.now}
				if err := tx.insert("djmdCue", c); err != nil {
					t.Fatal(err)
				}
			}
			add(keep, 1, 1000, "keep A")
			add(keep, 2, 20000, "keep B")
			add(extra, 1, 1500, "extra A, same as keep A") // lands on 1000 after the shift
			add(extra, 2, 40500, "extra B")                // B is taken: moves to C
			add(extra, 0, 60500, "extra memory")
			if _, err := tx.Commit(t.TempDir()); err != nil {
				t.Fatal(err)
			}
			tx, _ = Begin(loc)
			shift := -0.5
			if err := tx.Merge(keep, extra, &shift, mode); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Commit(t.TempDir()); err != nil {
				t.Fatal(err)
			}
			db, _ := Open(loc)
			defer db.Close()
			cues, _ := db.Cues(keep)
			var got []string
			for _, c := range cues {
				got = append(got, fmt.Sprintf("%d@%d %s", c.Hot, c.InMs, c.Comment))
			}
			want := map[CueMode][]string{
				CuesKeep:  {"1@1000 keep A", "2@20000 keep B"},
				CuesOther: {"1@1000 extra A, same as keep A", "2@40000 extra B", "0@60000 extra memory"},
				CuesBoth:  {"1@1000 keep A", "2@20000 keep B", "3@40000 extra B", "0@60000 extra memory"},
			}[mode]
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("got %v\nwant %v", got, want)
			}
		})
	}
}
