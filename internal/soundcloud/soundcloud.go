// Package soundcloud reads public (or secret-link) playlists through the same
// web API the soundcloud.com site uses. No account or API key is needed: a
// client_id is scraped from the site's JavaScript and cached.
package soundcloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const api = "https://api-v2.soundcloud.com"

type Track struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Uploader     string `json:"uploader"`
	Artist       string `json:"artist,omitempty"` // publisher_metadata.artist, when the uploader set it
	DurationMS   int64  `json:"durationMs"`
	URL          string `json:"url"`
	Artwork      string `json:"artwork,omitempty"`
	PurchaseURL  string `json:"purchaseUrl,omitempty"`
	PurchaseText string `json:"purchaseText,omitempty"`
	Downloadable bool   `json:"downloadable"`
	Unavailable  bool   `json:"unavailable,omitempty"`
	// GoPlus: SoundCloud Go+ only; everyone else gets a 30-second preview.
	GoPlus bool `json:"goPlus,omitempty"`
	// Protected: SoundCloud only streams it copy-protected.
	Protected bool `json:"protected,omitempty"`
	// DownloadsLeft is false when the uploader's download limit is used up.
	DownloadsLeft bool `json:"downloadsLeft"`
	// Links are free-download and store links from the buy button and description.
	Links []Link `json:"links,omitempty"`

	transcodings []transcoding
	trackAuth    string
}

type transcoding struct {
	URL     string `json:"url"`
	Preset  string `json:"preset"`
	Snipped bool   `json:"snipped"`
	Quality string `json:"quality"`
	Format  struct {
		Protocol string `json:"protocol"`
		MimeType string `json:"mime_type"`
	} `json:"format"`
}

type Playlist struct {
	Title  string   `json:"title"`
	URL    string   `json:"url"`
	Owner  string   `json:"owner"`
	Tracks []*Track `json:"tracks"`
}

type Client struct {
	HTTP     *http.Client
	mu       sync.Mutex
	clientID string
}

func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Second}}
}

var ErrNotPlaylist = errors.New("that link isn't a SoundCloud playlist, album, or track")

// FetchPlaylist resolves a soundcloud.com playlist/set/album URL (or a single
// track URL, returned as a one-track playlist).
func (c *Client) FetchPlaylist(link string) (*Playlist, error) {
	link = strings.TrimSpace(link)
	if !strings.Contains(link, "://") {
		link = "https://" + link
	}
	u, err := url.Parse(link)
	if err != nil || !strings.HasSuffix(u.Hostname(), "soundcloud.com") {
		return nil, fmt.Errorf("not a soundcloud.com link: %q", link)
	}
	u.Host = "soundcloud.com" // m.soundcloud.com, www., on.soundcloud.com short links resolve too
	if strings.HasPrefix(u.Hostname(), "on.") {
		u.Host = "on.soundcloud.com"
	}
	u.RawQuery = ""

	var raw rawResource
	if err := c.get("/resolve", url.Values{"url": {u.String()}}, &raw); err != nil {
		return nil, err
	}
	switch raw.Kind {
	case "track":
		var t rawTrack
		if err := json.Unmarshal(raw.body, &t); err != nil {
			return nil, err
		}
		return &Playlist{Title: t.Title, URL: t.PermalinkURL, Owner: t.User.Username, Tracks: []*Track{t.convert()}}, nil
	case "playlist", "system-playlist":
	default:
		return nil, ErrNotPlaylist
	}

	var p rawPlaylist
	if err := json.Unmarshal(raw.body, &p); err != nil {
		return nil, err
	}
	// Only the first few tracks come back complete; the rest are just IDs.
	full := map[int64]*rawTrack{}
	var missing []int64
	for i := range p.Tracks {
		t := &p.Tracks[i]
		if t.Title != "" {
			full[t.ID] = t
		} else {
			missing = append(missing, t.ID)
		}
	}
	for len(missing) > 0 {
		n := min(50, len(missing))
		batch := missing[:n]
		missing = missing[n:]
		ids := make([]string, len(batch))
		for i, id := range batch {
			ids[i] = strconv.FormatInt(id, 10)
		}
		var got []rawTrack
		if err := c.get("/tracks", url.Values{"ids": {strings.Join(ids, ",")}}, &got); err != nil {
			return nil, err
		}
		for i := range got {
			full[got[i].ID] = &got[i]
		}
	}

	out := &Playlist{Title: p.Title, URL: p.PermalinkURL, Owner: p.User.Username}
	for _, st := range p.Tracks {
		if t, ok := full[st.ID]; ok {
			out.Tracks = append(out.Tracks, t.convert())
		} else {
			// Removed, region-locked, or private: keep the slot so numbering matches.
			out.Tracks = append(out.Tracks, &Track{ID: st.ID, Title: "(unavailable track)", Unavailable: true})
		}
	}
	return out, nil
}

type rawResource struct {
	Kind string `json:"kind"`
	body json.RawMessage
}

func (r *rawResource) UnmarshalJSON(b []byte) error {
	r.body = append([]byte(nil), b...)
	var k struct {
		Kind string `json:"kind"`
	}
	err := json.Unmarshal(b, &k)
	r.Kind = k.Kind
	return err
}

type rawUser struct {
	Username string `json:"username"`
}

