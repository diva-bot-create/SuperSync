package rbdb

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Track is one entry of the rekordbox collection (djmdContent).
type Track struct {
	ID         string  `json:"id"`
	Path       string  `json:"path"`
	Title      string  `json:"title"`
	Artist     string  `json:"artist"`
	Album      string  `json:"album,omitempty"`
	Genre      string  `json:"genre,omitempty"`
	Key        string  `json:"key,omitempty"`
	Label      string  `json:"label,omitempty"`
	Comment    string  `json:"comment,omitempty"`
	BPM        float64 `json:"bpm,omitempty"`
	Length     int     `json:"length"` // seconds
	BitRate    int     `json:"kbps"`
	BitDepth   int     `json:"bits,omitempty"`
	SampleRate int     `json:"hz,omitempty"`
	FileType   int     `json:"fileType"`
	FileSize   int64   `json:"size,omitempty"`
	Rating     int     `json:"rating,omitempty"` // 0-5 stars
	Color      string  `json:"color,omitempty"`
	PlayCount  int     `json:"plays,omitempty"`
	Added      string  `json:"added,omitempty"`
	Year       int     `json:"year,omitempty"`
	Analysed   bool    `json:"analysed"`
	UUID       string  `json:"uuid"`
	Cues       int     `json:"cues"`
	// Analysis is rekordbox's analysis file (ANLZ0000.DAT) for the track, if analysed.
	Analysis string `json:"-"`
}

// Playlist is a playlist, folder, or smart playlist.
type Playlist struct {
	ID       string      `json:"id"`
	ParentID string      `json:"parentId"`
	Name     string      `json:"name"`
	Seq      int         `json:"seq"`
	Kind     string      `json:"kind"` // "playlist", "folder", "smart"
	Count    int         `json:"count"`
	Children []*Playlist `json:"children,omitempty"`
}

// Cue is a hot cue, memory cue, or loop.
type Cue struct {
	ID      string `json:"id"`
	Hot     int    `json:"hot"`           // 0 for memory cues, 1-8 for hot cues A-H
	InMs    int    `json:"inMs"`          //
	OutMs   int    `json:"outMs"`         // > InMs for loops, otherwise -1
	Color   int    `json:"color"`         // rekordbox color table index, -1/0 for default
	RGB     string `json:"rgb,omitempty"` // "#rrggbb" when the source gives an exact color (XML)
	Comment string `json:"comment,omitempty"`
}

// FileTypes maps rekordbox's FileType codes.
var FileTypes = map[int]string{1: "mp3", 4: "m4a", 5: "flac", 11: "wav", 12: "aiff"}

