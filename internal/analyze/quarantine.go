package analyze

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"supersync/internal/library"
)

// Move records one file moved into quarantine, so it can be put back.
type Move struct {
	Time time.Time `json:"time"`
	From string    `json:"from"`
	To   string    `json:"to"`
}

func logPath(qdir string) string {
	return filepath.Join(qdir, "moves.jsonl")
}

// QuarantineDir is the duplicates folder inside a music folder.
func QuarantineDir(musicDir string) string { return filepath.Join(musicDir, library.QuarantineDir) }

// Quarantine moves each file into qdir, keeping its path relative to root
// (files outside root keep their full path, minus any drive letter), and logs
// every move so Undo can put it back.
func Quarantine(qdir, root string, paths []string) ([]Move, error) {
	if err := os.MkdirAll(qdir, 0o755); err != nil {
		return nil, err
	}
	lf, err := os.OpenFile(logPath(qdir), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer lf.Close()
	var moves []Move
	for _, p := range paths {
		rel, err := filepath.Rel(root, p)
		if root == "" || err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			rel = strings.TrimPrefix(p, filepath.VolumeName(p))
		}
		dst := uniquePath(filepath.Join(qdir, rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return moves, err
		}
		if err := moveFile(p, dst); err != nil {
			return moves, fmt.Errorf("moving %s: %w", rel, err)
		}
		m := Move{Time: time.Now(), From: p, To: dst}
		moves = append(moves, m)
		b, _ := json.Marshal(m)
		lf.Write(append(b, '\n'))
	}
	return moves, nil
}

// Undo moves every quarantined file back to where it came from (skipping any
// whose original location is occupied again) and returns the number restored.
func Undo(qdir string) (int, error) {
	f, err := os.Open(logPath(qdir))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	var moves []Move
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m Move
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			moves = append(moves, m)
		}
	}
	f.Close()

	restored := 0
	var keep []Move
	for i := len(moves) - 1; i >= 0; i-- {
		m := moves[i]
		if _, err := os.Stat(m.To); err != nil {
			continue // already gone (restored or deleted by hand)
		}
		if _, err := os.Stat(m.From); err == nil {
			keep = append(keep, m)
			continue
		}
		os.MkdirAll(filepath.Dir(m.From), 0o755)
		if err := moveFile(m.To, m.From); err != nil {
			keep = append(keep, m)
			continue
		}
		restored++
	}
	lf, err := os.Create(logPath(qdir))
	if err != nil {
		return restored, err
	}
	defer lf.Close()
	for i := len(keep) - 1; i >= 0; i-- {
		b, _ := json.Marshal(keep[i])
		lf.Write(append(b, '\n'))
	}
	return restored, nil
}

func uniquePath(p string) string {
	if _, err := os.Stat(p); err != nil {
		return p
	}
	ext := filepath.Ext(p)
	base := p[:len(p)-len(ext)]
	for i := 2; ; i++ {
		c := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(c); err != nil {
			return c
		}
	}
}

// moveFile renames, falling back to copy+delete across filesystems.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	st, _ := in.Stat()
	out, err := os.Create(dst)
	if err != nil {
		in.Close()
		return err
	}
	_, err = io.Copy(out, in)
	in.Close()
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return err
	}
	if st != nil {
		os.Chtimes(dst, st.ModTime(), st.ModTime())
	}
	return os.Remove(src)
}

// UndoMoves puts back specific moved files (e.g. from one clean-up) and
// drops them from the undo log.
func UndoMoves(moves []Move) (int, error) {
	restored := 0
	for i := len(moves) - 1; i >= 0; i-- {
		m := moves[i]
		if _, err := os.Stat(m.To); err != nil {
			continue
		}
		if _, err := os.Stat(m.From); err == nil {
			continue // something's there again; leave both alone
		}
		os.MkdirAll(filepath.Dir(m.From), 0o755)
		if err := moveFile(m.To, m.From); err != nil {
			return restored, err
		}
		restored++
	}
	for _, m := range moves {
		if lp := logPathFor(m.To); m.To != "" && lp != "" {
			pruneLog(filepath.Dir(lp), moves)
			break
		}
	}
	return restored, nil
}

// logPathFor walks up from a quarantined file to its moves.jsonl.
func logPathFor(p string) string {
	for d := filepath.Dir(p); d != filepath.Dir(d); d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "moves.jsonl")); err == nil {
			return filepath.Join(d, "moves.jsonl")
		}
	}
	return ""
}

func pruneLog(qdir string, done []Move) {
	b, err := os.ReadFile(logPath(qdir))
	if err != nil {
		return
	}
	gone := map[string]bool{}
	for _, m := range done {
		gone[m.To] = true
	}
	var keep []byte
	for _, line := range bytes.Split(b, []byte("\n")) {
		var m Move
		if len(line) == 0 || json.Unmarshal(line, &m) != nil || gone[m.To] {
			continue
		}
		keep = append(append(keep, line...), '\n')
	}
	os.WriteFile(logPath(qdir), keep, 0o644)
}
