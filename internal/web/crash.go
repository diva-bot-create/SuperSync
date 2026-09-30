package web

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"supersync/internal/app"
)

// If SuperSync crashes, Go writes why to crash.txt (a crash goes straight to
// the process's error output, which the log doesn't catch). The next start
// keeps it as last-crash.txt and offers the report.

func crashPath() string     { return filepath.Join(app.DataDir(), "crash.txt") }
func lastCrashPath() string { return filepath.Join(app.DataDir(), "last-crash.txt") }

// catchCrashes keeps a crash report from the last run, if there is one, and
// sets up this run's.
func catchCrashes() {
	os.MkdirAll(app.DataDir(), 0o755)
	if st, err := os.Stat(crashPath()); err == nil && st.Size() > 0 {
		os.Rename(crashPath(), lastCrashPath())
	}
	f, err := os.OpenFile(crashPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	debug.SetCrashOutput(f, debug.CrashOptions{})
}

// CrashInfo tells the page about a crash last time.
type CrashInfo struct {
	At      time.Time `json:"at"`
	Summary string    `json:"summary"` // the panic message
}

func lastCrash() *CrashInfo {
	st, err := os.Stat(lastCrashPath())
	if err != nil || st.Size() == 0 {
		return nil
	}
	c := &CrashInfo{At: st.ModTime()}
	if f, err := os.Open(lastCrashPath()); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" {
				c.Summary = l
				break
			}
		}
		f.Close()
	}
	return c
}

// dismissCrash: the user has seen (or sent) the crash report.
func dismissCrash() { os.Remove(lastCrashPath()) }

// report is everything useful for fixing a problem, with the user's home
// folder masked. It never includes the SoundCloud login.
func (s *server) report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "SuperSync %s on %s/%s (%s)\n", s.version, runtime.GOOS, runtime.GOARCH, runtime.Version())
	fmt.Fprintf(&b, "Window: %v, running since %s\n\n", s.inWindow, s.started.Format(time.RFC3339))
	b.WriteString("## Library\n")
	b.WriteString(s.diagnostics())
	if c, err := os.ReadFile(lastCrashPath()); err == nil && len(c) > 0 {
		b.WriteString("\n## Crash last time\n")
		b.Write(tail(c, 150))
	}
	if l, err := os.ReadFile(filepath.Join(app.DataDir(), "SuperSync.log")); err == nil && len(l) > 0 {
		b.WriteString("\n## Log (latest)\n")
		b.Write(tail(l, 120))
	}
	out := b.String()
	if home, err := os.UserHomeDir(); err == nil && len(home) > 3 {
		out = strings.ReplaceAll(out, home, "~")
	}
	if t := s.app.Cfg.SCToken; len(t) > 4 {
		out = strings.ReplaceAll(out, t, "[SoundCloud login removed]")
	}
	return out
}

// tail is the last n lines of b.
func tail(b []byte, n int) []byte {
	lines := strings.SplitAfter(string(b), "\n")
	if len(lines) > n {
		lines = append([]string{"…\n"}, lines[len(lines)-n:]...)
	}
	return []byte(strings.Join(lines, ""))
}

// issueURL opens a new GitHub issue with a short summary; the full report
// is too long for a link, so the user pastes it in.
func (s *server) issueURL(title string) string {
	body := fmt.Sprintf("**What happened?**\n\n\n**What did you expect?**\n\n\n---\nSuperSync %s on %s/%s\n\n<details><summary>Report</summary>\n\n```\n(paste the report SuperSync copied here)\n```\n</details>\n",
		s.version, runtime.GOOS, runtime.GOARCH)
	return "https://github.com/diva-bot-create/SuperSync/issues/new?title=" + url.QueryEscape(title) + "&body=" + url.QueryEscape(body)
}