func (d *DB) Tracks() ([]*Track, error) {
	rows, err := d.SQL.Query(`
		SELECT c.ID, IFNULL(c.FolderPath,''), IFNULL(c.Title,''), IFNULL(a.Name,''), IFNULL(al.Name,''),
		       IFNULL(g.Name,''), IFNULL(k.ScaleName,''), IFNULL(l.Name,''), IFNULL(c.Commnt,''),
		       IFNULL(c.BPM,0), IFNULL(c.Length,0), IFNULL(c.BitRate,0), IFNULL(c.BitDepth,0),
		       IFNULL(c.SampleRate,0), IFNULL(c.FileType,0), IFNULL(c.FileSize,0), IFNULL(c.Rating,0),
		       IFNULL(col.Commnt,''), IFNULL(c.DJPlayCount,0), IFNULL(c.StockDate, IFNULL(c.DateCreated,'')),
		       IFNULL(c.ReleaseYear,0), IFNULL(c.Analysed,0), IFNULL(c.UUID,''), IFNULL(c.AnalysisDataPath,''),
		       (SELECT COUNT(*) FROM djmdCue q WHERE q.ContentID = c.ID AND IFNULL(q.rb_local_deleted,0) = 0)
		FROM djmdContent c
		LEFT JOIN djmdArtist a ON a.ID = c.ArtistID
		LEFT JOIN djmdAlbum al ON al.ID = c.AlbumID
		LEFT JOIN djmdGenre g ON g.ID = c.GenreID
		LEFT JOIN djmdKey k ON k.ID = c.KeyID
		LEFT JOIN djmdLabel l ON l.ID = c.LabelID
		LEFT JOIN djmdColor col ON col.ID = c.ColorID
		WHERE IFNULL(c.rb_local_deleted,0) = 0 AND IFNULL(c.FolderPath,'') <> ''
		  AND c.FolderPath NOT LIKE '%/PioneerDJ/Sampler/%'`) // rekordbox's bundled sampler sounds
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Track
	for rows.Next() {
		t := &Track{}
		var bpm, analysed, rating int
		if err := rows.Scan(&t.ID, &t.Path, &t.Title, &t.Artist, &t.Album, &t.Genre, &t.Key, &t.Label,
			&t.Comment, &bpm, &t.Length, &t.BitRate, &t.BitDepth, &t.SampleRate, &t.FileType, &t.FileSize,
			&rating, &t.Color, &t.PlayCount, &t.Added, &t.Year, &analysed, &t.UUID, &t.Analysis, &t.Cues); err != nil {
			return nil, err
		}
		t.BPM = float64(bpm) / 100
		t.Rating = stars(rating)
		t.Analysed = analysed != 0
		if t.Analysis != "" {
			t.Analysis = filepath.Join(d.shareDir(), filepath.FromSlash(strings.TrimLeft(t.Analysis, "/\\")))
		}
		if len(t.Added) > 10 {
			t.Added = t.Added[:10]
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// rekordbox stores ratings as 0, 51, 102, 153, 204, 255.
func stars(v int) int {
	if v > 5 {
		return (v + 25) / 51
	}
	return v
}

// Playlists returns the playlist tree (children of "root"), in rekordbox's order.
func (d *DB) Playlists() ([]*Playlist, error) {
	rows, err := d.SQL.Query(`
		SELECT p.ID, IFNULL(p.ParentID,'root'), IFNULL(p.Name,''), IFNULL(p.Seq,0), IFNULL(p.Attribute,0),
		       (SELECT COUNT(*) FROM djmdSongPlaylist s WHERE s.PlaylistID = p.ID AND IFNULL(s.rb_local_deleted,0) = 0)
		FROM djmdPlaylist p WHERE IFNULL(p.rb_local_deleted,0) = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]*Playlist{}
	var all []*Playlist
	for rows.Next() {
		p := &Playlist{}
		var attr int
		if err := rows.Scan(&p.ID, &p.ParentID, &p.Name, &p.Seq, &attr, &p.Count); err != nil {
			return nil, err
		}
		switch attr {
		case 0:
			p.Kind = "playlist"
		case 1:
			p.Kind = "folder"
		case 4:
			p.Kind = "smart"
		default:
			continue // internal lists (e.g. cloud sync trial, -128)
		}
		byID[p.ID] = p
		all = append(all, p)
	}
	var roots []*Playlist
	for _, p := range all {
		if parent := byID[p.ParentID]; parent != nil {
			parent.Children = append(parent.Children, p)
		} else {
			roots = append(roots, p)
		}
	}
	var sortTree func([]*Playlist)
	sortTree = func(ps []*Playlist) {
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].Seq < ps[j].Seq })
		for _, p := range ps {
			sortTree(p.Children)
		}
	}
	sortTree(roots)
	return roots, rows.Err()
}

// PlaylistTrackIDs lists a playlist's content IDs in order.
func (d *DB) PlaylistTrackIDs(playlistID string) ([]string, error) {
	rows, err := d.SQL.Query(`SELECT ContentID FROM djmdSongPlaylist
		WHERE PlaylistID = ? AND IFNULL(rb_local_deleted,0) = 0 ORDER BY TrackNo`, playlistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Cues returns a track's hot cues, memory cues and loops, in time order.
func (d *DB) Cues(contentID string) ([]Cue, error) {
	rows, err := d.SQL.Query(`SELECT ID, IFNULL(Kind,0), IFNULL(InMsec,0), IFNULL(OutMsec,-1),
		IFNULL(ColorTableIndex, IFNULL(Color,-1)), IFNULL(Comment,'')
		FROM djmdCue WHERE ContentID = ? AND IFNULL(rb_local_deleted,0) = 0 ORDER BY InMsec`, contentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Cue
	var kinds []int
	for rows.Next() {
		var c Cue
		if err := rows.Scan(&c.ID, &c.Hot, &c.InMs, &c.OutMs, &c.Color, &c.Comment); err != nil {
			return nil, err
		}
		if c.OutMs <= c.InMs {
			c.OutMs = -1
		}
		out = append(out, c)
		kinds = append(kinds, c.Hot)
	}
	// Some libraries number hot cues 1,2,3,5,…,9 (skipping 4); fold that onto A-H.
	skip4 := false
	for _, k := range kinds {
		if k == 9 {
			skip4 = true
		}
	}
	for i := range out {
		if skip4 && out[i].Hot >= 5 {
			out[i].Hot--
		}
		if out[i].Hot > 8 {
			out[i].Hot = 8
		}
	}
	return out, rows.Err()
}

// Count returns the number of rows in a table (for status displays).
func (d *DB) Count(table string) int {
	var n int
	d.SQL.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&n)
	return n
}

// TrackPlaylists maps content ID -> names of the playlists it's in ("Folder / Playlist").
func (d *DB) TrackPlaylists() (map[string][]string, error) {
	pls, err := d.Playlists()
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	var walk func(ps []*Playlist, prefix string)
	walk = func(ps []*Playlist, prefix string) {
		for _, p := range ps {
			n := p.Name
			if prefix != "" {
				n = prefix + " / " + n
			}
			names[p.ID] = n
			walk(p.Children, n)
		}
	}
	walk(pls, "")
	rows, err := d.SQL.Query(`SELECT ContentID, PlaylistID FROM djmdSongPlaylist WHERE IFNULL(rb_local_deleted,0)=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var c, p string
		if rows.Scan(&c, &p) == nil && names[p] != "" {
			out[c] = append(out[c], names[p])
		}
	}
	return out, rows.Err()
}

// shareDir is where rekordbox keeps analysis files: the path it recorded in
// agentRegistry, or "share" next to the database.
func (d *DB) shareDir() string {
	if d.share != "" {
		return d.share
	}
	var p string
	d.SQL.QueryRow(`SELECT IFNULL(str_1,'') FROM agentRegistry WHERE registry_id='SyncAnalysisDataRootPath'`).Scan(&p)
	d.share = filepath.Join(d.Loc.Dir, "share")
	if p != "" {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			d.share = p
		}
	}
	return d.share
}
