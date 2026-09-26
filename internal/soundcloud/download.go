package soundcloud

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotDownloadable means the artist hasn't enabled SoundCloud's download
// button for the track (or its download limit is used up).
var ErrNotDownloadable = errors.New("the artist hasn't enabled downloads for this track")

// CanDownload reports whether SuperSync may download the track: only when
// the artist has turned on SoundCloud's own download button.
func (t *Track) CanDownload() bool { return t.Downloadable && t.DownloadsLeft }

// Progress reports bytes received (total is <= 0 if unknown).
type Progress func(done, total int64)

// Download saves a track the artist allows downloading into dir and returns
// the file path. With the user's own SoundCloud login token it fetches the
// artist's original upload (often WAV); without one, the MP3 stream.
func (c *Client) Download(t *Track, dir, token string, progress Progress) (path string, original bool, err error) {
	if !t.CanDownload() {
		return "", false, ErrNotDownloadable
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	if token != "" {
		p, err := c.downloadOriginal(t, dir, token, progress)
		if err == nil {
			return p, true, nil
		}
		if !errors.Is(err, errUnauthorized) {
			return "", false, err
		}
		// Token expired or wrong: fall back to the stream.
	}
	p, err := c.downloadStream(t, dir, progress)
	return p, false, err
}

var errUnauthorized = errors.New("SoundCloud didn't accept the login token")

func (c *Client) downloadOriginal(t *Track, dir, token string, progress Progress) (string, error) {
	id, err := c.id(false)
	if err != nil {
		return "", err
	}
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/tracks/%d/download?client_id=%s", api, t.ID, id), nil)
	req.Header.Set("Authorization", "OAuth "+strings.TrimPrefix(strings.TrimSpace(token), "OAuth "))
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return "", errUnauthorized
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("SoundCloud download returned HTTP %d", resp.StatusCode)
	}
	var r struct {
		RedirectURI string `json:"redirectUri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || r.RedirectURI == "" {
		return "", errors.New("SoundCloud didn't return a download link")
	}
	return c.fetchFile(r.RedirectURI, dir, baseName(t), "", progress)
}

func (c *Client) downloadStream(t *Track, dir string, progress Progress) (string, error) {
	var prog, hls *transcoding
	for i := range t.transcodings {
		tc := &t.transcodings[i]
		if tc.Snipped || !strings.Contains(tc.Format.MimeType, "mpeg") {
			continue
		}
		switch tc.Format.Protocol {
		case "progressive":
			prog = tc
		case "hls":
			if hls == nil {
				hls = tc
			}
		}
	}
	tc := prog
	if tc == nil {
		tc = hls
	}
	if tc == nil {
		return "", errors.New("SoundCloud offers no MP3 stream for this track; use the download button on its page")
	}
	q := url.Values{}
	if t.trackAuth != "" {
		q.Set("track_authorization", t.trackAuth)
	}
	var loc struct {
		URL string `json:"url"`
	}
	if err := c.getURL(tc.URL, q, &loc); err != nil {
		return "", err
	}
	if loc.URL == "" {
		return "", errors.New("SoundCloud didn't return a stream link")
	}
	if tc.Format.Protocol == "progressive" {
		return c.fetchFile(loc.URL, dir, baseName(t), ".mp3", progress)
	}
	return c.fetchHLS(loc.URL, dir, baseName(t), progress)
}

// getURL is get() for a full API URL (as given in transcodings).
func (c *Client) getURL(full string, q url.Values, out any) error {
	u, err := url.Parse(full)
	if err != nil {
		return err
	}
	for k, v := range u.Query() {
		q[k] = v
	}
	return c.get(u.Path, q, out, u.Scheme+"://"+u.Host)
}

func baseName(t *Track) string {
	name := t.Title
	if t.Artist != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(t.Artist)) && !strings.Contains(name, " - ") {
		name = t.Artist + " - " + name
	}
	return SafeFilename(name)
}

// SafeFilename strips characters that aren't allowed in file names on any OS.
func SafeFilename(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 32 {
			return '-'
		}
		return r
	}, s)
	s = strings.Trim(strings.TrimSpace(s), ".")
	if len([]rune(s)) > 150 {
		s = string([]rune(s)[:150])
	}
	if s == "" {
		s = "track"
	}
	return s
}

func uniquePath(p string) string {
	if _, err := os.Stat(p); err != nil {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 2; ; i++ {
		c := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(c); err != nil {
			return c
		}
	}
}

// fetchFile downloads u to dir/name.<ext>; ext comes from the server's file
// name or content type unless given.
func (c *Client) fetchFile(u, dir, name, ext string, progress Progress) (string, error) {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: 15 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	if ext == "" {
		ext = extFor(resp)
	}
	dst := uniquePath(filepath.Join(dir, name+ext))
	return dst, saveBody(resp.Body, dst, resp.ContentLength, progress)
}

func extFor(resp *http.Response) string {
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		if e := strings.ToLower(filepath.Ext(params["filename"])); e != "" {
			return e
		}
	}
	if e := strings.ToLower(path.Ext(resp.Request.URL.Path)); e != "" && len(e) <= 5 {
		return e
	}
	switch ct := resp.Header.Get("Content-Type"); {
	case strings.Contains(ct, "wav"):
		return ".wav"
	case strings.Contains(ct, "aiff"):
		return ".aiff"
	case strings.Contains(ct, "flac"):
		return ".flac"
	case strings.Contains(ct, "mp4"), strings.Contains(ct, "m4a"), strings.Contains(ct, "aac"):
		return ".m4a"
	}
	return ".mp3"
}

func saveBody(r io.Reader, dst string, total int64, progress Progress) error {
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	var done int64
	buf := make([]byte, 256*1024)
	last := time.Now()
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				os.Remove(tmp)
				return err
			}
			done += int64(n)
			if progress != nil && time.Since(last) > 150*time.Millisecond {
				progress(done, total)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(tmp)
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if progress != nil {
		progress(done, done)
	}
	return os.Rename(tmp, dst)
}

// fetchHLS joins an MP3 HLS playlist's segments into one file.
func (c *Client) fetchHLS(u, dir, name string, progress Progress) (string, error) {
	list, err := c.fetch(u)
	if err != nil {
		return "", err
	}
	base, _ := url.Parse(u)
	var segs []string
	sc := bufio.NewScanner(strings.NewReader(list))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			if strings.HasPrefix(line, "#EXT-X-KEY") && !strings.Contains(line, "METHOD=NONE") {
				return "", errors.New("this stream is encrypted; use the download button on its page")
			}
			continue
		}
		ref, err := base.Parse(line)
		if err != nil {
			return "", err
		}
		segs = append(segs, ref.String())
	}
	if len(segs) == 0 {
		return "", errors.New("empty stream")
	}
	pr, pw := io.Pipe()
	go func() {
		for _, s := range segs {
			req, _ := http.NewRequest("GET", s, nil)
			req.Header.Set("User-Agent", userAgent)
			resp, err := c.HTTP.Do(req)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			_, err = io.Copy(pw, resp.Body)
			resp.Body.Close()
			if err != nil {
				pw.CloseWithError(err)
				return
			}
		}
		pw.Close()
	}()
	dst := uniquePath(filepath.Join(dir, name+".mp3"))
	return dst, saveBody(pr, dst, -1, progress)
}
