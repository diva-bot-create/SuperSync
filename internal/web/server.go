// Package web serves SuperSync's browser UI on localhost.
package web

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"supersync/internal/app"
	"supersync/internal/autostart"
	"supersync/internal/rbdb"
	"supersync/internal/update"
	"supersync/internal/window"
)

//go:embed static
var static embed.FS

const defaultPort = 47123

type server struct {
	app     *app.App
	token   string
	version string
	upd     *update.Updater
	srv     *http.Server
	ln      net.Listener
	port    int
	// restarting is set while an update restarts SuperSync.
	restarting atomic.Bool
	quitting   atomic.Bool
	inWindow   bool // showing its own app window (not a browser tab)

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
// Serve runs SuperSync's app. With showUI it opens the app window (or, where
// there's none, a browser tab); without it, it only serves (for testing).
// With background (opened at login) the window starts hidden.
func Serve(a *app.App, port int, showUI, background bool, version string) error {
	if port == 0 {
		port = defaultPort
	}
	s := &server{app: a, token: loadToken(), version: version}
	useWindow := showUI && window.Supported() && os.Getenv("SUPERSYNC_BROWSER") == ""

	// Restarted after an update: take the same port back (the old process is
	// just letting go of it). The window reopens; a browser tab reloads by itself.
	restarted := false
	if p, err := strconv.Atoi(os.Getenv(restartPortEnv)); err == nil && p > 0 {
		os.Unsetenv(restartPortEnv)
		port, restarted = p, true
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	for i := 0; err != nil && restarted && i < 60; i++ {
		time.Sleep(250 * time.Millisecond)
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	}
	if err != nil {
		// Already running? Then bring it forward.
		if isRunning(port) {
			u := s.url(port)
			fmt.Println("SuperSync is already running:", u)
			if showUI && !background && !s.focusRunning(port) {
				open(u)
			}
			return nil
		}
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return err
		}
	}
	port = ln.Addr().(*net.TCPAddr).Port
	s.port = port
	u := s.url(port)

	if useWindow {
		logToFile() // no terminal to print to
	}
	update.Cleanup()
	s.upd = update.New(version)
	if a.Cfg.OpenAtLogin {
		autostart.Set(true) // keep the login item pointing at this copy
	}
	go a.Background(nil) // scheduled syncs, auto-apply when rekordbox closes
	go s.updateLoop()

	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("supersync")) })
	s.routes(mux)
	srv := &http.Server{Handler: s.guard(mux), ReadHeaderTimeout: 10 * time.Second}
	s.srv, s.ln = srv, ln

	fmt.Printf("SuperSync %s is running at\n\n  %s\n\n", version, u)
	if useWindow {
		s.inWindow = true
		go srv.Serve(ln)
		// The window runs on the main thread until SuperSync quits.
		if window.Run(window.Options{URL: u, Title: "SuperSync", Width: 1360, Height: 880,
			DataDir: filepath.Join(app.DataDir(), "WebView2"), OnClose: s.shutdown,
			OnSyncAll: a.SyncAll, KeepRunning: !a.Cfg.QuitOnClose, Hidden: background}) {
			if s.restarting.Load() {
				select {} // an update is restarting SuperSync
			}
			return nil
		}
		s.inWindow = false // no web view on this computer: use the browser
		fmt.Println("No app window available; opening your browser instead.")
		if !restarted && !background {
			open(u)
		}
		select {}
	}
	if showUI && !restarted && !background {
		fmt.Println("Leave this window open while you use it; close it to quit.")
		go func() { time.Sleep(300 * time.Millisecond); open(u) }()
	}
	err = srv.Serve(ln)
	if s.restarting.Load() || s.quitting.Load() {
		select {} // restarting or quitting: that goroutine exits the process
	}
	return err
}

// shutdown runs as SuperSync quits: it waits for a library write in
// progress to finish, and keeps new ones from starting.
func (s *server) shutdown() {
	if s.quitting.Swap(true) {
		return
	}
	rbdb.HoldWrites() // never released: the process is ending
}

// quit closes SuperSync from the page (its Quit button or menu).
func (s *server) quit() {
	if s.inWindow {
		window.Close() // the window's closing runs shutdown
		return
	}
	go func() {
		time.Sleep(300 * time.Millisecond) // let the reply reach the page
		s.shutdown()
		os.Exit(0)
	}()
}

// focusRunning asks an already-running SuperSync to bring its window forward.
func (s *server) focusRunning(port int) bool {
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/focus", port), strings.NewReader("{}"))
	req.Header.Set("X-SuperSync-Token", s.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out struct {
		Window bool `json:"window"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode == 200 && out.Window
}

// logToFile sends output to SuperSync.log in the data folder, since an app
// started from its icon has no terminal. The previous run's log is kept.
func logToFile() {
	dir := app.DataDir()
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, "SuperSync.log")
	os.Rename(p, p+".1")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	os.Stdout, os.Stderr = f, f
	log.SetOutput(f)
}

const restartPortEnv = "SUPERSYNC_RESTART_PORT"

// updateLoop checks GitHub for a new version shortly after starting and then
// every 6 hours, downloading it in the background unless turned off.
func (s *server) updateLoop() {
	time.Sleep(5 * time.Second)
	for {
		if !s.app.Cfg.NoAutoUpdate {
			s.upd.Check(context.Background())
		}
		time.Sleep(6 * time.Hour)
	}
}

// installUpdate swaps in the downloaded version and restarts into it, after
// any library write in progress has finished.
func (s *server) installUpdate() error {
	st := s.upd.Status()
	if st.State != "ready" || st.Download == "" {
		return errors.New("no update is ready")
	}
	if len(s.app.Syncing()) > 0 {
		return errors.New("a playlist is still syncing; update once it's finished")
	}
	release := rbdb.HoldWrites()
	if err := update.Install(st.Download); err != nil {
		release()
		return fmt.Errorf("couldn't install the update: %w", err)
	}
	go func() {
		time.Sleep(400 * time.Millisecond) // let the reply reach the page
		s.restarting.Store(true)
		s.ln.Close() // free the port for the new version
		if err := update.Restart(fmt.Sprintf("%s=%d", restartPortEnv, s.port)); err != nil {
			fmt.Println("The update is installed; close this window and open SuperSync again.", err)
			os.Exit(0)
		}
	}()
	return nil
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
