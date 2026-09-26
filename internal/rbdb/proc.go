package rbdb

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrWouldNotQuit means rekordbox is still running even after a force-quit.
var ErrWouldNotQuit = errors.New("rekordbox didn't quit, even when forced; close it yourself")

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

// Quit closes rekordbox and returns the application to pass to Relaunch. It
// first asks rekordbox to quit normally (as its Quit menu item does), so an
// idle rekordbox can finish writing its library; if it's still open after
// grace (e.g. showing a dialog), it force-quits it. Only call this after the
// user has confirmed.
func Quit(grace time.Duration) (string, error) {
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
	if !waitClosed(grace) {
		forceQuit()
		if !waitClosed(10 * time.Second) {
			return app, ErrWouldNotQuit
		}
	}
	// Let the file system settle before the library is read.
	time.Sleep(2 * time.Second)
	return app, nil
}

func waitClosed(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for Running() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
	return true
}

func forceQuit() {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("pkill", "-KILL", "-x", "rekordbox").Run()
	case "windows":
		exec.Command("taskkill", "/F", "/IM", "rekordbox.exe").Run()
	}
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
