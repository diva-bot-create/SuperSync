package rbdb

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"supersync/internal/sqlcipher"
)

// NewTrack describes a file to add to the collection.
type NewTrack struct {
	Path       string
	Title      string
	Artist     string
	Album      string
	Genre      string
	Comment    string
	Length     int // seconds
	BitRate    int
	BitDepth   int
	SampleRate int
}

// Tx is a batch of changes made to a private decrypted copy of the library.
// Nothing reaches rekordbox until Commit succeeds.
type Tx struct {
	loc       *Location
	salt      []byte
	plainPath string
	db        *sql.DB
	tx        *sql.Tx
	usn       int64
	now       string
	device    struct{ ID, MasterDBID string }
	trackLink int64
	playlists []xmlNode // additions for masterPlaylists6.xml
	done      bool
	start     fingerprint // the library as it was when the transaction began
	// ReuseBackup lets a small follow-up write (a playlist filling in during
	// an import) skip making a new backup when SuperSync made one minutes ago
	// and nothing else has touched the library since. Otherwise one import
	// could push all the older backups out of the 10 that are kept.
	ReuseBackup bool
}

var (
	lastWrote    fingerprint // the library right after SuperSync's last write
	lastBackup   string
	lastBackupAt time.Time
)

// writeMu makes SuperSync's own writes take turns: a transaction holds it
// from Begin until Commit or Rollback, so a background sync and a clean-up
// can't both start from the same library and overwrite each other's changes.
var writeMu sync.Mutex

// ErrChanged means the library was written by something else (rekordbox)
// while SuperSync was preparing its change; nothing was written.
var ErrChanged = errors.New("rekordbox's library changed while SuperSync was working on it, so nothing was written; try again")

// HoldWrites waits for any write in progress to finish and blocks new ones
// until release is called (used before SuperSync restarts to update itself).
func HoldWrites() (release func()) {
	writeMu.Lock()
	return writeMu.Unlock
}

type fingerprint struct {
	size, walSize int64
	mod, walMod   time.Time
}

func fingerprintOf(loc *Location) fingerprint {
	var f fingerprint
	if st, err := os.Stat(loc.DB); err == nil {
		f.size, f.mod = st.Size(), st.ModTime()
	}
	if st, err := os.Stat(loc.DB + "-wal"); err == nil {
		f.walSize, f.walMod = st.Size(), st.ModTime()
	}
	return f
}

type xmlNode struct {
	ID, ParentID string
	Attribute    int
	At           time.Time
}

// Begin opens the library for writing. rekordbox must be closed.
func Begin(loc *Location) (_ *Tx, err error) {
	writeMu.Lock()
	unlock := true
	defer func() {
		if unlock {
			writeMu.Unlock()
		}
	}()
	if Running() {
		return nil, ErrRunning
	}
	start := fingerprintOf(loc)
	plain, salt, err := sqlcipher.Decrypt(loc.DB, Passphrase)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "supersync-rbwrite-*")
	if err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "master.db")
	if err := os.WriteFile(p, plain, 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+p+"?_pragma=busy_timeout(5000)")
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	db.SetMaxOpenConns(1)
	unlock = false // from here t.abort releases it
	t := &Tx{loc: loc, salt: salt, plainPath: p, db: db, now: stamp(time.Now()), start: start}
	if t.tx, err = db.Begin(); err != nil {
		t.abort()
		return nil, err
	}
	if err := t.tx.QueryRow(`SELECT IFNULL(int_1,0) FROM agentRegistry WHERE registry_id='localUpdateCount'`).Scan(&t.usn); err != nil {
		t.abort()
		return nil, fmt.Errorf("unexpected library layout (no update counter): %w", err)
	}
	t.tx.QueryRow(`SELECT ID, IFNULL(MasterDBID,'') FROM djmdDevice ORDER BY created_at LIMIT 1`).Scan(&t.device.ID, &t.device.MasterDBID)
	if t.device.MasterDBID == "" {
		t.tx.QueryRow(`SELECT DBID FROM djmdProperty LIMIT 1`).Scan(&t.device.MasterDBID)
	}
	t.tx.QueryRow(`SELECT IFNULL(rb_local_usn,0) FROM djmdMenuItems WHERE Name='TRACK'`).Scan(&t.trackLink)
	return t, nil
}

