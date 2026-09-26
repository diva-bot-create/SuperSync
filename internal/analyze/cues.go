package analyze

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"supersync/internal/align"
	"supersync/internal/audio"
	"supersync/internal/library"
	"supersync/internal/rekordbox"
)

// CueStatus is the verdict on moving one track's cues to another file.
type CueStatus string

const (
	CueReady         CueStatus = "ready"          // lined up confidently
	CueCheck         CueStatus = "check"          // lined up, but worth checking one cue after import
	CueDifferentEdit CueStatus = "different-edit" // lengths/structure differ; one offset can't fit all cues
	CueNotSame       CueStatus = "not-same"       // the audio doesn't match
	CueUnreadable    CueStatus = "unreadable"     // couldn't decode one of the files
)

// Pair names the file whose rekordbox prep should move (From) and the file
// that should receive it (To).
type Pair struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// CueTransfer is the plan for one pair.
type CueTransfer struct {
	Pair
	FromRel     string    `json:"fromRel"`
	ToRel       string    `json:"toRel"`
	FromQuality string    `json:"fromQuality"`
	ToQuality   string    `json:"toQuality"`
	Name        string    `json:"name"`
	Cues        int       `json:"cues"`
	Grid        bool      `json:"grid"`
	Offset      float64   `json:"offset"`     // seconds; cue at t in From -> t+Offset in To
	Confidence  float64   `json:"confidence"` // 0..1
	Status      CueStatus `json:"status"`
	Note        string    `json:"note,omitempty"`
	Playlists   []string  `json:"playlists,omitempty"`
	Dropped     int       `json:"dropped,omitempty"` // cues that would fall outside the new file
}

// Usable reports whether the transfer can go into an XML.
func (c *CueTransfer) Usable() bool { return c.Status == CueReady || c.Status == CueCheck }

// CuePairs picks, for every duplicate group, the copy with the most cues as
// the source when the keeper has none. keep maps group ID -> chosen keeper
// path; groups not in keep use their recommended keeper.
func CuePairs(groups []*Group, keep map[string]string) []Pair {
	var out []Pair
	for _, g := range groups {
		keeper := g.Copies[0]
		if p, ok := keep[g.ID]; ok {
			for _, c := range g.Copies {
				if c.Path == p {
					keeper = c
				}
			}
		}
		if keeper.Cues > 0 {
			continue
		}
		var src *Copy
		for _, c := range g.Copies {
			if c != keeper && c.Cues > 0 && (src == nil || c.Cues > src.Cues) {
				src = c
			}
		}
		if src != nil {
			out = append(out, Pair{From: src.Path, To: keeper.Path})
		}
	}
	return out
}

// PlanCues decodes and lines up each pair (in parallel).
func PlanCues(lib *library.Library, col rekordbox.Collection, pairs []Pair) ([]*CueTransfer, error) {
	out := make([]*CueTransfer, len(pairs))
	for i, p := range pairs {
		from, to := lib.ByPath(p.From), lib.ByPath(p.To)
		switch {
		case from == nil || to == nil:
			return nil, fmt.Errorf("not in your library: %s", map[bool]string{true: p.From, false: p.To}[from == nil])
		case col.Lookup(p.From) == nil:
			return nil, fmt.Errorf("%s isn't in your rekordbox collection export", from.Rel)
		}
		out[i] = &CueTransfer{Pair: p}
	}
	var wg sync.WaitGroup
	jobs := make(chan *CueTransfer)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				plan(t, lib.ByPath(t.From), lib.ByPath(t.To), col.Lookup(t.From))
			}
		}()
	}
	for _, t := range out {
		jobs <- t
	}
	close(jobs)
	wg.Wait()
	return out, nil
}

func plan(t *CueTransfer, from, to *library.Track, rb *rekordbox.CollectionTrack) {
	t.FromRel, t.ToRel = from.Rel, to.Rel
	t.FromQuality, t.ToQuality = from.QualityLabel(), to.QualityLabel()
	name, artist := rb.Name, rb.Artist
	if rb.Name == from.Title && to.Title != "" { // unedited rip tag: the purchase's tags read better
		name, artist = to.Title, to.Artist
	}
	t.Name = name
	if artist != "" {
		t.Name = artist + " – " + name
	}
	t.Cues, t.Grid, t.Playlists = len(rb.Marks), len(rb.Tempos) > 0, rb.Playlists

	a, ta, err := audio.DecodeMono(from.Path, align.Rate)
	if err == nil {
		var b []float32
		var tb audio.Timing
		b, tb, err = audio.DecodeMono(to.Path, align.Rate)
		if err == nil {
			r := align.Offset(a, b)
			t.Offset, t.Confidence = r.Offset, r.Confidence
			switch {
			case r.Confidence < 0.5:
				t.Status, t.Note = CueNotSame, "The audio in these two files doesn't match closely enough to line up."
			case !r.SameEdit:
				t.Status, t.Note = CueDifferentEdit, "These are different edits (a section is longer or shorter), so the cues can't all be shifted by one amount. Set them by hand on the kept file."
			case ta == audio.Approximate || tb == audio.Approximate:
				t.Status, t.Note = CueCheck, "AAC/Opus files carry a short encoder lead-in that rekordbox may count differently, so cues could be off by up to ~50 ms. Check one cue after importing."
			case r.Confidence < 0.8:
				t.Status, t.Note = CueCheck, "The files sound slightly different (probably a different master), so check one cue after importing."
			default:
				t.Status = CueReady
			}
		}
	}
	if err != nil {
		t.Status = CueUnreadable
		if errors.Is(err, audio.ErrNoDecoder) {
			t.Note = "Reading " + strings.ToUpper(from.Format+"/"+to.Format) + " audio needs ffmpeg installed (MP3, FLAC, WAV and AIFF work without it)."
		} else {
			t.Note = "Couldn't read the audio: " + err.Error()
		}
		return
	}
	if t.Usable() {
		_, _, t.Dropped = shiftMarks(rb.Marks, t.Offset, to.Duration)
	}
}

