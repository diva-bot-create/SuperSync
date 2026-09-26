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
	if bundle := AppBundle(); bundle != "" && zipHasBundle(zr) {
		return stageBundle(ctx, zr, bundle, rel.Tag)
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

// AppBundle is the SuperSync.app the running executable is inside, or "".
func AppBundle() string {
	exe, err := Executable()
	if err != nil {
		return ""
	}
	if i := strings.Index(exe, ".app/Contents/MacOS/"); i > 0 {
		return exe[:i+4]
	}
	return ""
}

func zipHasBundle(zr *zip.Reader) bool {
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "SuperSync.app/Contents/MacOS/") {
			return true
		}
	}
	return false
}

// stageBundle unpacks the new SuperSync.app into a hidden folder beside the
// installed one and checks it runs. It returns the new bundle's path.
func stageBundle(ctx context.Context, zr *zip.Reader, bundle, tag string) (string, error) {
	dir := filepath.Join(filepath.Dir(bundle), ".SuperSync-update-"+tag)
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("can't write next to SuperSync (%v); move SuperSync to your Applications folder, or download the update yourself", err)
	}
	fail := func(err error) (string, error) { os.RemoveAll(dir); return "", err }
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "SuperSync.app/") {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(p, dir+string(filepath.Separator)) {
			return fail(errors.New("the download has an unexpected layout"))
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(p, 0o755); err != nil {
				return fail(err)
			}
			continue
		case mode&os.ModeSymlink != 0:
			rc, err := f.Open()
			if err != nil {
				return fail(err)
			}
			target, _ := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.Symlink(string(target), p); err != nil {
				return fail(err)
			}
			continue
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		rc, err := f.Open()
		if err != nil {
			return fail(err)
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm()|0o600)
		if err == nil {
			_, err = io.Copy(out, io.LimitReader(rc, 300<<20))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
		}
		rc.Close()
		if err != nil {
			return fail(err)
		}
	}
	app := filepath.Join(dir, "SuperSync.app")
	if err := verifyRuns(ctx, filepath.Join(app, "Contents", "MacOS", "SuperSync"), tag); err != nil {
		return fail(err)
	}
	return app, nil
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
	if strings.HasSuffix(staged, ".app") {
		// Swap the whole app: the old one goes into the update folder, which
		// is removed on the next start.
		bundle := AppBundle()
		if bundle == "" {
			return errors.New("SuperSync isn't running from its app")
		}
		old := filepath.Join(filepath.Dir(staged), "old.app")
		if err := os.Rename(bundle, old); err != nil {
			return err
		}
		if err := os.Rename(staged, bundle); err != nil {
			os.Rename(old, bundle) // put it back
			return err
		}
		return nil
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
	dir := filepath.Dir(exe)
	if b := AppBundle(); b != "" {
		dir = filepath.Dir(b)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".SuperSync-update-*"))
	for _, m := range matches {
		os.RemoveAll(m)
	}
}