// stamp formats times the way rekordbox stores them.
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05.000 +00:00") }

func (t *Tx) nextUSN() int64 { t.usn++; return t.usn }

// newID returns an unused random 28-bit ID for a table column, as rekordbox uses.
func (t *Tx) newID(table, col string) (string, error) {
	for i := 0; i < 1000; i++ {
		var b [4]byte
		rand.Read(b[:])
		id := binary.BigEndian.Uint32(b[:]) >> 4
		if id < 100 {
			continue
		}
		s := strconv.FormatUint(uint64(id), 10)
		var n int
		if err := t.tx.QueryRow(`SELECT COUNT(*) FROM "`+table+`" WHERE "`+col+`" = ?`, s).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return s, nil
		}
	}
	return "", errors.New("couldn't find a free ID")
}

// insert adds a row, filling only the given columns (so newer rekordbox
// versions' extra columns keep their defaults).
func (t *Tx) insert(table string, cols map[string]any) error {
	names := make([]string, 0, len(cols))
	for k := range cols {
		names = append(names, k)
	}
	sort.Strings(names)
	q := make([]string, len(names))
	vals := make([]any, len(names))
	for i, n := range names {
		q[i] = "?"
		vals[i] = cols[n]
		names[i] = `"` + n + `"`
	}
	_, err := t.tx.Exec(`INSERT INTO "`+table+`" (`+strings.Join(names, ",")+`) VALUES (`+strings.Join(q, ",")+`)`, vals...)
	if err != nil {
		return fmt.Errorf("adding to %s: %w", table, err)
	}
	return nil
}

