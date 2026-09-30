package rbdb

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
)

// Merge folds a duplicate collection entry (extra) into the one being kept:
// every playlist, history entry, tag, sampler slot and related-track link
// that pointed at extra points at keep instead (without doubling a track up
// in a playlist); play counts add up; rating, colour and comment carry over
// where keep has none; and extra is removed from the collection.
//
// If cueShift is non-nil, extra's hot cues, memory cues and loops are copied
// onto keep, moved by cueShift seconds (the measured offset between the two
// files). What happens when keep has cues of its own too is up to cues:
// CuesKeep (or "") leaves keep's alone, CuesOther replaces them with extra's,
// CuesBoth adds extra's to them.
func (t *Tx) Merge(keep, extra string, cueShift *float64, cues CueMode) error {
	if keep == extra {
		return errors.New("can't merge a track into itself")
	}
	var keepUUID string
	if err := t.tx.QueryRow(`SELECT IFNULL(UUID,'') FROM djmdContent WHERE ID = ?`, keep).Scan(&keepUUID); err != nil {
		return fmt.Errorf("track to keep (%s) isn't in the library", keep)
	}
	var n int
	if t.tx.QueryRow(`SELECT COUNT(*) FROM djmdContent WHERE ID = ?`, extra).Scan(&n); n == 0 {
		return fmt.Errorf("duplicate (%s) isn't in the library", extra)
	}

	if err := t.movePlaylistEntries(keep, extra); err != nil {
		return err
	}
	// Other references just follow the kept track.
	for _, tc := range [][2]string{
		{"djmdSongHistory", "ContentID"}, {"djmdSongMyTag", "ContentID"}, {"djmdSongTagList", "ContentID"},
		{"djmdSongHotCueBanklist", "ContentID"}, {"djmdSongSampler", "ContentID"}, {"djmdSongRelatedTracks", "ContentID"},
		{"djmdRecommendLike", "ContentID1"}, {"djmdRecommendLike", "ContentID2"},
	} {
		if !t.hasColumn(tc[0], tc[1]) {
			continue
		}
		if _, err := t.tx.Exec(`UPDATE "`+tc[0]+`" SET "`+tc[1]+`" = ?, rb_local_usn = ?, updated_at = ? WHERE "`+tc[1]+`" = ?`,
			keep, t.nextUSN(), t.now, extra); err != nil {
			return fmt.Errorf("%s: %w", tc[0], err)
		}
	}
	// A track tagged twice with the same My Tag after the move keeps one.
	if t.hasColumn("djmdSongMyTag", "MyTagID") {
		t.tx.Exec(`DELETE FROM djmdSongMyTag WHERE ContentID = ? AND rowid NOT IN
			(SELECT MIN(rowid) FROM djmdSongMyTag WHERE ContentID = ? GROUP BY MyTagID)`, keep, keep)
	}

	if cueShift != nil {
		var have int
		t.tx.QueryRow(`SELECT COUNT(*) FROM djmdCue WHERE ContentID = ? AND IFNULL(rb_local_deleted,0)=0`, keep).Scan(&have)
		switch {
		case have == 0:
			if err := t.copyCues(keep, keepUUID, extra, *cueShift, false); err != nil {
				return err
			}
		case cues == CuesOther:
			for _, tbl := range []string{"djmdCue", "contentCue"} {
				if _, err := t.tx.Exec(`DELETE FROM "`+tbl+`" WHERE ContentID = ?`, keep); err != nil {
					return fmt.Errorf("%s: %w", tbl, err)
				}
			}
			if err := t.copyCues(keep, keepUUID, extra, *cueShift, false); err != nil {
				return err
			}
		case cues == CuesBoth:
			if err := t.copyCues(keep, keepUUID, extra, *cueShift, true); err != nil {
				return err
			}
		}
	}

	// Combine what the DJ built up on the extra copy.
	if _, err := t.tx.Exec(`UPDATE djmdContent SET
		DJPlayCount = IFNULL(DJPlayCount,0) + IFNULL((SELECT DJPlayCount FROM djmdContent WHERE ID = ?),0),
		Rating = MAX(IFNULL(Rating,0), IFNULL((SELECT Rating FROM djmdContent WHERE ID = ?),0)),
		ColorID = CASE WHEN IFNULL(ColorID,'') IN ('','0') THEN (SELECT ColorID FROM djmdContent WHERE ID = ?) ELSE ColorID END,
		Commnt = CASE WHEN IFNULL(Commnt,'') = '' THEN (SELECT Commnt FROM djmdContent WHERE ID = ?) ELSE Commnt END,
		rb_local_usn = ?, updated_at = ?
		WHERE ID = ?`, extra, extra, extra, extra, t.nextUSN(), t.now, keep); err != nil {
		return err
	}

	// Data that belongs to the extra file itself goes with it.
	for _, tbl := range []string{"djmdCue", "contentCue", "contentFile", "djmdMixerParam", "djmdActiveCensor", "contentActiveCensor", "djmdSongPlaylist"} {
		if t.hasColumn(tbl, "ContentID") {
			if _, err := t.tx.Exec(`DELETE FROM "`+tbl+`" WHERE ContentID = ?`, extra); err != nil {
				return fmt.Errorf("%s: %w", tbl, err)
			}
		}
	}
	if _, err := t.tx.Exec(`DELETE FROM djmdContent WHERE ID = ?`, extra); err != nil {
		return err
	}
	t.nextUSN()
	return nil
}

