// Package youtube downloads YouTube playlists as mp3s, talking to YouTube's
// own InnerTube API (the one its apps use) with nothing to install.
//
// The audio is YouTube's AAC stream, pulled out of its fragmented MP4,
// decoded and re-encoded as a CBR mp3 in pure Go (see mp3.go).
//
// Stream URLs come from the visionOS app's client identity, which YouTube
// serves plain URLs (no JavaScript signature puzzle to solve). When YouTube
// changes that, the client constants below are the first thing to update.
package youtube

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"supersync/internal/match"
)

type Entry struct {
	ID       string
	Title    string
	Channel  string
	Duration float64 // seconds; 0 when the playlist page didn't say
}

type Playlist struct {
	Title   string
	Entries []*Entry
}

// Artist is the channel, minus the auto-generated " - Topic" suffix.
func (e *Entry) Artist() string { return strings.TrimSuffix(e.Channel, " - Topic") }

func (e *Entry) URL() string { return "https://www.youtube.com/watch?v=" + e.ID }

// IsLink reports whether u is a youtube.com / youtu.be / music.youtube.com link.
func IsLink(u string) bool {
	p, err := parseLink(u)
	if err != nil {
		return false
	}
	h := strings.TrimPrefix(p.Hostname(), "www.")
	return h == "youtu.be" || h == "youtube.com" || strings.HasSuffix(h, ".youtube.com")
}

func parseLink(u string) (*url.URL, error) {
	u = strings.TrimSpace(u)
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	return url.Parse(u)
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// ids pulls the playlist id (list=) and video id out of any YouTube link form.
func ids(link string) (playlist, video string, err error) {
	p, err := parseLink(link)
	if err != nil {
		return "", "", err
	}
	q := p.Query()
	playlist = q.Get("list")
	video = q.Get("v")
	parts := strings.Split(strings.Trim(p.Path, "/"), "/")
	switch {
	case strings.HasSuffix(p.Hostname(), "youtu.be") && len(parts) > 0:
		video = parts[0]
	case len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "live" || parts[0] == "embed"):
		video = parts[1]
	}
	// Auto-generated mixes (RD…) are endless and personal; treat as the one video.
	if strings.HasPrefix(playlist, "RD") && video != "" {
		playlist = ""
	}
	if playlist == "" && !idRe.MatchString(video) {
		return "", "", fmt.Errorf("that doesn't look like a YouTube video or playlist link: %q", link)
	}
	return playlist, video, nil
}

type Client struct {
	HTTP *http.Client

	mu      sync.Mutex
	visitor string
}

func New() *Client { return &Client{HTTP: &http.Client{Timeout: 30 * time.Second}} }

const (
	webUA        = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0 Safari/537.36"
	webVersion   = "2.20260708.00.00"
	visionUA     = "Mozilla/5.0 (Macintosh; Intel Mac OS X 15_7_3) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Safari/605.1.15"
	visionClient = "101" // X-Youtube-Client-Name for VISIONOS
)

var visitorRe = regexp.MustCompile(`"VISITOR_DATA":"([^"]+)"`)

// visitorID is the anonymous visitor token youtube.com hands every browser.
// Without it the player API refuses to answer.
func (c *Client) visitorID() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.visitor != "" {
		return c.visitor, nil
	}
	req, _ := http.NewRequest("GET", "https://www.youtube.com/", nil)
	req.Header.Set("User-Agent", webUA)
	req.Header.Set("Accept-Language", "en")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("couldn't reach YouTube: %w", err)
	}
	defer resp.Body.Close()
	page, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	m := visitorRe.FindSubmatch(page)
	if m == nil {
		return "", errors.New("couldn't read YouTube's home page (YouTube may have changed their site)")
	}
	c.visitor = string(m[1])
	return c.visitor, nil
}

