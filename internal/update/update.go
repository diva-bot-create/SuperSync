// Package update keeps SuperSync current from its GitHub releases: it finds
// the latest release, downloads this platform's zip, checks it, stages the
// new executable beside the running one, and swaps it in on restart.
package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Repo is where releases are published.
const Repo = "diva-bot-create/SuperSync"

// Status is what the app shows about updates.
type Status struct {
	Current  string    `json:"current"`
	Latest   string    `json:"latest,omitempty"`
	State    string    `json:"state"` // "idle", "checking", "downloading", "ready", "current", "error", "unsupported"
	Message  string    `json:"message,omitempty"`
	Notes    string    `json:"notes,omitempty"`
	URL      string    `json:"url,omitempty"` // the release page
	Checked  time.Time `json:"checked,omitzero"`
	Download string    `json:"-"` // staged executable, when State is "ready"
}

type Updater struct {
	mu     sync.Mutex
	st     Status
	client *http.Client
}

func New(current string) *Updater {
	u := &Updater{st: Status{Current: current, State: "idle"}, client: &http.Client{Timeout: 5 * time.Minute}}
	if AssetName() == "" {
		u.st.State = "unsupported"
	}
	return u
}

func (u *Updater) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.st
}

func (u *Updater) set(f func(*Status)) {
	u.mu.Lock()
	f(&u.st)
	u.mu.Unlock()
}

// AssetName is the release file for this computer ("" if none is published).
// Windows on ARM runs the x64 build.
func AssetName() string {
	switch runtime.GOOS {
	case "darwin":
		return "SuperSync-mac.zip"
	case "windows":
		return "SuperSync-windows.zip"
	}
	return ""
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "SuperSync.exe"
	}
	return "SuperSync"
}

// Newer reports whether version a is later than b ("v0.1.1" > "v0.1").
// Anything that isn't a version (a "dev" build) is never updated.
func Newer(a, b string) bool {
	pa, oka := parse(a)
	pb, okb := parse(b)
	if !oka || !okb {
		return false
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func parse(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return nil, false
	}
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

type release struct {
	Tag    string `json:"tag_name"`
	Body   string `json:"body"`
	URL    string `json:"html_url"`
	Draft  bool   `json:"draft"`
	Pre    bool   `json:"prerelease"`
	Assets []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Size   int64  `json:"size"`
		Digest string `json:"digest"` // "sha256:<hex>"
	} `json:"assets"`
}

// Check looks for a newer release and, if there is one, downloads and stages
// it. It's safe to call repeatedly; a ready update isn't downloaded twice.
func (u *Updater) Check(ctx context.Context) {
	st := u.Status()
	if st.State == "unsupported" || st.State == "checking" || st.State == "downloading" {
		return
	}
	if _, ok := parse(st.Current); !ok {
		u.set(func(s *Status) {
			s.State, s.Message = "current", "This is a development build; it doesn't update itself."
		})
		return
	}
	u.set(func(s *Status) { s.State, s.Message = "checking", "" })
	rel, err := u.latest(ctx)
	if err != nil {
		u.set(func(s *Status) { s.State, s.Message, s.Checked = "error", err.Error(), time.Now() })
		return
	}
	u.set(func(s *Status) { s.Latest, s.Notes, s.URL, s.Checked = rel.Tag, rel.Body, rel.URL, time.Now() })
	if !Newer(rel.Tag, st.Current) {
		u.set(func(s *Status) { s.State = "current" })
		return
	}
	if st.State == "ready" && st.Latest == rel.Tag {
		u.set(func(s *Status) { s.State = "ready" })
		return
	}
	u.set(func(s *Status) { s.State = "downloading" })
	path, err := u.stage(ctx, rel)
	if err != nil {
		u.set(func(s *Status) {
			s.State, s.Message = "error", fmt.Sprintf("Couldn't download %s: %v. You can download it from the releases page instead.", rel.Tag, err)
		})
		return
	}
	u.set(func(s *Status) { s.State, s.Download, s.Message = "ready", path, "" })
}

func (u *Updater) latest(ctx context.Context) (*release, error) {
	api := "https://api.github.com/repos/" + Repo + "/releases/latest"
	if v := os.Getenv("SUPERSYNC_UPDATE_API"); v != "" {
		api = v // for testing against a local release
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", api, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "SuperSync-updater")
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, errors.New("couldn't reach GitHub")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	if rel.Draft || rel.Pre || rel.Tag == "" {
		return nil, errors.New("no published release")
	}
	return &rel, nil
}

// stage downloads the release's zip for this computer, checks its checksum,
// extracts the executable next to the running one, and makes sure it starts
// and reports the expected version.
func (u *Updater) stage(ctx context.Context, rel *release) (string, error) {
	name := AssetName()
	var url, digest string
	var size int64
	for _, a := range rel.Assets {
		if a.Name == name {
			url, digest, size = a.URL, a.Digest, a.Size
		}
	}
	if url == "" {
		return "", fmt.Errorf("the release has no %s", name)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", "SuperSync-updater")
	resp, err := u.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 200<<20))
	if err != nil {
		return "", err
	}
	if size > 0 && int64(len(data)) != size {
		return "", errors.New("the download was incomplete")
	}
	if want, ok := strings.CutPrefix(digest, "sha256:"); ok {
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != strings.ToLower(want) {
			return "", errors.New("the download didn't match GitHub's checksum")
		}
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var bin []byte
	for _, f := range zr.File {
		if filepath.Base(f.Name) == exeName() && !f.FileInfo().IsDir() {
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			bin, err = io.ReadAll(io.LimitReader(rc, 200<<20))
			rc.Close()
			if err != nil {
				return "", err
			}
		}
	}
	if len(bin) == 0 {
		return "", fmt.Errorf("%s isn't in the download", exeName())
	}
	exe, err := Executable()
	if err != nil {
		return "", err
	}
	// Beside the running file, so the swap is a rename on the same disk.
	staged := filepath.Join(filepath.Dir(exe), ".SuperSync-update-"+rel.Tag+filepath.Ext(exe))
	if err := os.WriteFile(staged, bin, 0o755); err != nil {
		return "", fmt.Errorf("can't write next to SuperSync (%v); move SuperSync to a folder you own, or download the update yourself", err)
	}
	if err := verifyRuns(ctx, staged, rel.Tag); err != nil {
		os.Remove(staged)
		return "", err
	}
	return staged, nil
}

// verifyRuns starts the new executable with "help" and checks it reports the
// release's version, so a broken or wrong-platform file never replaces a
// working one.
func verifyRuns(ctx context.Context, path, tag string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "help").CombinedOutput()
	if err != nil && len(out) == 0 {
		return fmt.Errorf("the new version didn't start: %v", err)
	}
	if !strings.Contains(string(out), "SuperSync "+tag+" ") {
		return errors.New("the new version didn't report the expected version")
	}
	return nil
}

// Executable is the running SuperSync file, with symlinks resolved.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// Install swaps the staged executable in for the running one. On Windows a
// running .exe can't be replaced but can be renamed, so the old one is moved
// aside (and deleted on the next start by Cleanup).
func Install(staged string) error {
	exe, err := Executable()
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.Rename(staged, exe); err != nil {
			os.Rename(old, exe) // put it back
			return err
		}
		return nil
	}
	return os.Rename(staged, exe)
}

// Cleanup removes what a previous update left behind: the old Windows
// executable and any staged file that was never installed.
func Cleanup() {
	exe, err := Executable()
	if err != nil {
		return
	}
	os.Remove(exe + ".old")
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".SuperSync-update-*"))
	for _, m := range matches {
		os.Remove(m)
	}
}
