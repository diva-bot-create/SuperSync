package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"supersync/internal/soundcloud"
)

// Two-way removals: a song taken off a synced playlist here (in rekordbox,
// or in SuperSync) stays off at later syncs, and, for the user's own
// SoundCloud playlists with their login set, comes off the SoundCloud
// playlist too. A lot at once (a cleared playlist, say) waits for the user's
// OK first. Each change to SoundCloud is in the history, where Undo puts the
// songs back.

var scMeCache struct {
	sync.Mutex
	token string
	id    int64
}

// scMe is the SoundCloud account of the login token, or 0.
func (a *App) scMe() int64 {
	token := strings.TrimSpace(a.Cfg.SCToken)
	if token == "" {
		return 0
	}
	scMeCache.Lock()
	defer scMeCache.Unlock()
	if scMeCache.token == token && scMeCache.id != 0 {
		return scMeCache.id
	}
	id, _, err := a.SC.Me(token)
	if err != nil {
		log.Printf("SoundCloud login: %v", err)
		return 0
	}
	scMeCache.token, scMeCache.id = token, id
	return id
}

// askOver: taking more than this share of a playlist off SoundCloud at
// once waits for the user's OK.
func tooMany(n, of int) bool { return n > 3 && n*3 > of }

var checkingDrops sync.Mutex

// checkDropped finds songs taken off synced playlists (link, or all when
// "") since SuperSync last put them there, and deals with them.
func (a *App) checkDropped(link string) {
	if a.Src == nil {
		return
	}
	checkingDrops.Lock()
	defer checkingDrops.Unlock()
	syncing := map[string]bool{}
	for _, u := range a.Syncing() {
		syncing[strings.ToLower(u)] = true
	}
	for _, sp := range a.SCPlaylists() {
		if link != "" && !strings.EqualFold(strings.TrimRight(sp.URL, "/"), strings.TrimRight(link, "/")) {
			continue
		}
		a.State.mu.Lock()
		pid, skip := sp.PlaylistID, sp.PlaylistID == "" || sp.Pending || len(sp.Applied) == 0
		a.State.mu.Unlock()
		// While a sync or a waiting change is putting songs in, the playlist
		// is still catching up; and a deleted playlist isn't "everything
		// taken off".
		if skip || (link == "" && syncing[strings.ToLower(sp.URL)]) || a.playlistName(pid) == "" {
			continue
		}
		in := map[string]bool{}
		for _, id := range a.Src.PlaylistTrackIDs(pid) {
			in[id] = true
		}
		before := a.syncSnapshot()
		a.State.mu.Lock()
		applied := map[string]bool{}
		for _, id := range sp.Applied {
			applied[id] = true
		}
		var gone []int64
		var goneTracks []string
		for _, e := range sp.Entries {
			if e.SC != nil && e.TrackID != "" && e.Status != "dropped" && applied[e.TrackID] && !in[e.TrackID] {
				e.Status, e.Note = "dropped", "Taken off the playlist here"
				gone = append(gone, e.SC.ID)
				goneTracks = append(goneTracks, e.TrackID)
			}
		}
		if len(gone) == 0 {
			a.State.mu.Unlock()
			continue
		}
		sp.Applied = dropStrings(sp.Applied, goneTracks)
		push := sp.Source != "youtube" && sp.Mine && sp.SCID != 0 && !a.Cfg.NoTwoWay && a.Cfg.SCToken != ""
		ask := push && tooMany(len(gone)+len(sp.AskDrop), len(sp.Entries))
		switch {
		case ask:
			sp.AskDrop = append(sp.AskDrop, gone...)
			sp.AskNote = ""
		default:
			sp.Dropped = append(sp.Dropped, gone...)
		}
		title := sp.Title
		a.State.save()
		a.State.mu.Unlock()
		log.Printf("%s: %d song(s) taken off the playlist here", title, len(gone))
		switch {
		case ask:
			a.notify(Notice{Kind: "drop", Title: "Take them off SoundCloud too?",
				Body: fmt.Sprintf("You took %d songs off “%s”. Open it in SuperSync to choose.", len(gone), title)})
		case push:
			a.pushDrops(sp, gone, before)
		}
	}
}