// api POSTs to an InnerTube endpoint as the given client and decodes the reply.
func (c *Client) api(endpoint string, client map[string]any, headers map[string]string, body map[string]any, out any) error {
	vd, err := c.visitorID()
	if err != nil {
		return err
	}
	client["hl"], client["gl"], client["visitorData"] = "en", "US", vd
	body["context"] = map[string]any{"client": client}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", "https://www.youtube.com/youtubei/v1/"+endpoint+"?prettyPrint=false", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Visitor-Id", vd)
	req.Header.Set("Origin", "https://www.youtube.com")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach YouTube: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("YouTube returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// List reads a playlist's videos (or a single video) without downloading.
func (c *Client) List(link string) (*Playlist, error) {
	plID, videoID, err := ids(link)
	if err != nil {
		return nil, err
	}
	if plID == "" {
		p, err := c.player(videoID)
		if err != nil {
			return nil, err
		}
		return &Playlist{Title: p.entry.Title, Entries: []*Entry{p.entry}}, nil
	}

	web := func() map[string]any { return map[string]any{"clientName": "WEB", "clientVersion": webVersion} }
	hdr := map[string]string{"User-Agent": webUA, "X-Youtube-Client-Name": "1", "X-Youtube-Client-Version": webVersion}
	var page any
	if err := c.api("browse", web(), hdr, map[string]any{"browseId": "VL" + plID}, &page); err != nil {
		return nil, err
	}
	pl := &Playlist{}
	pl.Title, _ = dig(page, "microformat", "microformatDataRenderer", "title").(string)
	seen := map[string]bool{}
	for pages := 0; pages < 200; pages++ { // 100 videos a page; 20,000 is plenty
		for _, l := range find(page, "lockupViewModel") {
			if e := lockupEntry(l); e != nil && !seen[e.ID] {
				seen[e.ID] = true
				pl.Entries = append(pl.Entries, e)
			}
		}
		token := ""
		for _, cc := range find(page, "continuationCommand") {
			if t, ok := dig(cc, "token").(string); ok {
				token = t
			}
		}
		if token == "" {
			break
		}
		page = nil
		if err := c.api("browse", web(), hdr, map[string]any{"continuation": token}, &page); err != nil {
			return nil, err
		}
	}
	if pl.Title == "" && len(pl.Entries) == 0 {
		return nil, errors.New("YouTube says that playlist doesn't exist or is private")
	}
	return pl, nil
}

// lockupEntry reads a video out of a playlist page's lockupViewModel.
func lockupEntry(l any) *Entry {
	id, _ := dig(l, "contentId").(string)
	if typ, _ := dig(l, "contentType").(string); typ != "LOCKUP_CONTENT_TYPE_VIDEO" || !idRe.MatchString(id) {
		return nil
	}
	e := &Entry{ID: id}
	e.Title, _ = dig(l, "metadata", "lockupMetadataViewModel", "title", "content").(string)
	if rows, ok := dig(l, "metadata", "lockupMetadataViewModel", "metadata", "contentMetadataViewModel", "metadataRows").([]any); ok && len(rows) > 0 {
		if parts, ok := dig(rows[0], "metadataParts").([]any); ok && len(parts) > 0 {
			e.Channel, _ = dig(parts[0], "text", "content").(string)
		}
	}
	for _, b := range find(l, "thumbnailBadgeViewModel") {
		if t, ok := dig(b, "text").(string); ok {
			e.Duration = clock(t)
		}
	}
	return e
}

// clock parses "1:02:03" / "4:05" into seconds.
func clock(s string) float64 {
	var secs float64
	for _, p := range strings.Split(s, ":") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return 0
		}
		secs = secs*60 + float64(n)
	}
	return secs
}

type format struct {
	Itag          int    `json:"itag"`
	URL           string `json:"url"`
	MimeType      string `json:"mimeType"`
	Bitrate       int    `json:"bitrate"`
	ContentLength string `json:"contentLength"`
	IsDrc         bool   `json:"isDrc"`
	AudioTrack    *struct {
		AudioIsDefault bool `json:"audioIsDefault"`
	} `json:"audioTrack"`
}

type playerInfo struct {
	entry  *Entry
	format format
}

