package autostart

import (
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const label = "com.queenout.supersync"

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

func Supported() bool { return true }

// Enabled reports whether SuperSync opens at login.
func Enabled() bool {
	_, err := os.Stat(plistPath())
	return err == nil
}

// Set turns opening at login on or off. It's also called at every start
// while on, so the login item follows the app if it's moved.
func Set(on bool) error {
	p := plistPath()
	if !on {
		exec.Command("launchctl", "bootout", "gui/"+uid(), p).Run()
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	exe, err := executable()
	if err != nil {
		return err
	}
	args := []string{exe, Arg}
	if b := appBundle(exe); b != "" {
		// Through Launch Services, so it starts as the app (in the background).
		args = []string{"/usr/bin/open", "-g", "-a", b, "--args", Arg}
	}
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>` + label + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range args {
		sb.WriteString("\t\t<string>")
		xml.EscapeText(&sb, []byte(a))
		sb.WriteString("</string>\n")
	}
	sb.WriteString(`	</array>
	<key>RunAtLoad</key><true/>
	<key>LimitLoadToSessionType</key><string>Aqua</string>
	<key>ProcessType</key><string>Interactive</string>
</dict>
</plist>
`)
	if old, err := os.ReadFile(p); err == nil && string(old) == sb.String() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(sb.String()), 0o644)
}

func uid() string {
	out, _ := exec.Command("id", "-u").Output()
	return strings.TrimSpace(string(out))
}