type rawTrack struct {
	ID               int64  `json:"id"`
	Title            string `json:"title"`
	Duration         int64  `json:"duration"`
	FullDuration     int64  `json:"full_duration"`
	PermalinkURL     string `json:"permalink_url"`
	ArtworkURL       string `json:"artwork_url"`
	PurchaseURL      string `json:"purchase_url"`
	PurchaseTitle    string `json:"purchase_title"`
	Downloadable     bool   `json:"downloadable"`
	HasDownloadsLeft bool   `json:"has_downloads_left"`
	TrackAuth        string `json:"track_authorization"`
	Policy           string `json:"policy"`
	Media            struct {
		Transcodings []transcoding `json:"transcodings"`
	} `json:"media"`
	Description       string  `json:"description"`
	User              rawUser `json:"user"`
	PublisherMetadata *struct {
		Artist string `json:"artist"`
	} `json:"publisher_metadata"`
}

type rawPlaylist struct {
	Title        string     `json:"title"`
	PermalinkURL string     `json:"permalink_url"`
	User         rawUser    `json:"user"`
	Tracks       []rawTrack `json:"tracks"`
}

func (t *rawTrack) convert() *Track {
	d := t.FullDuration // "snipped" 30s previews report the real length here
	if d == 0 {
		d = t.Duration
	}
	out := &Track{
		ID: t.ID, Title: t.Title, Uploader: t.User.Username, DurationMS: d,
		URL: t.PermalinkURL, Artwork: t.ArtworkURL, PurchaseURL: t.PurchaseURL,
		PurchaseText: t.PurchaseTitle, Downloadable: t.Downloadable, DownloadsLeft: t.HasDownloadsLeft,
		Links:        ExtractLinks(t.Description, t.PurchaseURL, t.PurchaseTitle),
		transcodings: t.Media.Transcodings, trackAuth: t.TrackAuth,
	}
	if t.PublisherMetadata != nil {
		out.Artist = strings.TrimSpace(t.PublisherMetadata.Artist)
	}
	full := false
	for _, tc := range t.Media.Transcodings {
		if !tc.Snipped {
			full = true
		}
	}
	out.GoPlus = t.Policy == "SNIP" || (len(t.Media.Transcodings) > 0 && !full)
	out.Protected = out.protectedOnly()
	return out
}

// TrackByID looks a track up by its SoundCloud id.
func (c *Client) TrackByID(id string) (*Track, error) {
	var raw rawTrack
	if err := c.get("/tracks/"+url.PathEscape(id), url.Values{}, &raw); err != nil {
		return nil, err
	}
	return raw.convert(), nil
}

// get calls the API, refreshing the client_id once if it has expired.
func (c *Client) get(path string, q url.Values, out any, base ...string) error {
	root := api
	if len(base) > 0 {
		root = base[0]
	}
	for attempt := 0; attempt < 2; attempt++ {
		id, err := c.id(attempt > 0)
		if err != nil {
			return err
		}
		q.Set("client_id", id)
		req, _ := http.NewRequest("GET", root+path+"?"+q.Encode(), nil)
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "application/json")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return fmt.Errorf("couldn't reach SoundCloud: %w", err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		switch {
		case resp.StatusCode == 401 || resp.StatusCode == 403:
			continue // stale client_id: scrape a fresh one
		case resp.StatusCode == 404:
			return errors.New("SoundCloud says that link doesn't exist (or it's private — use the secret share link)")
		case resp.StatusCode != 200:
			return fmt.Errorf("SoundCloud returned HTTP %d", resp.StatusCode)
		}
		return json.Unmarshal(body, out)
	}
	return errors.New("SoundCloud rejected the request (couldn't get a working client id)")
}

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0 Safari/537.36"

var scriptRe = regexp.MustCompile(`<script[^>]+src="(https://a-v2\.sndcdn\.com/assets/[^"]+\.js)"`)
var clientIDRe = regexp.MustCompile(`client_id\s*[:=]\s*"([0-9a-zA-Z]{32})"`)

func idCachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "SuperSync", "soundcloud-client-id")
}

func (c *Client) id(refresh bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !refresh {
		if c.clientID != "" {
			return c.clientID, nil
		}
		if p := idCachePath(); p != "" {
			if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) == 32 {
				c.clientID = strings.TrimSpace(string(b))
				return c.clientID, nil
			}
		}
	}
	id, err := c.scrapeID()
	if err != nil {
		return "", err
	}
	c.clientID = id
	if p := idCachePath(); p != "" {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(id), 0o644)
	}
	return id, nil
}

func (c *Client) fetch(u string) (string, error) {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("couldn't reach SoundCloud: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(b), err
}

func (c *Client) scrapeID() (string, error) {
	page, err := c.fetch("https://soundcloud.com/")
	if err != nil {
		return "", err
	}
	scripts := scriptRe.FindAllStringSubmatch(page, -1)
	// The id lives in one of the later bundles; check from the end.
	for i := len(scripts) - 1; i >= 0; i-- {
		js, err := c.fetch(scripts[i][1])
		if err != nil {
			continue
		}
		if m := clientIDRe.FindStringSubmatch(js); m != nil {
			return m[1], nil
		}
	}
	return "", errors.New("couldn't find a SoundCloud client id (SoundCloud may have changed their site)")
}