// lookup finds a named row (artist, album, genre) or creates it.
func (t *Tx) lookup(table, name string) (any, error) {
	if name == "" {
		return nil, nil
	}
	var id string
	err := t.tx.QueryRow(`SELECT ID FROM "`+table+`" WHERE Name = ? AND IFNULL(rb_local_deleted,0)=0 LIMIT 1`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if id, err = t.newID(table, "ID"); err != nil {
		return nil, err
	}
	return id, t.insert(table, map[string]any{
		"ID": id, "Name": name, "UUID": uuid.NewString(),
		"rb_local_usn": t.nextUSN(), "created_at": t.now, "updated_at": t.now,
	})
}

// FindContent returns the content ID for a file already in the collection, or "".
func (t *Tx) FindContent(path string) (string, error) {
	var id string
	err := t.tx.QueryRow(`SELECT ID FROM djmdContent WHERE FolderPath = ? AND IFNULL(rb_local_deleted,0)=0 LIMIT 1`, filepath.ToSlash(path)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

var fileTypeCodes = map[string]int{".mp3": 1, ".m4a": 4, ".mp4": 4, ".aac": 4, ".flac": 5, ".wav": 11, ".aif": 12, ".aiff": 12}

// AddTrack adds a file to the collection (or returns the existing entry's ID).
// rekordbox analyses it (beatgrid, waveform, key) the first time it's loaded.
func (t *Tx) AddTrack(n NewTrack) (string, error) {
	if id, err := t.FindContent(n.Path); err != nil || id != "" {
		return id, err
	}
	ft, ok := fileTypeCodes[strings.ToLower(filepath.Ext(n.Path))]
	if !ok {
		return "", fmt.Errorf("rekordbox can't import %s files", filepath.Ext(n.Path))
	}
	st, err := os.Stat(n.Path)
	if err != nil {
		return "", err
	}
	id, err := t.newID("djmdContent", "ID")
	if err != nil {
		return "", err
	}
	fileID, err := t.newID("djmdContent", "rb_file_id")
	if err != nil {
		return "", err
	}
	artist, err := t.lookup("djmdArtist", n.Artist)
	if err != nil {
		return "", err
	}
	album, err := t.lookup("djmdAlbum", n.Album)
	if err != nil {
		return "", err
	}
	genre, err := t.lookup("djmdGenre", n.Genre)
	if err != nil {
		return "", err
	}
	title := n.Title
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(n.Path), filepath.Ext(n.Path))
	}
	today := time.Now().Format("2006-01-02")
	cols := map[string]any{
		"ID": id, "UUID": uuid.NewString(), "FolderPath": filepath.ToSlash(n.Path),
		"FileNameL": filepath.Base(n.Path), "FileSize": st.Size(), "FileType": ft,
		"Title": title, "ArtistID": artist, "AlbumID": album, "GenreID": genre,
		"Length": n.Length, "BitRate": n.BitRate, "SampleRate": n.SampleRate,
		"DateCreated": today, "StockDate": today, "HotCueAutoLoad": "on",
		"MasterSongID": id, "rb_file_id": fileID,
		"rb_local_usn": t.nextUSN(), "created_at": t.now, "updated_at": t.now,
	}
	if n.BitDepth > 0 {
		cols["BitDepth"] = n.BitDepth
	}
	if n.Comment != "" {
		cols["Commnt"] = n.Comment
	}
	if t.device.ID != "" {
		cols["DeviceID"] = t.device.ID
	}
	if t.device.MasterDBID != "" {
		cols["MasterDBID"] = t.device.MasterDBID
	}
	if t.trackLink != 0 {
		cols["ContentLink"] = t.trackLink
	}
	return id, t.insert("djmdContent", cols)
}

// FindPlaylist returns the ID of a playlist/folder with this name under
// parent ("root" for top level), or "".
func (t *Tx) FindPlaylist(name, parent string, folder bool) (string, error) {
	attr := 0
	if folder {
		attr = 1
	}
	var id string
	err := t.tx.QueryRow(`SELECT ID FROM djmdPlaylist WHERE Name = ? AND IFNULL(ParentID,'root') = ? AND Attribute = ?
		AND IFNULL(rb_local_deleted,0)=0 LIMIT 1`, name, parent, attr).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// CreatePlaylist adds a playlist (or folder) at the end of parent ("root" or a folder ID).
func (t *Tx) CreatePlaylist(name, parent string, folder bool) (string, error) {
	if parent == "" {
		parent = "root"
	}
	attr := 0
	if folder {
		attr = 1
	}
	if parent != "root" {
		var a int
		if err := t.tx.QueryRow(`SELECT Attribute FROM djmdPlaylist WHERE ID = ?`, parent).Scan(&a); err != nil || a != 1 {
			return "", fmt.Errorf("parent %s isn't a folder", parent)
		}
	}
	var seq int
	t.tx.QueryRow(`SELECT COUNT(*) FROM djmdPlaylist WHERE IFNULL(ParentID,'root') = ? AND IFNULL(rb_local_deleted,0)=0`, parent).Scan(&seq)
	id, err := t.newID("djmdPlaylist", "ID")
	if err != nil {
		return "", err
	}
	if err := t.insert("djmdPlaylist", map[string]any{
		"ID": id, "Seq": seq + 1, "Name": name, "Attribute": attr, "ParentID": parent,
		"UUID": uuid.NewString(), "rb_local_usn": t.nextUSN(), "created_at": t.now, "updated_at": t.now,
	}); err != nil {
		return "", err
	}
	t.playlists = append(t.playlists, xmlNode{ID: id, ParentID: parent, Attribute: attr, At: time.Now()})
	return id, nil
}

// AddToPlaylist appends tracks to a playlist, skipping ones already in it.
func (t *Tx) AddToPlaylist(playlistID string, contentIDs ...string) error {
	var n int
	t.tx.QueryRow(`SELECT COUNT(*) FROM djmdSongPlaylist WHERE PlaylistID = ? AND IFNULL(rb_local_deleted,0)=0`, playlistID).Scan(&n)
	for _, cid := range contentIDs {
		var dup int
		t.tx.QueryRow(`SELECT COUNT(*) FROM djmdSongPlaylist WHERE PlaylistID = ? AND ContentID = ? AND IFNULL(rb_local_deleted,0)=0`, playlistID, cid).Scan(&dup)
		if dup > 0 {
			continue
		}
		n++
		if err := t.insert("djmdSongPlaylist", map[string]any{
			"ID": uuid.NewString(), "PlaylistID": playlistID, "ContentID": cid, "TrackNo": n,
			"UUID": uuid.NewString(), "rb_local_usn": t.nextUSN(), "created_at": t.now, "updated_at": t.now,
		}); err != nil {
			return err
		}
	}
	if _, err := t.tx.Exec(`UPDATE djmdPlaylist SET updated_at = ?, rb_local_usn = ? WHERE ID = ?`, t.now, t.nextUSN(), playlistID); err != nil {
		return err
	}
	return nil
}

// OrderPlaylist puts a playlist's entries in the given order (content IDs);
// entries not listed keep their relative order after them.
func (t *Tx) OrderPlaylist(playlistID string, contentIDs []string) error {
	rows, err := t.tx.Query(`SELECT ID, ContentID FROM djmdSongPlaylist WHERE PlaylistID = ? AND IFNULL(rb_local_deleted,0)=0 ORDER BY TrackNo`, playlistID)
	if err != nil {
		return err
	}
	type entry struct{ id, content string }
	var all []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.content); err != nil {
			rows.Close()
			return err
		}
		all = append(all, e)
	}
	rows.Close()
	rank := map[string]int{}
	for i, c := range contentIDs {
		if _, ok := rank[c]; !ok {
			rank[c] = i
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		ri, iok := rank[all[i].content]
		rj, jok := rank[all[j].content]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		}
		return false
	})
	for n, e := range all {
		if _, err := t.tx.Exec(`UPDATE djmdSongPlaylist SET TrackNo = ?, rb_local_usn = ?, updated_at = ? WHERE ID = ? AND TrackNo <> ?`,
			n+1, t.nextUSN(), t.now, e.id, n+1); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tx) abort() {
	if t.db == nil {
		return // already finished
	}
	if t.tx != nil && !t.done {
		t.tx.Rollback()
	}
	t.db.Close()
	t.db = nil
	os.RemoveAll(filepath.Dir(t.plainPath))
	t.done = true
	writeMu.Unlock()
}