// movePlaylistEntries swaps extra for keep in every playlist. Where keep is
// already in that playlist, extra's entry is dropped and the rest close up.
func (t *Tx) movePlaylistEntries(keep, extra string) error {
	rows, err := t.tx.Query(`SELECT ID, PlaylistID, IFNULL(TrackNo,0) FROM djmdSongPlaylist WHERE ContentID = ? AND IFNULL(rb_local_deleted,0)=0`, extra)
	if err != nil {
		return err
	}
	type entry struct {
		id, pl string
		no     int
	}
	var es []entry
	for rows.Next() {
		var e entry
		rows.Scan(&e.id, &e.pl, &e.no)
		es = append(es, e)
	}
	rows.Close()
	touched := map[string]bool{}
	for _, e := range es {
		var dup int
		t.tx.QueryRow(`SELECT COUNT(*) FROM djmdSongPlaylist WHERE PlaylistID = ? AND ContentID = ? AND IFNULL(rb_local_deleted,0)=0`, e.pl, keep).Scan(&dup)
		if dup > 0 {
			if _, err := t.tx.Exec(`DELETE FROM djmdSongPlaylist WHERE ID = ?`, e.id); err != nil {
				return err
			}
			if _, err := t.tx.Exec(`UPDATE djmdSongPlaylist SET TrackNo = TrackNo - 1, rb_local_usn = ?, updated_at = ?
				WHERE PlaylistID = ? AND TrackNo > ?`, t.nextUSN(), t.now, e.pl, e.no); err != nil {
				return err
			}
		} else if _, err := t.tx.Exec(`UPDATE djmdSongPlaylist SET ContentID = ?, rb_local_usn = ?, updated_at = ? WHERE ID = ?`,
			keep, t.nextUSN(), t.now, e.id); err != nil {
			return err
		}
		touched[e.pl] = true
	}
	for pl := range touched {
		t.tx.Exec(`UPDATE djmdPlaylist SET updated_at = ?, rb_local_usn = ? WHERE ID = ?`, t.now, t.nextUSN(), pl)
	}
	return nil
}

// CueMode says whose cues a merged track ends up with when both copies have
// some.
type CueMode string

const (
	CuesKeep  CueMode = "keep"  // the kept copy's (the default)
	CuesOther CueMode = "other" // the duplicate's, lined up to the kept file
	CuesBoth  CueMode = "both"  // both sets together
)

// cueKey identifies a cue for combining two sets.
type cueKey struct {
	kind int64
	in   int64
}

