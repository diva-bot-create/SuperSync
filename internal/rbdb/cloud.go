package rbdb

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

var contentsRe = regexp.MustCompile(`^/(contents_[^/]+)/`)

// findContentsFolders looks for Cloud Library Sync folders ("contents_…")
// on this computer: in the user's folders, rekordbox's data folder and
// other drives. It returns folder name -> the folder containing it.
func findContentsFolders(names map[string]bool) map[string]string {
	found := map[string]string{}
	if len(names) == 0 {
		return found
	}
	home, _ := os.UserHomeDir()
	type root struct {
		dir   string
		depth int
	}
	var roots []root
	if home != "" {
		roots = append(roots, root{home, 5})
		if runtime.GOOS == "windows" {
			roots = append(roots, root{filepath.Join(home, "AppData", "Roaming", "Pioneer"), 6}, root{filepath.Join(home, "AppData", "Local", "Pioneer"), 6})
		} else {
			roots = append(roots, root{filepath.Join(home, "Library", "Pioneer"), 6}, root{filepath.Join(home, "Library", "Application Support", "Pioneer"), 6})
		}
	}
	if runtime.GOOS == "windows" {
		for d := 'C'; d <= 'Z'; d++ {
			roots = append(roots, root{string(d) + `:\`, 3})
		}
	} else if runtime.GOOS == "darwin" {
		if vs, err := os.ReadDir("/Volumes"); err == nil {
			for _, v := range vs {
				roots = append(roots, root{filepath.Join("/Volumes", v.Name()), 3})
			}
		}
	}
	skip := map[string]bool{"node_modules": true, ".git": true, "Windows": true, "Program Files": true, "Program Files (x86)": true,
		"ProgramData": true, "$Recycle.Bin": true, "System Volume Information": true, "Caches": true, "Containers": true}
	for _, r := range roots {
		if len(found) == len(names) {
			break
		}
		base := strings.Count(filepath.Clean(r.dir), string(filepath.Separator))
		filepath.WalkDir(r.dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			n := d.Name()
			if p != r.dir && (skip[n] || strings.HasPrefix(n, ".")) {
				return filepath.SkipDir
			}
			if names[n] {
				if _, ok := found[n]; !ok {
					found[n] = filepath.Dir(p)
				}
				return filepath.SkipDir
			}
			if strings.Count(p, string(filepath.Separator))-base >= r.depth {
				return filepath.SkipDir
			}
			return nil
		})
	}
	return found
}

var (
	contentsMu    sync.Mutex
	contentsCache = map[string]string{} // folder name -> parent ("" = looked, not found)
)

// contentsRoot is where a Cloud Library Sync folder lives on this computer
// ("" if it isn't). The search runs once per folder name.
func contentsRoots(names map[string]bool) map[string]string {
	contentsMu.Lock()
	defer contentsMu.Unlock()
	todo := map[string]bool{}
	for n := range names {
		if _, ok := contentsCache[n]; !ok {
			todo[n] = true
		}
	}
	for n, dir := range findContentsFolders(todo) {
		contentsCache[n] = dir
	}
	out := map[string]string{}
	for n := range names {
		if _, ok := contentsCache[n]; !ok {
			contentsCache[n] = ""
		}
		if d := contentsCache[n]; d != "" {
			out[n] = d
		}
	}
	return out
}

// CloudInfo describes what rekordbox records about a Cloud Library Sync
// track and its cloud storage, for diagnostics. Secrets (credentials,
// tokens) are never included: only setting names, and values that are
// plainly folder paths.
func (d *DB) CloudInfo(contentID string) string {
	var b strings.Builder
	rows, err := d.SQL.Query(`SELECT IFNULL(Path,''), IFNULL(rb_local_path,''), IFNULL(rb_local_file_status,0), IFNULL(rb_temp_path,''), IFNULL(Size,0), IFNULL(rb_local_deleted,0)
		FROM contentFile WHERE ContentID = ?`, contentID)
	if err == nil {
		n := 0
		for rows.Next() {
			var path, local, temp string
			var status, size, deleted int64
			rows.Scan(&path, &local, &status, &temp, &size, &deleted)
			if strings.Contains(path, "/USBANLZ/") {
				continue // analysis files
			}
			n++
			fmt.Fprintf(&b, "  file record: path=%q local=%q status=%d temp=%q size=%d deleted=%d\n", path, local, status, temp, size, deleted)
		}
		rows.Close()
		if n == 0 {
			b.WriteString("  file record: none\n")
		}
	}
	pathy := func(v string) bool {
		return (len(v) > 2 && v[1] == ':') || strings.HasPrefix(v, "/") || strings.HasPrefix(v, `\\`)
	}
	for _, tbl := range []struct{ name, key string }{{"agentRegistry", "registry_id"}, {"cloudAgentRegistry", "ID"}} {
		rows, err := d.SQL.Query(`SELECT IFNULL(` + tbl.key + `,''), IFNULL(str_1,''), IFNULL(str_2,'') FROM ` + tbl.name)
		if err != nil {
			continue
		}
		var keys []string
		for rows.Next() {
			var k, s1, s2 string
			rows.Scan(&k, &s1, &s2)
			if strings.HasPrefix(k, "opLog.") {
				continue
			}
			v := ""
			for _, s := range []string{s1, s2} {
				if pathy(s) {
					v = s
				}
			}
			if v != "" {
				keys = append(keys, fmt.Sprintf("%s=%q", k, v))
			} else {
				keys = append(keys, k)
			}
		}
		rows.Close()
		fmt.Fprintf(&b, "  %s: %s\n", tbl.name, strings.Join(keys, ", "))
	}
	return b.String()
}