// Rollback discards all changes.
func (t *Tx) Rollback() { t.abort() }

// Commit writes the changes into rekordbox's library: back up, verify,
// re-encrypt, swap in, then update masterPlaylists6.xml. It returns the
// backup folder.
func (t *Tx) Commit(backupRoot string) (string, error) {
	defer t.abort()
	if _, err := t.tx.Exec(`UPDATE agentRegistry SET int_1 = ?, updated_at = ? WHERE registry_id = 'localUpdateCount'`, t.usn, t.now); err != nil {
		return "", err
	}
	if err := t.tx.Commit(); err != nil {
		return "", err
	}
	t.done = true
	var check string
	if err := t.db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		return "", fmt.Errorf("the updated library failed SQLite's integrity check (%s); nothing was changed", check)
	}
	if _, err := t.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return "", err
	}
	t.db.Close()
	plain, err := os.ReadFile(t.plainPath)
	if err != nil {
		return "", err
	}
	enc, err := sqlcipher.Encrypt(plain, Passphrase, t.salt)
	if err != nil {
		return "", err
	}
	// Prove the new file opens with the rekordbox key before replacing anything.
	tmp := t.loc.DB + ".supersync-new"
	if err := os.WriteFile(tmp, enc, 0o644); err != nil {
		return "", err
	}
	if back, _, err := sqlcipher.Decrypt(tmp, Passphrase); err != nil || len(back) != len(plain) {
		os.Remove(tmp)
		return "", fmt.Errorf("verification of the re-encrypted library failed: %v", err)
	}
	if Running() { // opened while we worked
		os.Remove(tmp)
		return "", ErrRunning
	}
	if fingerprintOf(t.loc) != t.start {
		os.Remove(tmp)
		return "", ErrChanged
	}
	var backup string
	if t.ReuseBackup && lastBackup != "" && time.Since(lastBackupAt) < 15*time.Minute && t.start == lastWrote {
		backup = lastBackup
	} else if backup, err = t.backup(backupRoot); err == nil {
		lastBackup, lastBackupAt = backup, time.Now()
	}
	if err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("couldn't back up the library, so nothing was changed: %w", err)
	}
	// The WAL's contents are already folded into the new file.
	for _, p := range []string{t.loc.DB + "-wal", t.loc.DB + "-shm"} {
		os.Remove(p)
	}
	if err := os.Rename(tmp, t.loc.DB); err != nil {
		return backup, err
	}
	lastWrote = fingerprintOf(t.loc)
	if err := t.updatePlaylistXML(); err != nil {
		return backup, fmt.Errorf("library updated, but masterPlaylists6.xml couldn't be: %w", err)
	}
	return backup, nil
}

