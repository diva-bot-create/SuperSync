package rekordbox

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocationRoundTrip(t *testing.T) {
	for _, p := range []string{
		"/Users/dj/Music/Beatport/HayaT - Gin & Juice (HayaT Remix).aiff",
		"/Volumes/USB Stick/Música/Beyoncé – CUFF IT #1 100%.mp3",
	} {
		loc := LocationFromPath(p)
		if !strings.HasPrefix(loc, "file://localhost/") || strings.Contains(loc, " ") {
			t.Errorf("bad location %q", loc)
		}
		if got := PathFromLocation(loc); got != filepath.FromSlash(p) {
			t.Errorf("round trip %q -> %q -> %q", p, loc, got)
		}
	}
	// rekordbox on Windows writes the drive letter after localhost/.
	if got := filepath.ToSlash(PathFromLocation("file://localhost/C:/Music/a%20b.mp3")); got != "C:/Music/a b.mp3" {
		t.Errorf("windows location -> %q", got)
	}
	if got := LocationFromPath("C:/Music/a b.mp3"); got != "file://localhost/C:/Music/a%20b.mp3" {
		t.Errorf("windows path -> %q", got)
	}
}

func TestWriteThenRead(t *testing.T) {
	var buf bytes.Buffer
	tracks := []PlaylistTrack{
		{Path: "/m/a & b.mp3", Name: "A & B", Artist: "X", Duration: 200, Kind: "MP3 File"},
		{Path: "/m/c.flac", Name: "C", Artist: "Y", Duration: 300},
	}
	if err := WritePlaylists(&buf, "SuperSync", []Playlist{{Name: "Friday", Tracks: tracks}}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "out.xml")
	os.WriteFile(p, buf.Bytes(), 0o644)
	c, err := ReadCollection(p)
	if err != nil {
		t.Fatal(err)
	}
	ct := c.Lookup("/m/a & b.mp3")
	if len(c) != 2 || ct == nil || ct.Name != "A & B" {
		t.Fatalf("read back %d tracks, a&b=%+v", len(c), ct)
	}
	if len(ct.Playlists) != 1 || ct.Playlists[0] != "SuperSync / Friday" {
		t.Errorf("playlists = %q", ct.Playlists)
	}
}

func TestCarryRawData(t *testing.T) {
	src := `<?xml version="1.0" encoding="UTF-8"?>
<DJ_PLAYLISTS Version="1.0.0"><COLLECTION Entries="1">
<TRACK TrackID="7" Name="Old" Artist="A" Rating="255" Colour="0xFF007F" Comments="banger" Location="file://localhost/m/old.mp3">
<TEMPO Inizio="0.025" Bpm="126.00" Metro="4/4" Battito="1"/>
<POSITION_MARK Name="drop" Type="0" Start="61.025" Num="0" Red="40" Green="226" Blue="20"/>
<POSITION_MARK Name="" Type="4" Start="90.5" End="98.1" Num="-1"/>
</TRACK></COLLECTION><PLAYLISTS><NODE Type="0" Name="ROOT" Count="0"/></PLAYLISTS></DJ_PLAYLISTS>`
	dir := t.TempDir()
	in := filepath.Join(dir, "in.xml")
	os.WriteFile(in, []byte(src), 0o644)
	c, err := ReadCollection(in)
	if err != nil {
		t.Fatal(err)
	}
	old := c.Lookup("/m/old.mp3")
	if old == nil || len(old.Marks) != 2 || len(old.Tempos) != 1 || old.Marks[0].Get("Name") != "drop" {
		t.Fatalf("parsed %+v", old)
	}
	var buf bytes.Buffer
	WritePlaylists(&buf, "SuperSync", []Playlist{{Name: "x", Tracks: []PlaylistTrack{{
		Path: "/m/new.flac", Name: "New", Kind: "FLAC File", Attrs: old.Attrs, Tempos: old.Tempos, Marks: old.Marks,
	}}}})
	out := filepath.Join(dir, "out.xml")
	os.WriteFile(out, buf.Bytes(), 0o644)
	c2, err := ReadCollection(out)
	if err != nil {
		t.Fatal(err)
	}
	n := c2.Lookup("/m/new.flac")
	if n == nil || n.Name != "New" || n.Artist != "A" || getAttr(n.Attrs, "Rating") != "255" ||
		getAttr(n.Attrs, "Kind") != "FLAC File" || len(n.Marks) != 2 || n.Marks[1].Get("End") != "98.1" ||
		n.Tempos[0].Get("Bpm") != "126.00" {
		t.Errorf("carried: %+v\n%s", n, buf.String())
	}
}

func TestLibraryRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lib.xml")
	l, err := LoadLibrary(p)
	if err != nil || len(l.Tracks) != 0 {
		t.Fatal(err)
	}
	sc := l.Child(l.Root, "SoundCloud", true)
	pl := l.Child(sc, "Friday", false)
	a := l.AddTrack(PlaylistTrack{Path: "/m/a.mp3", Name: "A", Artist: "X", Duration: 200, Kind: "MP3 File"})
	b := l.AddTrack(PlaylistTrack{Path: "/m/b.wav", Name: "B"})
	if l.AddTrack(PlaylistTrack{Path: "/m/a.mp3"}) != a {
		t.Error("duplicate path added twice")
	}
	pl.Keys = append(pl.Keys, a.ID, b.ID)
	if err := l.Save(); err != nil {
		t.Fatal(err)
	}
	l2, err := LoadLibrary(p)
	if err != nil {
		t.Fatal(err)
	}
	n := l2.Node(pl.ID)
	if len(l2.Tracks) != 2 || n == nil || n.Name != "Friday" || len(n.Keys) != 2 || l2.Track(n.Keys[1]).Get("Name") != "B" {
		t.Fatalf("reloaded: %+v %+v", l2.Tracks, n)
	}
	// Also readable as a plain collection export.
	c, err := ReadCollection(p)
	if err != nil || c.Lookup("/m/a.mp3") == nil || c.Lookup("/m/a.mp3").Playlists[0] != "SoundCloud / Friday" {
		t.Fatalf("collection view: %v %+v", err, c)
	}
}
