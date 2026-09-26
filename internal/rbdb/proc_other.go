//go:build !windows

package rbdb

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// The app's process is "rekordbox"; the background agent is "rekordboxAgent" (harmless).
func running() bool { return exec.Command("pgrep", "-x", "rekordbox").Run() == nil }

// appPath finds the running rekordbox's .app bundle.
func appPath() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
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
}

func requestQuit(app string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("restarting rekordbox isn't supported on this system")
	}
	target := `application id "com.pioneerdj.rekordboxdj"`
	if app != "" {
		target = `application "` + strings.ReplaceAll(app, `"`, `\"`) + `"`
	}
	exec.Command("osascript", "-e", "tell "+target+" to quit").Run()
	return nil
}

func forceQuit() { exec.Command("pkill", "-KILL", "-x", "rekordbox").Run() }

// Relaunch opens rekordbox again.
func Relaunch(app string) error {
	if app != "" {
		return exec.Command("open", app).Run()
	}
	return exec.Command("open", "-b", "com.pioneerdj.rekordboxdj").Run()
}