// pushDrops takes songs off the user's SoundCloud playlist. If SoundCloud
// won't, they wait for the user to try again (AskDrop, with why).
func (a *App) pushDrops(sp *SCPlaylist, ids []int64, before json.RawMessage) error {
	a.State.mu.Lock()
	scID, title, link := sp.SCID, sp.Title, sp.URL
	a.State.mu.Unlock()
	was, err := a.SC.RemoveFromPlaylist(a.Cfg.SCToken, scID, ids)
	a.State.mu.Lock()
	defer a.State.mu.Unlock()
	if err != nil {
		if errors.Is(err, soundcloud.ErrNotMine) {
			sp.Mine = false // it stays taken off here only
			a.State.save()
			return err
		}
		sp.Dropped = dropIDs(sp.Dropped, ids)
		sp.AskDrop = append(dropIDs(sp.AskDrop, ids), ids...)
		sp.AskNote = "Couldn't take them off SoundCloud: " + err.Error()
		a.State.save()
		go a.notify(Notice{Kind: "drop", Title: "Couldn't change your SoundCloud playlist", Body: title + ": " + err.Error()})
		return err
	}
	a.State.save()
	go a.recordSC(fmt.Sprintf("Took %d song%s off “%s” on SoundCloud", len(ids), plural(len(ids)), title), before,
		&SCRestore{URL: link, PlaylistID: scID, Tracks: was})
	return nil
}

// ConfirmDrops answers "take them off SoundCloud too?" for a playlist.
func (a *App) ConfirmDrops(link string, remove bool) error {
	sp := a.scByURLLocked(link)
	if sp == nil {
		return errors.New("that playlist isn't synced any more")
	}
	before := a.syncSnapshot()
	a.State.mu.Lock()
	ids := append([]int64(nil), sp.AskDrop...)
	sp.AskDrop, sp.AskNote = nil, ""
	sp.Dropped = append(sp.Dropped, ids...)
	a.State.save()
	a.State.mu.Unlock()
	if !remove || len(ids) == 0 {
		return nil // they stay off here, and on SoundCloud
	}
	return a.pushDrops(sp, ids, before)
}

// Undrop puts a song taken off a synced playlist back in it (not on
// SoundCloud: if it came off there, it comes back with a sync once it's
// back there).
func (a *App) Undrop(link string, scID int64) error {
	sp := a.scByURLLocked(link)
	if sp == nil {
		return errors.New("that playlist isn't synced any more")
	}
	a.State.mu.Lock()
	var entry *SCEntry
	for _, e := range sp.Entries {
		if e.SC != nil && e.SC.ID == scID {
			entry = e
		}
	}
	if entry == nil {
		a.State.mu.Unlock()
		return errors.New("that song isn't in the playlist any more")
	}
	sp.Dropped, sp.AskDrop = dropIDs(sp.Dropped, []int64{scID}), dropIDs(sp.AskDrop, []int64{scID})
	entry.Note = ""
	if entry.TrackID != "" {
		entry.Status = "have"
	} else {
		entry.Status = "missing" // the next sync downloads it
	}
	entries := append([]*SCEntry(nil), sp.Entries...)
	folder := "SoundCloud"
	if sp.Source == "youtube" {
		folder = "YouTube"
	}
	title, u := sp.Title, sp.URL
	a.State.save()
	a.State.mu.Unlock()
	return a.applyChange(scChange(folder, title, u, entries, false))
}

// SCRestore puts a SoundCloud playlist's tracks back (for undo).
type SCRestore struct {
	URL        string  `json:"url"`
	PlaylistID int64   `json:"playlistId"`
	Tracks     []int64 `json:"tracks"`
}

func keepIDs(ids []int64, keep map[int64]bool) []int64 {
	var out []int64
	for _, id := range ids {
		if keep[id] {
			out = append(out, id)
		}
	}
	return out
}

func dropIDs(ids, drop []int64) []int64 {
	d := map[int64]bool{}
	for _, id := range drop {
		d[id] = true
	}
	var out []int64
	for _, id := range ids {
		if !d[id] {
			out = append(out, id)
		}
	}
	return out
}

func dropStrings(ids, drop []string) []string {
	d := map[string]bool{}
	for _, id := range drop {
		d[id] = true
	}
	var out []string
	for _, id := range ids {
		if !d[id] {
			out = append(out, id)
		}
	}
	return out
}