func (t *Tx) backup(root string) (string, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	// One folder per write, never shared: two writes in the same second
	// mustn't overwrite each other's backup. Names sort by time.
	base := filepath.Join(root, time.Now().Format("2006-01-02 15.04.05.000"))
	dir := base
	for i := 2; ; i++ {
		if err := os.Mkdir(dir, 0o755); err == nil {
			break
		} else if !os.IsExist(err) {
			return "", err
		}
		dir = fmt.Sprintf("%s-%d", base, i)
	}
	for _, p := range []string{t.loc.DB, t.loc.DB + "-wal", t.loc.DB + "-shm", t.loc.PlaylistXML} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := copyFile(p, filepath.Join(dir, filepath.Base(p))); err != nil {
			return "", err
		}
	}
	pruneBackups(root, 10)
	return dir, nil
}

func pruneBackups(root string, keep int) {
	es, err := os.ReadDir(root)
	if err != nil || len(es) <= keep {
		return
	}
	sort.Slice(es, func(i, j int) bool { return es[i].Name() < es[j].Name() })
	for _, e := range es[:len(es)-keep] {
		os.RemoveAll(filepath.Join(root, e.Name()))
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// updatePlaylistXML registers new playlists in masterPlaylists6.xml, which
// rekordbox keeps alongside the database.
func (t *Tx) updatePlaylistXML() error {
	if len(t.playlists) == 0 {
		return nil
	}
	b, err := os.ReadFile(t.loc.PlaylistXML)
	if errors.Is(err, os.ErrNotExist) {
		return nil // older libraries don't have one
	} else if err != nil {
		return err
	}
	s := string(b)
	end := strings.LastIndex(s, "</PLAYLISTS>")
	if end < 0 {
		return errors.New("unexpected format")
	}
	var add strings.Builder
	for _, p := range t.playlists {
		parent := "0"
		if p.ParentID != "root" {
			parent = hexID(p.ParentID)
		}
		fmt.Fprintf(&add, "    <NODE Id=%q ParentId=%q Attribute=\"%d\" Timestamp=\"%d\" Lib_Type=\"0\" CheckType=\"0\"/>\n",
			hexID(p.ID), parent, p.Attribute, p.At.UnixMilli())
	}
	// Keep the file's own indentation before the closing tag.
	lineStart := strings.LastIndex(s[:end], "\n") + 1
	s = s[:lineStart] + add.String() + s[lineStart:]
	if err := xmlWellFormed(s); err != nil {
		return err
	}
	tmp := t.loc.PlaylistXML + ".supersync-new"
	if err := os.WriteFile(tmp, []byte(s), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, t.loc.PlaylistXML)
}

func hexID(id string) string {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return id
	}
	return strings.ToUpper(strconv.FormatUint(n, 16))
}

func xmlWellFormed(s string) error {
	d := xml.NewDecoder(strings.NewReader(s))
	for {
		if _, err := d.Token(); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
	}
}

func readDirSorted(root string) ([]string, error) {
	es, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range es {
		if e.IsDir() {
			out = append(out, filepath.Join(root, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

func decryptCheck(p string) ([]byte, []byte, error) { return sqlcipher.Decrypt(p, Passphrase) }

func removeIfExists(p string) { os.Remove(p) }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func rename(a, b string) error { return os.Rename(a, b) }