// copyCues duplicates extra's cue rows onto keep, shifted in time. Every
// column is copied (names, colours, loop lengths...); positions that are
// specific to the old file's MP3 framing are cleared for rekordbox to redo.
//
// With combine, keep's own cues stay: a cue of extra's that lands within
// 50 ms of one of keep's of the same sort is left out as the same cue, and a
// hot cue whose letter keep already uses moves to a free letter, or becomes
// a memory cue when all eight are taken.
func (t *Tx) copyCues(keep, keepUUID, extra string, shift float64, combine bool) error {
	rows, err := t.tx.Query(`SELECT * FROM djmdCue WHERE ContentID = ? AND IFNULL(rb_local_deleted,0)=0`, extra)
	if err != nil {
		return err
	}
	cols, _ := rows.Columns()
	var all []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			rows.Close()
			return err
		}
		m := map[string]any{}
		for i, c := range cols {
			m[c] = vals[i]
		}
		all = append(all, m)
	}
	rows.Close()

	var mine []cueKey
	used := map[int64]bool{}
	if combine {
		r, err := t.tx.Query(`SELECT IFNULL(Kind,0), IFNULL(InMsec,0) FROM djmdCue WHERE ContentID = ? AND IFNULL(rb_local_deleted,0)=0`, keep)
		if err != nil {
			return err
		}
		for r.Next() {
			var k cueKey
			r.Scan(&k.kind, &k.in)
			mine = append(mine, k)
			used[k.kind] = true
		}
		r.Close()
	}
	letters := hotCueKinds(mine, all)
	// added maps extra's cues (kind and position once shifted) to the kind
	// each got on keep, for the JSON copy below.
	added := map[cueKey]int64{}
	for _, m := range all {
		in := asInt(m["InMsec"]) + int64(math.Round(shift*1000))
		if in < 0 {
			if in < -5 {
				continue // lands before the start of the kept file
			}
			in = 0
		}
		orig := asInt(m["Kind"])
		kind := orig
		if combine {
			same := false
			for _, k := range mine {
				if (k.kind == 0) == (kind == 0) && abs64(k.in-in) <= 50 {
					same = true
				}
			}
			if same {
				continue
			}
			if kind != 0 && used[kind] {
				kind = 0 // memory cue, unless a letter is free
				for _, l := range letters {
					if !used[l] {
						kind = l
						break
					}
				}
			}
			if kind != 0 {
				used[kind] = true
			}
			m["Kind"] = kind
		}
		added[cueKey{orig, in}] = kind
		id, err := t.newID("djmdCue", "ID")
		if err != nil {
			return err
		}
		m["ID"], m["ContentID"], m["ContentUUID"], m["UUID"] = id, keep, keepUUID, uuid.NewString()
		m["InMsec"] = in
		m["InFrame"] = in * 150 / 1000
		if out := asInt(m["OutMsec"]); out > 0 {
			o := out + int64(math.Round(shift*1000))
			m["OutMsec"], m["OutFrame"] = o, o*150/1000
		}
		if v, ok := m["CueMicrosec"]; ok && v != nil {
			m["CueMicrosec"] = asInt(v) + int64(math.Round(shift*1e6))
		}
		for _, k := range []string{"InMpegFrame", "InMpegAbs", "OutMpegFrame", "OutMpegAbs"} {
			if _, ok := m[k]; ok {
				m[k] = 0
			}
		}
		for _, k := range []string{"InPointSeekInfo", "OutPointSeekInfo"} {
			if _, ok := m[k]; ok {
				m[k] = nil
			}
		}
		m["rb_local_usn"], m["usn"], m["created_at"], m["updated_at"] = t.nextUSN(), nil, t.now, t.now
		m["rb_local_synced"], m["rb_data_status"], m["rb_local_data_status"] = 0, 0, 0
		if err := t.insert("djmdCue", m); err != nil {
			return err
		}
	}
	// Newer rekordbox versions also keep a JSON copy of a track's cues.
	var cid string
	var js sql.NullString
	err = t.tx.QueryRow(`SELECT ID, Cues FROM contentCue WHERE ContentID = ? LIMIT 1`, extra).Scan(&cid, &js)
	if combine {
		if err == nil && js.Valid && js.String != "" {
			return t.combineCueJSON(keep, keepUUID, js.String, shift, added)
		}
		return nil
	}
	if err == nil && js.Valid && js.String != "" {
		shifted, err := shiftCueJSON(js.String, shift, keep, keepUUID)
		if err == nil {
			id, _ := t.newID("contentCue", "ID")
			t.tx.Exec(`DELETE FROM contentCue WHERE ContentID = ?`, keep)
			if err := t.insert("contentCue", map[string]any{"ID": id, "ContentID": keep, "Cues": shifted,
				"rb_cue_count": len(all), "UUID": uuid.NewString(), "rb_local_usn": t.nextUSN(), "created_at": t.now, "updated_at": t.now}); err != nil {
				return err
			}
		}
	}
	return nil
}

