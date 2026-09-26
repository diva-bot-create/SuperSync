// Package web serves SuperSync's browser UI on localhost.
package web

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"supersync/internal/app"
	"supersync/internal/rbdb"
)

//go:embed static
var static embed.FS

const defaultPort = 47123

type server struct {
	app     *app.App
	token   string
	version string

	mu       sync.Mutex
	scanning *progress
	scanErr  string
}

type progress struct {
	Phase string `json:"phase"` // "scan" or "quality"
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

// Serve runs the UI until the process is killed.
func Serve(a *app.App, port int, openBrowser bool, version string) error {
	if port == 0 {
		port = defaultPort
	}
	s := &server{app: a, token: loadToken(), version: version}
	go a.Background(nil) // scheduled syncs, auto-apply when rekordbox closes

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		// Already running? Then just open it.
		if isRunning(port) {
			u := s.url(port)
			fmt.Println("SuperSync is already running:", u)
			if openBrowser {
				open(u)
			}
			return nil
		}
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return err
		}
	}
	port = ln.Addr().(*net.TCPAddr).Port
	u := s.url(port)

	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("supersync")) })
	s.routes(mux)

	fmt.Printf("SuperSync %s is running at\n\n  %s\n\nLeave this window open while you use it; close it to quit.\n", version, u)
	if openBrowser {
		go func() { time.Sleep(300 * time.Millisecond); open(u) }()
	}
	srv := &http.Server{Handler: s.guard(mux), ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(ln)
}

func (s *server) url(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d/#t=%s", port, s.token)
}

// guard blocks DNS-rebinding (Host check) and cross-site calls (token header).
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.Host)
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// <audio> and <img>-style loads can't set headers, so media GETs may carry the key as ?k=.
		key := r.Header.Get("X-SuperSync-Token")
		if key == "" && r.Method == http.MethodGet && (strings.HasPrefix(r.URL.Path, "/api/audio/") || strings.HasPrefix(r.URL.Path, "/api/waveform/")) {
			key = r.URL.Query().Get("k")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && key != s.token {
			http.Error(w, "bad token — reopen SuperSync from its window", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type dirListing struct {
	Path   string   `json:"path"`
	Parent string   `json:"parent"`
	Dirs   []string `json:"dirs"`
	Files  []string `json:"files,omitempty"`
	Roots  []string `json:"roots"`
}

func browse(dir, ext string) (*dirListing, error) {
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	dir = filepath.Clean(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	l := &dirListing{Path: dir, Parent: filepath.Dir(dir), Dirs: []string{}, Roots: roots()}
	if l.Parent == dir {
		l.Parent = ""
	}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(dir, n)); err == nil {
				isDir = st.IsDir()
			}
		}
		if isDir {
			l.Dirs = append(l.Dirs, n)
		} else if ext != "" && strings.EqualFold(filepath.Ext(n), ext) {
			l.Files = append(l.Files, n)
		}
	}
	sort.Slice(l.Dirs, func(i, j int) bool { return strings.ToLower(l.Dirs[i]) < strings.ToLower(l.Dirs[j]) })
	return l, nil
}

// roots are quick-jump locations: home, Music, and external drives.
func roots() []string {
	var r []string
	if h, err := os.UserHomeDir(); err == nil {
		r = append(r, h)
		if _, err := os.Stat(filepath.Join(h, "Music")); err == nil {
			r = append(r, filepath.Join(h, "Music"))
		}
	}
	switch runtime.GOOS {
	case "darwin":
		if es, err := os.ReadDir("/Volumes"); err == nil {
			for _, e := range es {
				r = append(r, "/Volumes/"+e.Name())
			}
		}
	case "windows":
		for c := 'A'; c <= 'Z'; c++ {
			d := string(c) + `:\`
			if _, err := os.Stat(d); err == nil {
				r = append(r, d)
			}
		}
	default:
		for _, base := range []string{"/media", "/mnt", "/run/media"} {
			if es, err := os.ReadDir(base); err == nil {
				for _, e := range es {
					r = append(r, filepath.Join(base, e.Name()))
				}
			}
		}
	}
	return r
}

func reveal(p string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", "-R", p).Start()
	case "windows":
		return exec.Command("explorer", "/select,", p).Start()
	default:
		return exec.Command("xdg-open", filepath.Dir(p)).Start()
	}
}

func open(u string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", u).Start()
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		exec.Command("xdg-open", u).Start()
	}
}

func isRunning(port int) bool {
	c := &http.Client{Timeout: time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/ping", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b := make([]byte, 9)
	n, _ := resp.Body.Read(b)
	return string(b[:n]) == "supersync"
}

// loadToken returns a per-install secret so the UI can be bookmarked.
func loadToken() string {
	dir, err := os.UserConfigDir()
	if err == nil {
		p := filepath.Join(dir, "SuperSync", "token")
		if b, err := os.ReadFile(p); err == nil && len(b) == 32 {
			return string(b)
		}
		t := newToken()
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(t), 0o600)
		return t
	}
	return newToken()
}

func newToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 32 {
			return '-'
		}
		return r
	}, s)
	if s = strings.TrimSpace(s); s == "" {
		s = "playlist"
	}
	return s
}

func disposition(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r > 126 || r == '"' {
			return '_'
		}
		return r
	}, name)
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + url.PathEscape(name)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(v); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func reply(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		body := map[string]string{"error": err.Error()}
		if errors.Is(err, rbdb.ErrRunning) {
			body["code"] = "rekordbox_running" // the page asks before quitting it
		}
		json.NewEncoder(w).Encode(body)
		return
	}
	json.NewEncoder(w).Encode(v)
}
