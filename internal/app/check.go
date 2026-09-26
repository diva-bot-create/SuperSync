package app

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"supersync/internal/analyze"
	"supersync/internal/library"
	"supersync/internal/match"
	"supersync/internal/rekordbox"
	"supersync/internal/soundcloud"
)

// File is a library file as shown to the user.
type File struct {
	Path        string  `json:"path"`
	Rel         string  `json:"rel"`
	Artist      string  `json:"artist"`
	Title       string  `json:"title"`
	Quality     string  `json:"quality"`
	Lossless    bool    `json:"lossless"`
	Duration    float64 `json:"secs"`
	Score       float64 `json:"score,omitempty"`
	InRekordbox bool    `json:"inRekordbox"`
	Cues        int     `json:"cues"`
	Upscaled    string  `json:"upscaled,omitempty"`
	TrueKbps    int     `json:"trueKbps,omitempty"`
}

type Status string

const (
	Have        Status = "have"
	Maybe       Status = "maybe"
	Need        Status = "need"
	Unavailable Status = "unavailable"
)

type Row struct {
	N       int               `json:"n"`
	SC      *soundcloud.Track `json:"sc"`
	Status  Status            `json:"status"`
	Match   *File             `json:"match,omitempty"`   // best owned copy
	Others  []*File           `json:"others,omitempty"`  // other candidates / duplicate copies
	Decided bool              `json:"decided,omitempty"` // user confirmed this answer
	Upgrade string            `json:"upgrade,omitempty"` // why the owned copy is low quality
	Search  string            `json:"search"`
	Links   analyze.Links     `json:"links"`
}

type Result struct {
	Playlist *soundcloud.Playlist `json:"playlist"`
	Rows     []*Row               `json:"rows"`
	Have     int                  `json:"have"`
	Maybe    int                  `json:"maybe"`
	Need     int                  `json:"need"`
	Upgrades int                  `json:"upgrades"`
	// Free counts tracks still to get (or upgrade) that have a free download.
	Free int `json:"free"`
}

func decisionKey(id int64) string { return "sc:" + strconv.FormatInt(id, 10) }

// Check compares a SoundCloud playlist link (or a text file of "Artist - Title"
// lines) against the library.
func (a *App) Check(source string, progress library.Progress) (*Result, error) {
	var pl *soundcloud.Playlist
	var err error
	if st, serr := os.Stat(source); serr == nil && !st.IsDir() {
		pl, err = readTextList(source)
	} else {
		pl, err = a.SC.FetchPlaylist(source)
	}
	if err != nil {
		return nil, err
	}
	lib, err := a.Library(progress)
	if err != nil {
		return nil, err
	}
	return a.Compare(pl, lib), nil
}

// Compare matches every playlist track against lib.
func (a *App) Compare(pl *soundcloud.Playlist, lib *library.Library) *Result {
	a.mu.Lock()
	decisions := a.Cfg.Decisions
	minKbps := a.Cfg.MinKbps
	col := a.Col
	a.mu.Unlock()

	res := &Result{Playlist: pl}
	for i, t := range pl.Tracks {
		row := &Row{N: i + 1, SC: t}
		res.Rows = append(res.Rows, row)
		if t.Unavailable {
			row.Status = Unavailable
			continue
		}
		keys := trackKeys(t)
		row.Search = searchText(t)
		row.Links = analyze.StoreLinks(row.Search)

		hits := lib.Find(keys)
		var sure []library.Hit
		for _, h := range hits {
			if h.Score >= match.Sure {
				sure = append(sure, h)
			}
		}

		switch d := decisions[decisionKey(t.ID)]; {
		case d == "none":
			row.Status, row.Decided = Need, true
		case d != "" && lib.ByPath(d) != nil:
			row.Status, row.Decided = Have, true
			sure = append([]library.Hit{{Track: lib.ByPath(d), Score: 1}}, sure...)
		case len(sure) > 0:
			row.Status = Have
		case len(hits) > 0:
			row.Status = Maybe
		default:
			row.Status = Need
		}

		if row.Status == Have {
			// Best copy = highest quality among confident matches (the decided one wins).
			best := sure[0]
			if !row.Decided {
				for _, h := range sure[1:] {
					if h.Track.Quality() > best.Track.Quality() {
						best = h
					}
				}
			}
			row.Match = fileView(best.Track, best.Score, col)
			if bad, why := analyze.NeedsUpgrade(&best.Track.Info, minKbps); bad {
				row.Upgrade = why
				res.Upgrades++
			}
		}
		for _, h := range hits {
			if row.Match == nil || h.Track.Path != row.Match.Path {
				row.Others = append(row.Others, fileView(h.Track, h.Score, col))
			}
			if len(row.Others) == 5 {
				break
			}
		}
		if (row.Status == Need || row.Status == Maybe || row.Upgrade != "") && FreeLink(t) != "" {
			res.Free++
		}
		switch row.Status {
		case Have:
			res.Have++
		case Maybe:
			res.Maybe++
		case Need:
			res.Need++
		}
	}
	return res
}

func trackKeys(t *soundcloud.Track) []match.Key {
	var keys []match.Key
	if t.Artist != "" && !strings.EqualFold(t.Artist, t.Uploader) {
		keys = append(keys, match.Parse(t.Artist, t.Title)...)
	}
	keys = append(keys, match.Parse(t.Uploader, t.Title)...)
	for i := range keys {
		keys[i].Duration = float64(t.DurationMS) / 1000
	}
	return keys
}

