// Package rbdb reads and writes rekordbox 6/7's own library database
// (master.db, SQLCipher-encrypted) and its companion masterPlaylists6.xml.
//
// Reads work on a decrypted snapshot and never touch the original. Writes are
// done on a decrypted copy, verified, re-encrypted, and swapped in only while
// rekordbox is closed, after backing up every file they replace.
//
// Table layout and conventions follow pyrekordbox (MIT), which documents the
// database: https://github.com/dylanljones/pyrekordbox
package rbdb

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"supersync/internal/sqlcipher"
)

// Passphrase is rekordbox's database key, as published by pyrekordbox.
const Passphrase = "402fd482c38817c35ffa8ffb8c7d93143b749e7d315df7a81732a1ff43608497"

// Location is where a rekordbox library lives.
type Location struct {
	Dir         string `json:"dir"`
	DB          string `json:"db"`
	PlaylistXML string `json:"playlistXml"` // masterPlaylists6.xml; may not exist
}

// ErrNotFound means no rekordbox 6/7 library exists on this computer.
var ErrNotFound = errors.New("no rekordbox library found on this computer")

// ErrRunning means rekordbox is open, so the database can't be written.
var ErrRunning = errors.New("rekordbox is open — close it so SuperSync can update the library")

// Find locates the library: an explicit path, then rekordbox's own
// options.json (which records a moved library), then the default location.
func Find(explicit string) (*Location, error) {
	if explicit != "" {
		if st, err := os.Stat(explicit); err == nil && st.IsDir() {
			explicit = filepath.Join(explicit, "master.db")
		}
		return at(explicit)
	}
	var candidates []string
	if p := optionsDBPath(); p != "" {
		candidates = append(candidates, p)
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		candidates = append(candidates, filepath.Join(home, "Library", "Pioneer", "rekordbox", "master.db"))
	case "windows":
		if ad := os.Getenv("APPDATA"); ad != "" {
			candidates = append(candidates, filepath.Join(ad, "Pioneer", "rekordbox", "master.db"))
		}
	}
	for _, c := range candidates {
		if loc, err := at(c); err == nil {
			return loc, nil
		}
	}
	return nil, ErrNotFound
}

func at(db string) (*Location, error) {
	if st, err := os.Stat(db); err != nil || st.IsDir() {
		return nil, fmt.Errorf("%s: %w", db, ErrNotFound)
	}
	dir := filepath.Dir(db)
	return &Location{Dir: dir, DB: db, PlaylistXML: filepath.Join(dir, "masterPlaylists6.xml")}, nil
}

// optionsDBPath reads rekordboxAgent's options.json ("db-path"), if present.
func optionsDBPath() string {
	var base string
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, "Library", "Application Support", "Pioneer")
	case "windows":
		base = filepath.Join(os.Getenv("APPDATA"), "Pioneer")
	default:
		return ""
	}
	b, err := os.ReadFile(filepath.Join(base, "rekordboxAgent", "storage", "options.json"))
	if err != nil {
		return ""
	}
	var o struct {
		Options [][]any `json:"options"`
	}
	if json.Unmarshal(b, &o) != nil {
		return ""
	}
	for _, kv := range o.Options {
		if len(kv) == 2 && kv[0] == "db-path" {
			if s, ok := kv[1].(string); ok {
				return s
			}
		}
	}
	return ""
}

// Running reports whether the rekordbox app is open.
func Running() bool {
	switch runtime.GOOS {
	case "windows":
		out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq rekordbox.exe", "/NH").Output()
		return err == nil && strings.Contains(strings.ToLower(string(out)), "rekordbox.exe")
	default:
		// The app's process is "rekordbox"; the background agent is "rekordboxAgent" (harmless).
		return exec.Command("pgrep", "-x", "rekordbox").Run() == nil
	}
}

// DB is an open, decrypted snapshot of the library.
type DB struct {
	Loc      *Location
	SQL      *sql.DB
	salt     []byte
	plain    string
	LoadedAt time.Time
	modTime  time.Time
}

// Open decrypts the library into a private temporary file and opens it.
func Open(loc *Location) (*DB, error) {
	plain, salt, err := sqlcipher.Decrypt(loc.DB, Passphrase)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp("", "supersync-rekordbox-*.db")
	if err != nil {
		return nil, err
	}
	_, werr := f.Write(plain)
	f.Close()
	if werr != nil {
		os.Remove(f.Name())
		return nil, werr
	}
	db, err := sql.Open("sqlite", "file:"+f.Name()+"?mode=ro&immutable=1")
	if err != nil {
		os.Remove(f.Name())
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if err := db.Ping(); err != nil {
		db.Close()
		os.Remove(f.Name())
		return nil, fmt.Errorf("couldn't read the rekordbox library: %w", err)
	}
	return &DB{Loc: loc, SQL: db, salt: salt, plain: f.Name(), LoadedAt: time.Now(), modTime: newest(loc)}, nil
}

// Changed reports whether rekordbox has written to the library since Open.
func (d *DB) Changed() bool { return newest(d.Loc).After(d.modTime) }

func newest(loc *Location) time.Time {
	var t time.Time
	for _, p := range []string{loc.DB, loc.DB + "-wal"} {
		if st, err := os.Stat(p); err == nil && st.ModTime().After(t) {
			t = st.ModTime()
		}
	}
	return t
}

func (d *DB) Close() error {
	err := d.SQL.Close()
	os.Remove(d.plain)
	return err
}