// player asks for a video's details and picks its best AAC audio stream.
func (c *Client) player(id string) (*playerInfo, error) {
	var r struct {
		PlayabilityStatus struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"playabilityStatus"`
		VideoDetails struct {
			Title         string `json:"title"`
			Author        string `json:"author"`
			LengthSeconds string `json:"lengthSeconds"`
		} `json:"videoDetails"`
		StreamingData struct {
			AdaptiveFormats []format `json:"adaptiveFormats"`
		} `json:"streamingData"`
	}
	client := map[string]any{
		"clientName": "VISIONOS", "clientVersion": "1.02",
		"deviceMake": "Apple", "deviceModel": "RealityDevice17,1",
		"osName": "visionOS", "osVersion": "26.5.23O471",
	}
	hdr := map[string]string{"User-Agent": visionUA, "X-Youtube-Client-Name": visionClient, "X-Youtube-Client-Version": "1.02"}
	body := map[string]any{"videoId": id, "contentCheckOk": true, "racyCheckOk": true}
	if err := c.api("player", client, hdr, body, &r); err != nil {
		return nil, err
	}
	if st := r.PlayabilityStatus; st.Status != "OK" {
		reason := st.Reason
		if reason == "" {
			reason = strings.ToLower(st.Status)
		}
		return nil, fmt.Errorf("YouTube: %s", reason)
	}
	secs, _ := strconv.ParseFloat(r.VideoDetails.LengthSeconds, 64)
	p := &playerInfo{entry: &Entry{ID: id, Title: r.VideoDetails.Title, Channel: r.VideoDetails.Author, Duration: secs}}

	var aac []format
	for _, f := range r.StreamingData.AdaptiveFormats {
		// DRC is YouTube's loudness-squashed copy; dubbed tracks aren't the original.
		if !strings.HasPrefix(f.MimeType, "audio/mp4") || f.URL == "" || f.IsDrc || (f.AudioTrack != nil && !f.AudioTrack.AudioIsDefault) {
			continue
		}
		aac = append(aac, f)
	}
	if len(aac) == 0 {
		return nil, errors.New("YouTube offered no downloadable AAC audio for this video")
	}
	sort.Slice(aac, func(i, j int) bool { return aac[i].Bitrate > aac[j].Bitrate })
	p.format = aac[0]
	return p, nil
}

// Download saves e into dir as "<title>.mp3", tagged with title and artist,
// and returns the path. A file already there is kept, so re-running a
// playlist only fetches what's new. progress may be nil.
func (c *Client) Download(e *Entry, dir string, progress func(done, total int64)) (string, error) {
	if e.Title != "" {
		if path := filepath.Join(dir, safeName(e.Title)+".mp3"); exists(path) {
			return path, nil
		}
	}
	p, err := c.player(e.ID)
	if err != nil {
		return "", err
	}
	// The file keeps the video's name; the tags get a clean artist and title.
	path := filepath.Join(dir, safeName(p.entry.Title)+".mp3")
	artist, title := match.ArtistTitle(p.entry.Title, p.entry.Artist())
	if exists(path) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	raw, err := c.fetch(p.format, progress)
	if err != nil {
		return "", err
	}
	t, err := demux(raw)
	if err == nil && t == nil {
		err = errors.New("unexpected file layout")
	}
	if err != nil {
		return "", fmt.Errorf("couldn't read YouTube's audio file: %w", err)
	}
	out, err := t.mp3(title, artist)
	if err != nil {
		return "", fmt.Errorf("couldn't convert to mp3: %w", err)
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return path, os.Rename(tmp, path)
}

// Existing is the file an earlier Download saved for e in dir, or "".
func Existing(e *Entry, dir string) string {
	if p := filepath.Join(dir, safeName(e.Title)+".mp3"); e.Title != "" && exists(p) {
		return p
	}
	return ""
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// chunk is the range size per request: YouTube throttles a single request
// for a whole file to playback speed, but serves ~10 MB ranges at full speed.
const chunk = 10 << 20

func (c *Client) fetch(f format, progress func(done, total int64)) ([]byte, error) {
	total, _ := strconv.ParseInt(f.ContentLength, 10, 64)
	var buf bytes.Buffer
	if total > 0 {
		buf.Grow(int(total))
	}
	hc := &http.Client{Transport: c.HTTP.Transport, Timeout: 5 * time.Minute}
	for {
		start := int64(buf.Len())
		end := start + chunk - 1
		if total > 0 && end >= total {
			end = total - 1
		}
		req, _ := http.NewRequest("GET", f.URL+"&range="+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(end, 10), nil)
		req.Header.Set("User-Agent", visionUA)
		resp, err := hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("couldn't reach YouTube's audio server: %w", err)
		}
		if resp.StatusCode != 200 && resp.StatusCode != 206 {
			resp.Body.Close()
			return nil, fmt.Errorf("YouTube's audio server returned HTTP %d", resp.StatusCode)
		}
		n, err := io.Copy(&buf, resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if progress != nil {
			progress(int64(buf.Len()), total)
		}
		if n == 0 || (total > 0 && int64(buf.Len()) >= total) || (total == 0 && n < chunk) {
			return buf.Bytes(), nil
		}
	}
}

// safeName makes s usable as a file name on Mac, Windows and Linux.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		if r < 32 {
			return -1
		}
		return r
	}, s)
	s = strings.Trim(strings.TrimSpace(s), ".")
	if r := []rune(s); len(r) > 150 {
		s = string(r[:150])
	}
	if s == "" {
		s = "untitled"
	}
	return s
}

// dig walks nested JSON objects by key.
func dig(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// find returns every value stored under key anywhere in v. YouTube moves
// things around its page JSON constantly; searching beats fixed paths.
func find(v any, key string) []any {
	var out []any
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				if k == key {
					out = append(out, c)
				}
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(v)
	return out
}
