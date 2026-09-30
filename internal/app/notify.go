package app

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Notice is something SuperSync did by itself that the user should hear
// about: a background sync that added songs, a better copy swapped in,
// waiting changes added once rekordbox closed.
type Notice struct {
	ID    int64     `json:"id"`
	At    time.Time `json:"at"`
	Kind  string    `json:"kind"` // sync, swap, apply
	Title string    `json:"title"`
	Body  string    `json:"body,omitempty"`
}

var notices struct {
	sync.Mutex
	seq  int64
	list []Notice
}

// OnNotice, if set, is called for each new notice (the app shows a system
// notification when its window isn't in front).
var OnNotice func(Notice)

func (a *App) notify(n Notice) {
	notices.Lock()
	notices.seq++
	n.ID, n.At = notices.seq, time.Now()
	notices.list = append(notices.list, n)
	if len(notices.list) > 20 {
		notices.list = notices.list[len(notices.list)-20:]
	}
	notices.Unlock()
	if OnNotice != nil {
		go OnNotice(n)
	}
}

// Notices returns the recent notices, oldest first.
func (a *App) Notices() []Notice {
	notices.Lock()
	defer notices.Unlock()
	return append([]Notice(nil), notices.list...)
}

// syncSummary describes what a finished background sync of one playlist
// changed, or "" if nothing.
func syncSummary(v JobView) string {
	added, failed := 0, 0
	for _, s := range v.Steps {
		switch s.State {
		case "downloaded":
			added++
		case "failed":
			failed++
		}
	}
	var parts []string
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d new", added))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if v.Removed > 0 {
		parts = append(parts, fmt.Sprintf("%d taken off", v.Removed))
	}
	if v.Status == "waiting" && added > 0 {
		parts = append(parts, "waiting for rekordbox to close")
	}
	if len(parts) == 0 {
		return ""
	}
	return v.Title + ": " + strings.Join(parts, ", ")
}
