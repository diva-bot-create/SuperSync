package app

import (
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"supersync/internal/rbdb"
)

// Background is SuperSync's timekeeper while the app is open: it re-syncs
// playlists on the chosen schedule and, if allowed, applies waiting changes
// once rekordbox has been closed for a little while. It never quits or
// restarts rekordbox itself.
func (a *App) Background(stop <-chan struct{}) {
	go a.fixStereo()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	var closedSince, lastBetter, lastDrops time.Time
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		a.Refresh()
		// Songs taken off synced playlists (in rekordbox or here).
		if time.Since(lastDrops) > time.Minute {
			lastDrops = time.Now()
			a.checkDropped("")
		}
		// Every few minutes, look for better copies the user has downloaded.
		if time.Since(lastBetter) > 5*time.Minute && a.Lib != nil {
			lastBetter = time.Now()
			go a.FindBetterCopies()
			// New files (from syncs) get fingerprinted too.
			if a.ListenPending() > 0 {
				go a.Listen(nil)
			}
		}

		if a.Cfg.AutoApply && len(a.PendingChanges()) > 0 && a.Src != nil {
			if rbdb.Running() {
				closedSince = time.Time{}
			} else if closedSince.IsZero() {
				closedSince = time.Now()
			} else if time.Since(closedSince) > 10*time.Second {
				if n, err := a.ApplyPending(); err != nil {
					log.Printf("auto-apply: %v", err)
				} else if n > 0 {
					log.Printf("auto-apply: added %d waiting change(s) to rekordbox", n)
					a.notify(Notice{Kind: "apply", Title: "Added to rekordbox",
						Body: fmt.Sprintf("%d waiting change%s went in when rekordbox closed.", n, plural(n))})
				}
			}
		}

		if a.Cfg.AutoSyncHours > 0 && a.Cfg.MusicDir != "" && len(a.Syncing()) == 0 {
			a.State.mu.Lock()
			due := time.Since(a.State.LastSync) >= time.Duration(a.Cfg.AutoSyncHours)*time.Hour
			a.State.mu.Unlock()
			if due {
				go a.SyncAll() // runs alongside, so auto-apply keeps working during a long sync
			}
		}
	}
}

var syncingAll atomic.Bool

// SyncAll re-syncs every playlist that came from SoundCloud or YouTube, one
// after another, and records when it ran. A second call while one is running
// does nothing.
func (a *App) SyncAll() {
	if !syncingAll.CompareAndSwap(false, true) {
		return
	}
	defer syncingAll.Store(false)
	a.State.mu.Lock()
	a.State.LastSync = time.Now()
	a.State.save()
	a.State.mu.Unlock()
	var changed []string
	for _, p := range a.SCPlaylists() {
		j, err := a.ImportPlaylist(p.URL)
		if err != nil {
			log.Printf("auto-sync %s: %v", p.Title, err)
			continue
		}
		for j.running() {
			time.Sleep(time.Second)
		}
		v := j.Snapshot()
		if v.Status == "error" {
			log.Printf("auto-sync %s: %s", p.Title, v.Message)
			continue
		}
		if s := syncSummary(v); s != "" {
			changed = append(changed, s)
		}
	}
	if len(changed) > 0 {
		a.notify(Notice{Kind: "sync", Title: "Synced your playlists", Body: strings.Join(changed, "\n")})
	}
}

// NextSync is when the schedule will next run (zero if it's off).
func (a *App) NextSync() time.Time {
	if a.Cfg.AutoSyncHours <= 0 {
		return time.Time{}
	}
	a.State.mu.Lock()
	defer a.State.mu.Unlock()
	next := a.State.LastSync.Add(time.Duration(a.Cfg.AutoSyncHours) * time.Hour)
	if now := time.Now(); next.Before(now) {
		return now // due now: starts within the next check
	}
	return next
}