// hotCueKinds is the Kind numbers for hot cues A-H. Libraries use either
// 1-8 or 1,2,3,5,…,9 (skipping 4); whichever these cues use.
func hotCueKinds(mine []cueKey, theirs []map[string]any) []int64 {
	seen := map[int64]bool{}
	for _, k := range mine {
		seen[k.kind] = true
	}
	for _, m := range theirs {
		seen[asInt(m["Kind"])] = true
	}
	if seen[9] || (seen[5] && !seen[4]) {
		return []int64{1, 2, 3, 5, 6, 7, 8, 9}
	}
	return []int64{1, 2, 3, 4, 5, 6, 7, 8}
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// combineCueJSON adds the cues copied from extra to keep's JSON copy of its
// cues (newer rekordbox versions), when both copies have one and it's a
// list. Otherwise keep's JSON is left as it is.
func (t *Tx) combineCueJSON(keep, keepUUID, extraJSON string, shift float64, added map[cueKey]int64) error {
	var id string
	var mine sql.NullString
	if t.tx.QueryRow(`SELECT ID, Cues FROM contentCue WHERE ContentID = ? LIMIT 1`, keep).Scan(&id, &mine) != nil || !mine.Valid {
		return nil
	}
	shifted, err := shiftCueJSON(extraJSON, shift, keep, keepUUID)
	if err != nil {
		return nil
	}
	var ours, theirs []map[string]any
	if json.Unmarshal([]byte(mine.String), &ours) != nil || json.Unmarshal([]byte(shifted), &theirs) != nil {
		return nil
	}
	for _, c := range theirs {
		k, isNum := c["Kind"].(float64)
		in, isNum2 := c["InMsec"].(float64)
		if !isNum || !isNum2 {
			continue
		}
		kind, ok := added[cueKey{int64(k), int64(in)}]
		if !ok {
			continue // the same as one of keep's, or before the start
		}
		c["Kind"] = kind
		ours = append(ours, c)
	}
	b, err := json.Marshal(ours)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(`UPDATE contentCue SET Cues = ?, rb_cue_count = ?, rb_local_usn = ?, updated_at = ? WHERE ID = ?`,
		string(b), len(ours), t.nextUSN(), t.now, id)
	return err
}

// shiftCueJSON moves the time fields in contentCue's JSON the same way.
func shiftCueJSON(s string, shift float64, keep, keepUUID string) (string, error) {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return "", err
	}
	var walk func(any)
	walk = func(x any) {
		switch o := x.(type) {
		case []any:
			for _, e := range o {
				walk(e)
			}
		case map[string]any:
			for k, val := range o {
				f, isNum := val.(float64)
				switch {
				case isNum && (k == "InMsec" || (k == "OutMsec" && f > 0)):
					o[k] = math.Max(0, f+math.Round(shift*1000))
				case isNum && (k == "InFrame" || (k == "OutFrame" && f > 0)):
					o[k] = math.Max(0, f+math.Round(shift*150))
				case isNum && k == "CueMicrosec":
					o[k] = f + math.Round(shift*1e6)
				case isNum && strings.Contains(k, "Mpeg"):
					o[k] = 0
				case k == "ContentID":
					o[k] = keep
				case k == "ContentUUID":
					o[k] = keepUUID
				default:
					walk(val)
				}
			}
		}
	}
	walk(v)
	b, err := json.Marshal(v)
	return string(b), err
}

func asInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case []byte:
		var i int64
		fmt.Sscan(string(n), &i)
		return i
	case string:
		var i int64
		fmt.Sscan(n, &i)
		return i
	}
	return 0
}

func (t *Tx) hasColumn(table, col string) bool {
	rows, err := t.tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		rows.Scan(&n)
		if n == col {
			return true
		}
	}
	return false
}

// Backups lists the backup folders under root, newest first.
func Backups(root string) []string {
	var out []string
	es, _ := readDirSorted(root)
	for i := len(es) - 1; i >= 0; i-- {
		out = append(out, es[i])
	}
	return out
}

// Restore puts a backup (from Commit) back in place. rekordbox must be closed.
func Restore(loc *Location, backupDir string) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	if Running() {
		return ErrRunning
	}
	src := backupDir + "/master.db"
	if _, _, err := decryptCheck(src); err != nil {
		return fmt.Errorf("that backup can't be read: %w", err)
	}
	tmp := loc.DB + ".supersync-restore"
	if err := copyFile(src, tmp); err != nil {
		return err
	}
	for _, p := range []string{loc.DB + "-wal", loc.DB + "-shm"} {
		removeIfExists(p)
	}
	for _, name := range []string{"master.db-wal", "master.db-shm"} {
		if exists(backupDir + "/" + name) {
			copyFile(backupDir+"/"+name, loc.DB+strings.TrimPrefix(name, "master.db"))
		}
	}
	if err := rename(tmp, loc.DB); err != nil {
		return err
	}
	if exists(backupDir+"/masterPlaylists6.xml") && loc.PlaylistXML != "" {
		copyFile(backupDir+"/masterPlaylists6.xml", loc.PlaylistXML)
	}
	return nil
}
