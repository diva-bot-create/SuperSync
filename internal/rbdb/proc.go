package rbdb

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrWouldNotQuit means rekordbox was asked to quit but is still open
// (usually because it's showing a dialog, e.g. about an unfinished export).
var ErrWouldNotQuit = errors.New("rekordbox didn't quit — check it for an open dialog, or close it yourself")

// appPath finds the running rekordbox's application: the .app bundle on
// macOS, the .exe on Windows.
func appPath() string {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("pgrep", "-x", "rekordbox").Output()
		if err != nil {
			return ""
		}
		pid := strings.Fields(string(out))[0]
		exe, err := exec.Command("ps", "-o", "comm=", "-p", pid).Output()
		if err != nil {
			return ""
		}
		p := strings.TrimSpace(string(exe))
		if i := strings.Index(p, ".app/"); i > 0 {
			return p[:i+4]
		}
		return ""
	case "windows":
		out, err := exec.Command("powershell", "-NoProfile", "-Command", "(Get-Process rekordbox -ErrorAction SilentlyContinue | Select-Object -First 1).Path").Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return ""
}

// Quit asks rekordbox to quit the way ⌘Q / closing its window does (never a
// force-kill, which could corrupt the library mid-write) and waits for it to
// close. It returns the application to pass to Relaunch.
func Quit(timeout time.Duration) (string, error) {
	if !Running() {
		return "", nil
	}
	app := appPath()
	switch runtime.GOOS {
	case "darwin":
		target := `application id "com.pioneerdj.rekordboxdj"`
		if app != "" {
			target = `application "` + strings.ReplaceAll(app, `"`, `\"`) + `"`
		}
		exec.Command("osascript", "-e", "tell "+target+" to quit").Run()
	case "windows":
		exec.Command("taskkill", "/IM", "rekordbox.exe").Run() // no /F: a normal close request
	default:
		return "", errors.New("restarting rekordbox isn't supported on this system")
	}
	deadline := time.Now().Add(timeout)
	for Running() {
		if time.Now().After(deadline) {
			return app, ErrWouldNotQuit
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Let it finish flushing the library to disk.
	time.Sleep(2 * time.Second)
	return app, nil
}

// Relaunch opens rekordbox again.
func Relaunch(app string) error {
	switch runtime.GOOS {
	case "darwin":
		if app != "" {
			return exec.Command("open", app).Run()
		}
		return exec.Command("open", "-b", "com.pioneerdj.rekordboxdj").Run()
	case "windows":
		if app == "" {
			return errors.New("don't know where rekordbox is installed; open it yourself")
		}
		cmd := exec.Command(app)
		cmd.Dir = filepath.Dir(app)
		return cmd.Start()
	}
	return nil
}
