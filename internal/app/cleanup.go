package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"supersync/internal/analyze"
	"supersync/internal/rbdb"
)

// CleanupGroup names the copy to keep and the duplicates to fold into it (paths).
type CleanupGroup struct {
	Keep   string   `json:"keep"`
	Extras []string `json:"extras"`
}

// CleanupResult reports what a clean-up did, per group.
type CleanupResult struct {
	Groups []CleanupReport `json:"groups"`
	Moved  int             `json:"moved"`
	Backup string          `json:"backup,omitempty"`
}

type CleanupReport struct {
	Keep       string  `json:"keep"`
	Merged     int     `json:"merged"` // library entries folded into the keeper
	CuesFrom   string  `json:"cuesFrom,omitempty"`
	CueShift   float64 `json:"cueShift"`
	CuesCopied int     `json:"cuesCopied"`
	Note       string  `json:"note,omitempty"`
}

// LastCleanup is what's needed to undo the most recent clean-up.
type LastCleanup struct {
	At      time.Time      `json:"at"`
	Backup  string         `json:"backup"`
	DBMod   time.Time      `json:"dbMod"` // library's modification time right after; undo refuses if it changed since
	Moves   []analyze.Move `json:"moves"`
	Summary string         `json:"summary"`
}

// CleanupDuplicates folds each group's extras into its keeper, in the library
// and on disk: playlists and history switch to the keeper, play counts add up,
// cues move over (lined up by audio) when the keeper has none, the extras
// leave the collection, and their files move to the duplicates folder.
func (a *App) CleanupDuplicates(groups []CleanupGroup) (*CleanupResult, error) {
	if a.Src == nil {
		return nil, ErrNoSource
	}
	if _, err := a.quarantineDir(); err != nil {
		return nil, err
	}
	res := &CleanupResult{}
	var ops []MergeOp
	var files []string
	for _, g := range groups {
		rep := CleanupReport{Keep: g.Keep}
		keep := a.Col.Lookup(g.Keep)
		if keep == nil {
			return nil, fmt.Errorf("%s isn't in the library", filepath.Base(g.Keep))
		}
		// Carry cues from the extra with the most, if the keeper has none.
		var src string
		best := 0
		for _, x := range g.Extras {
			if ct := a.Col.Lookup(x); ct != nil && ct.Cues > best {
				src, best = x, ct.Cues
			}
		}
		var shift *float64
		if keep.Cues == 0 && src != "" {
			plans, err := a.PlanCues([]analyze.Pair{{From: src, To: g.Keep}})
			switch {
			case err != nil:
				rep.Note = "Cues not carried over: " + err.Error()
			case plans[0].Usable():
				off := plans[0].Offset
				shift = &off
				rep.CuesFrom, rep.CueShift, rep.CuesCopied = src, off, plans[0].Cues-plans[0].Dropped
				if plans[0].Status == analyze.CueCheck {
					rep.Note = plans[0].Note
				}
			default:
				rep.Note = "Cues not carried over: " + plans[0].Note
			}
		}
		for _, x := range g.Extras {
			files = append(files, x)
			ct := a.Col.Lookup(x)
			if ct == nil {
				continue // only on disk, not in the library
			}
			op := MergeOp{Keep: keep.ID, Extra: ct.ID}
			if x == src {
				op.CueShift = shift
			}
			ops = append(ops, op)
			rep.Merged++
		}
		res.Groups = append(res.Groups, rep)
	}
	if len(ops) > 0 {
		backup, err := a.Src.Merge(ops)
		if err != nil {
			return nil, err
		}
		res.Backup = backup
		a.rebuild()
	}
	moves, err := a.Quarantine(files)
	res.Moved = len(moves)
	last := &LastCleanup{At: time.Now(), Backup: res.Backup, Moves: moves,
		Summary: fmt.Sprintf("%d duplicate%s cleaned up", len(files), plural(len(files)))}
	if info := a.Src.Info(); info.Kind == "rekordbox" {
		if st, err := os.Stat(info.Path); err == nil {
			last.DBMod = st.ModTime()
		}
	}
	a.State.mu.Lock()
	a.State.LastCleanup = last
	a.State.save()
	a.State.mu.Unlock()
	return res, err
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// UndoCleanup restores the library from the backup taken before the last
// clean-up and moves its files back. It refuses if the library has changed
// since (restoring would throw that work away).
func (a *App) UndoCleanup() error {
	a.State.mu.Lock()
	last := a.State.LastCleanup
	a.State.mu.Unlock()
	if last == nil {
		return errors.New("nothing to undo")
	}
	if info := a.Src.Info(); info.Kind == "rekordbox" && !last.DBMod.IsZero() {
		if st, err := os.Stat(info.Path); err == nil && st.ModTime().After(last.DBMod.Add(2*time.Second)) {
			return errors.New("the library has changed since the clean-up, so undoing it would lose those changes. Restore a backup from Settings if you really want to")
		}
	}
	if last.Backup != "" {
		if err := a.Src.Restore(last.Backup); err != nil {
			return err
		}
		a.rebuild()
	}
	if _, err := analyze.UndoMoves(last.Moves); err != nil {
		return err
	}
	a.State.mu.Lock()
	a.State.LastCleanup = nil
	a.State.save()
	a.State.mu.Unlock()
	return a.Scan(nil)
}

// WithRekordboxClosed runs fn; if rekordbox is open and restart is set (the
// user confirmed), it quits rekordbox first, forcing it if it doesn't close
// within a few seconds, and reopens it afterwards.
func (a *App) WithRekordboxClosed(restart bool, fn func() error) error {
	if a.Src == nil || a.Src.Info().Kind != "rekordbox" || !rbdb.Running() {
		return fn()
	}
	if !restart {
		return rbdb.ErrRunning
	}
	app, err := rbdb.Quit(8 * time.Second)
	if err != nil {
		return err
	}
	ferr := fn()
	if rerr := rbdb.Relaunch(app); rerr != nil && ferr == nil {
		ferr = fmt.Errorf("done, but rekordbox didn't reopen by itself: %w", rerr)
	}
	return ferr
}

// Last is the most recent clean-up that can still be undone, or nil.
func (s *State) Last() *LastCleanup {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastCleanup
}

// LastSyncTime is when the playlists were last synced on schedule.
func (s *State) LastSyncTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastSync
}
