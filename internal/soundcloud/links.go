package soundcloud

import (
	"net/url"
	"regexp"
	"strings"
)

// LinkKind says what a link offers.
type LinkKind string

const (
	FreeDownload LinkKind = "download" // download gate or file host
	Store        LinkKind = "buy"      // a store selling the track
)

// Link is a download or purchase link found on a track.
type Link struct {
	URL   string   `json:"url"`
	Kind  LinkKind `json:"kind"`
	Label string   `json:"label"`           // e.g. "hypeddit.com", "Bandcamp"
	From  string   `json:"from"`            // "buy button" or "description"
	Free  bool     `json:"free"`            // store link that's labelled free (e.g. Bandcamp name-your-price)
	Title string   `json:"title,omitempty"` // the buy button's own text
}

// Hosts that hand out a file, directly or after a follow/like "gate".
var downloadHosts = map[string]string{
	"hypeddit.com": "Hypeddit", "toneden.io": "ToneDen", "hive.co": "Hive", "gate.fm": "Gate.fm",
	"show.co": "Show.co", "click.dj": "Click.dj", "download-gate.com": "Download Gate",
	"dropbox.com": "Dropbox", "drive.google.com": "Google Drive", "wetransfer.com": "WeTransfer",
	"we.tl": "WeTransfer", "mega.nz": "MEGA", "mediafire.com": "MediaFire", "box.com": "Box",
	"1drv.ms": "OneDrive", "onedrive.live.com": "OneDrive", "pcloud.com": "pCloud",
	"filemail.com": "Filemail", "disk.yandex.ru": "Yandex Disk", "sendspace.com": "Sendspace",
	"icloud.com": "iCloud", "fanlink.tv": "Fanlink", "droploud.com": "Droploud", "joyfeed.co": "Joyfeed",
	"slammes.com": "Slammes", "dpbr.me": "DPBR", "theartistunion.com": "Artist Union",
	"datatransmission.co": "Data Transmission", "supportify.ch": "Supportify", "pumpyoursound.com": "PumpYourSound",
}

var storeHosts = map[string]string{
	"beatport.com": "Beatport", "bandcamp.com": "Bandcamp", "traxsource.com": "Traxsource",
	"junodownload.com": "Juno Download", "bleep.com": "Bleep", "qobuz.com": "Qobuz",
	"music.apple.com": "Apple Music", "itunes.apple.com": "iTunes", "7digital.com": "7digital",
	"boomkat.com": "Boomkat", "beatsource.com": "Beatsource", "hardwax.com": "Hard Wax",
	"decks.de": "Decks", "whatpeopleplay.com": "WhatPeoplePlay",
}

// Places links go that aren't about getting the track.
var noiseHosts = []string{
	"instagram.com", "facebook.com", "fb.com", "twitter.com", "x.com", "tiktok.com", "youtube.com",
	"youtu.be", "spotify.com", "open.spotify.com", "soundcloud.com", "on.soundcloud.com",
	"snapchat.com", "twitch.tv", "discord.gg", "discord.com", "patreon.com", "linktr.ee",
	"docs.google.com/forms", "forms.gle", "mixcloud.com", "residentadvisor.net", "ra.co",
}

var urlRe = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>"'()\[\]{}]+` +
	`|\b(?:[a-z0-9-]+\.)*(?:hypeddit\.com|toneden\.io|bandcamp\.com|beatport\.com|dropbox\.com|we\.tl|wetransfer\.com|traxsource\.com|click\.dj)/[^\s<>"'()\[\]{}]+`)

// Pages on download sites that aren't a download (sign-ups, promo, socials).
var sitePage = regexp.MustCompile(`(?i)://[^/]+/(subscribe|spotify|apple(music)?|instagram|tiktok|youtube|twitter|facebook|promo|promote|submit|submissions?|contact|about|pricing|login|signup|register)(/|\?|$)|\.php(\?|$)`)

// Store pages about an artist or label rather than a track or release.
var storeProfile = regexp.MustCompile(`(?i)://[^/]+/(artist|label|labels|artists|chart|charts)/|://[^/]+\.bandcamp\.com/?(\?|$)`)

var freeWords = regexp.MustCompile(`(?i)free|download|\bdl\b|name your price|\bnyp\b`)

// ExtractLinks collects download and purchase links from a track's buy button
// and description, most useful first, without duplicates.
func ExtractLinks(description, purchaseURL, purchaseTitle string) []Link {
	var out []Link
	seen := map[string]bool{}
	add := func(raw, from, context string) {
		u, host, ok := normalize(raw)
		if !ok || seen[u] {
			return
		}
		l := Link{URL: u, From: from}
		switch {
		case isNoise(u, host):
			return
		case lookup(host, downloadHosts) != "" && sitePage.MatchString(u):
			return // the site's own promo/sign-up pages, not a download
		case lookup(host, downloadHosts) != "":
			l.Kind, l.Label = FreeDownload, lookup(host, downloadHosts)
		case lookup(host, storeHosts) != "" && storeProfile.MatchString(u):
			return // an artist or label page, not this track
		case lookup(host, storeHosts) != "":
			l.Kind, l.Label = Store, lookup(host, storeHosts)
			l.Free = freeWords.MatchString(context)
		case from == "buy button":
			// Unknown site behind the buy button: trust its label.
			l.Kind, l.Label = Store, host
			if freeWords.MatchString(context) {
				l.Kind = FreeDownload
			}
		case freeWords.MatchString(context):
			l.Kind, l.Label = FreeDownload, host // unknown host next to "free download"
		default:
			return
		}
		seen[u] = true
		out = append(out, l)
	}
	if purchaseURL != "" {
		add(purchaseURL, "buy button", purchaseTitle)
		if len(out) > 0 {
			out[0].Title = strings.TrimSpace(purchaseTitle)
		}
	}
	for _, line := range strings.Split(description, "\n") {
		for _, m := range urlRe.FindAllString(line, -1) {
			add(m, "description", line)
		}
	}
	// Free downloads first, then stores; keep discovery order otherwise.
	sorted := make([]Link, 0, len(out))
	for _, pass := range []func(Link) bool{
		func(l Link) bool { return l.Kind == FreeDownload },
		func(l Link) bool { return l.Kind == Store && l.Free },
		func(l Link) bool { return l.Kind == Store && !l.Free },
	} {
		for _, l := range out {
			if pass(l) {
				sorted = append(sorted, l)
			}
		}
	}
	return sorted
}

func normalize(raw string) (full, host string, ok bool) {
	raw = strings.TrimRight(raw, ".,;:!?…'\"")
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || !strings.Contains(u.Host, ".") {
		return "", "", false
	}
	// Drop tracking parameters.
	q := u.Query()
	for k := range q {
		if strings.HasPrefix(k, "utm_") || k == "si" || k == "ref" {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	host = strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	return u.String(), host, true
}

// lookup matches host or any parent domain ("artist.bandcamp.com").
func lookup(host string, m map[string]string) string {
	for h := host; h != ""; {
		if v, ok := m[h]; ok {
			return v
		}
		i := strings.IndexByte(h, '.')
		if i < 0 {
			break
		}
		h = h[i+1:]
	}
	return ""
}

func isNoise(u, host string) bool {
	rest := strings.SplitN(strings.SplitN(u, "://", 2)[1], "?", 2)[0]
	rest = strings.TrimPrefix(strings.ToLower(rest), "www.")
	for _, n := range noiseHosts {
		if host == n || strings.HasSuffix(host, "."+n) || strings.HasPrefix(rest, n) {
			return true
		}
	}
	return false
}
