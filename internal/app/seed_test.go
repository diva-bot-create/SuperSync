package app

import (
	"os"
	"path/filepath"
	"testing"

	"supersync/internal/audio"
	"supersync/internal/rbdb"
)

// TestSeed fills a rekordbox test library with a folder of audio (manual helper:
// SEED_DB=/path/master.db SEED_MUSIC=/path/music go test -run Seed ./internal/app).
func TestSeed(t *testing.T) {
	dbp, music := os.Getenv("SEED_DB"), os.Getenv("SEED_MUSIC")
	if dbp == "" {
		t.Skip()
	}
	loc, err := rbdb.Find(dbp)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := rbdb.Begin(loc)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	filepath.WalkDir(music, func(p string, d os.DirEntry, err error) error {
		if d == nil || d.IsDir() || !audio.IsAudio(p) || filepath.Base(p)[0] == '.' || filepath.Base(filepath.Dir(p)) == "_SuperSync Duplicates" {
			return nil
		}
		in, err := audio.Read(p)
		if err != nil {
			return nil
		}
		id, err := tx.AddTrack(*newTrack(in, "", ""))
		if err != nil {
			t.Logf("skip %s: %v", p, err)
			return nil
		}
		ids = append(ids, id)
		return nil
	})
	friday, _ := tx.CreatePlaylist("Friday", "root", false)
	tx.AddToPlaylist(friday, ids[:len(ids)/2]...)
	gigs, _ := tx.CreatePlaylist("Gigs", "root", true)
	wh, _ := tx.CreatePlaylist("Warehouse", gigs, false)
	tx.AddToPlaylist(wh, ids[len(ids)/3:]...)
	if _, err := tx.Commit(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Logf("added %d tracks", len(ids))
}