// CueTrack builds the rekordbox entry for t.To carrying t.From's prep.
func CueTrack(t *CueTransfer, lib *library.Library, col rekordbox.Collection) rekordbox.PlaylistTrack {
	from, to := lib.ByPath(t.From), lib.ByPath(t.To)
	rb := col.Lookup(t.From)

	// Copy everything the DJ set (rating, colour, comments, key, play count,
	// date added...) but not the old file's technical details.
	var attrs = rb.Attrs[:0:0]
	for _, a := range rb.Attrs {
		switch a.Name.Local {
		case "TrackID", "Location", "Kind", "Size", "TotalTime", "BitRate", "SampleRate", "DateModified":
			continue
		}
		attrs = append(attrs, a)
	}
	attrs = rekordbox.SetAttr(attrs, "Size", strconv.FormatInt(to.Size, 10))
	if to.SampleRate > 0 {
		attrs = rekordbox.SetAttr(attrs, "SampleRate", strconv.Itoa(to.SampleRate))
	}

	pt := rekordbox.PlaylistTrack{
		Path: to.Path, Kind: rekordbox.Kind(to.Format), Duration: to.Duration, Bitrate: to.Bitrate,
		Attrs: attrs, Tempos: shiftTempos(rb.Tempos, t.Offset),
	}
	pt.Marks, _, _ = shiftMarks(rb.Marks, t.Offset, to.Duration)
	// If the name in rekordbox is just the rip's own tag (never edited), the
	// purchased file's tags are the better source.
	if rb.Name == from.Title && to.Title != "" {
		pt.Name, pt.Artist, pt.Album = to.Title, to.Artist, to.Album
	}
	return pt
}

func fmtSecs(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

// shiftMarks moves cue/loop positions by off seconds, dropping any that would
// start before 0:00 or after the end of the new file.
func shiftMarks(marks []rekordbox.Elem, off, dur float64) (out []rekordbox.Elem, kept, dropped int) {
	for _, m := range marks {
		start, err := strconv.ParseFloat(m.Get("Start"), 64)
		if err != nil {
			dropped++
			continue
		}
		s := start + off
		if s < 0 && s > -0.005 {
			s = 0 // lands on the very start, give or take rounding
		}
		if s < 0 || (dur > 0 && s > dur) {
			dropped++
			continue
		}
		attrs := append(m.Attrs[:0:0], m.Attrs...)
		attrs = rekordbox.SetAttr(attrs, "Start", fmtSecs(s))
		if e, err := strconv.ParseFloat(m.Get("End"), 64); err == nil && m.Get("End") != "" {
			attrs = rekordbox.SetAttr(attrs, "End", fmtSecs(math.Min(e+off, math.Max(dur, s))))
		}
		out = append(out, rekordbox.Elem{Attrs: attrs})
		kept++
	}
	return out, kept, dropped
}

// shiftTempos moves the beatgrid by off seconds. Markers pushed before 0:00
// are dropped when a later one takes over; the first marker is then extended
// back by whole beats to the start of the file (keeping bar positions), so an
// extended mix's longer intro is gridded too.
func shiftTempos(tempos []rekordbox.Elem, off float64) []rekordbox.Elem {
	const tol = 0.005 // treat anything within 5 ms of 0:00 as 0:00
	type tm struct {
		at float64
		e  rekordbox.Elem
	}
	var ts []tm
	for _, t := range tempos {
		v, err := strconv.ParseFloat(t.Get("Inizio"), 64)
		if err != nil {
			continue
		}
		ts = append(ts, tm{v + off, t})
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].at < ts[j].at })
	for len(ts) > 1 && ts[1].at <= tol {
		ts = ts[1:] // a later marker already covers 0:00
	}
	var out []rekordbox.Elem
	for i, t := range ts {
		at := t.at
		attrs := append(t.e.Attrs[:0:0], t.e.Attrs...)
		if bpm, _ := strconv.ParseFloat(t.e.Get("Bpm"), 64); i == 0 && bpm > 0 {
			beat := 60 / bpm
			k := math.Floor((at + tol) / beat)
			at -= k * beat
			beats := 4
			if num, _, ok := strings.Cut(t.e.Get("Metro"), "/"); ok {
				if n, err := strconv.Atoi(num); err == nil && n > 0 {
					beats = n
				}
			}
			if b, err := strconv.Atoi(t.e.Get("Battito")); err == nil {
				attrs = rekordbox.SetAttr(attrs, "Battito", strconv.Itoa(((b-1-int(k))%beats+beats)%beats+1))
			}
		}
		attrs = rekordbox.SetAttr(attrs, "Inizio", fmtSecs(math.Max(0, at)))
		out = append(out, rekordbox.Elem{Attrs: attrs})
	}
	return out
}