// searchText is a clean "artist title (version)" for store searches. The
// uploader is only used as the artist when nothing better is available, since
// it's often a channel ("AYO IT'S HOUSE TIME") rather than the artist.
func searchText(t *soundcloud.Track) string {
	keys := match.Parse("", t.Title)
	if len(keys) > 0 && len(keys[0].Artist) == 0 && t.Artist != "" && !strings.EqualFold(t.Artist, t.Uploader) {
		keys = match.Parse(t.Artist, t.Title)
	}
	if q := match.SearchQuery(keys); q != "" {
		return q
	}
	return t.Title
}

func fileView(t *library.Track, score float64, col rekordbox.Collection) *File {
	f := &File{
		Path: t.Path, Rel: t.Rel, Artist: t.Artist, Title: t.Title,
		Quality: t.QualityLabel(), Lossless: t.Lossless, Duration: t.Duration, Score: score,
		Upscaled: t.Upscaled(), TrueKbps: t.TrueKbps(),
	}
	if ct := col.Lookup(t.Path); ct != nil {
		f.InRekordbox, f.Cues = true, ct.Cues
	}
	return f
}

// FreeLink is the best free-download link for a track: a download gate or
// file host, a store page marked free, or the track's own SoundCloud
// download button. "" if there's none.
func FreeLink(t *soundcloud.Track) string {
	for _, l := range t.Links {
		if l.Kind == soundcloud.FreeDownload || l.Free {
			return l.URL
		}
	}
	if t.Downloadable && t.DownloadsLeft {
		return t.URL
	}
	return ""
}

// BuyLink is the first store link the uploader gave, or "".
func BuyLink(t *soundcloud.Track) string {
	for _, l := range t.Links {
		if l.Kind == soundcloud.Store {
			return l.URL
		}
	}
	return ""
}

// readTextList reads one track per line ("Artist - Title"); blank lines and
// lines starting with # are ignored.
func readTextList(path string) (*soundcloud.Playlist, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	pl := &soundcloud.Playlist{Title: name, URL: path}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pl.Tracks = append(pl.Tracks, &soundcloud.Track{ID: -int64(len(pl.Tracks) + 1), Title: line})
	}
	if len(pl.Tracks) == 0 {
		return nil, fmt.Errorf("%s has no tracks in it", path)
	}
	return pl, sc.Err()
}

// ExportXML writes a rekordbox XML with one playlist of the given files.
func (a *App) ExportXML(w io.Writer, name string, paths []string) error {
	lib, err := a.Library(nil)
	if err != nil {
		return err
	}
	pl := rekordbox.Playlist{Name: name}
	for _, p := range paths {
		t := lib.ByPath(p)
		if t == nil {
			continue
		}
		artist, title := displayName(t)
		pl.Tracks = append(pl.Tracks, rekordbox.PlaylistTrack{
			Path: t.Path, Name: title, Artist: artist, Album: t.Album,
			Duration: t.Duration, Bitrate: t.Bitrate, Kind: rekordbox.Kind(t.Format),
		})
	}
	return rekordbox.WritePlaylists(w, "SuperSync", []rekordbox.Playlist{pl})
}

// displayName is the track's tags, or "Artist - Title" split from the
// filename when the file is untagged.
func displayName(t *library.Track) (artist, title string) {
	if t.Title != "" {
		return t.Artist, t.Title
	}
	base := strings.TrimSuffix(filepath.Base(t.Path), filepath.Ext(t.Path))
	if a, b, ok := strings.Cut(base, " - "); ok {
		return strings.TrimSpace(a), strings.TrimSpace(b)
	}
	return t.Artist, base
}

// ExportM3U writes an extended M3U8 playlist, which rekordbox can also import.
func ExportM3U(w io.Writer, lib *library.Library, paths []string) error {
	bw := bufio.NewWriter(w)
	bw.WriteString("#EXTM3U\n")
	for _, p := range paths {
		t := lib.ByPath(p)
		if t == nil {
			continue
		}
		artist, title := displayName(t)
		name := title
		if artist != "" {
			name = artist + " - " + title
		}
		fmt.Fprintf(bw, "#EXTINF:%d,%s\n%s\n", int(t.Duration+0.5), name, t.Path)
	}
	return bw.Flush()
}

// NeedCSV writes the tracks still to buy/download, with store search links.
func NeedCSV(w io.Writer, res *Result, includeUpgrades bool) error {
	bw := bufio.NewWriter(w)
	bw.WriteString("#,Track,Status,Free download,Buy link,SoundCloud,Beatport,Bandcamp,Traxsource\n")
	for _, r := range res.Rows {
		st := string(r.Status)
		switch {
		case r.Status == Need, r.Status == Maybe:
		case includeUpgrades && r.Upgrade != "":
			st = "upgrade (" + r.Upgrade + ")"
		default:
			continue
		}
		fmt.Fprintf(bw, "%d,%s,%s,%s,%s,%s,%s,%s,%s\n", r.N, csvQuote(r.SC.Title), csvQuote(st),
			csvQuote(FreeLink(r.SC)), csvQuote(BuyLink(r.SC)), r.SC.URL,
			r.Links.Beatport, r.Links.Bandcamp, r.Links.Traxsource)
	}
	return bw.Flush()
}

func csvQuote(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}
