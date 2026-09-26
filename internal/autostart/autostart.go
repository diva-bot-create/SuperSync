// Package autostart opens SuperSync (in the background) when you log in.
package autostart

import (
	"os"
	"path/filepath"
	"strings"
)

// Arg is the flag SuperSync is started with at login: run in the background.
const Arg = "--background"

func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// appBundle is the .app the executable is inside, or "".
func appBundle(exe string) string {
	if i := strings.Index(exe, ".app/Contents/MacOS/"); i > 0 {
		return exe[:i+4]
	}
	return ""
}
