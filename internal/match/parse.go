// Package match decides whether two track descriptions refer to the same
// recording, tolerating the mess found in SoundCloud titles and in the
// filenames and tags that downloaders produce.
package match

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Key is one normalized reading of a track: artist tokens, core title tokens
// (without version/junk annotations) and the version signature, e.g. the
// remixer's name for "(Someone Remix)".
type Key struct {
	Artist  []string `json:"a"`
	Title   []string `json:"t"`
	Version []string `json:"v,omitempty"`
	// Swapped marks the "Title - Artist" reading of a dashed title.
	Swapped bool `json:"-"`
	// Different lengths are normal (radio vs extended), but a big gap is a hint.
	Duration float64 `json:"-"`
}

// Words that describe a version without making it a different recording.
var genericVersion = set("original", "extended", "radio", "club", "mix", "version", "remaster",
	"remastered", "clean", "dirty", "explicit", "main", "edit", "pro", "short", "long", "full",
	"master", "mixed", "album", "single", "vocal", "orig", "ext", "rmx", "remix", "stereo", "mono",
	"hq", "hd", "lossless", "wav", "mp3", "flac", "320", "320kbps", "24bit")

// Words that mark a bracketed group as version info worth keeping.
var versionWords = set("remix", "rmx", "edit", "mix", "bootleg", "vip", "flip", "rework", "dub",
	"version", "refix", "remake", "mashup", "extended", "radio", "instrumental", "acapella",
	"acappella", "live", "remaster", "remastered", "cover", "reprise", "interlude", "intro",
	"outro", "rerub", "reedit", "re", "blend", "amapiano", "techno", "house", "remode")

var stopwords = set("the", "a", "an", "feat", "ft", "featuring", "and", "x", "vs", "with", "prod",
	"by", "presents", "pres")

// Phrases that are promotional noise, removed wherever they appear.
var junkRe = regexp.MustCompile(`(?i)\b(free\s*(download|dl)|out\s*now|premiere|exclusive|` +
	`buy\s*=\s*free(\s*(download|dl))?|click\s*buy|download\s*link|free\s*track|` +
	`supported\s*by\s*[^\]\)]*|official\s*(audio|video|music video)|lyric\s*video|` +
	`hi[\s-]?res|clip|visuali[sz]er|\d+\s*k\s*(followers|special)|creative\s*commons|` +
	`no\s*copyright(\s*music)?|copyright\s*free|royalty\s*free|(background\s+)?music\s+for\s+(youtube\s+)?videos|ncs\s*release)\b`)

var bracketRe = regexp.MustCompile(`[\(\[\{【（][^\)\]\}】）]*[\)\]\}】）]`)
var featRe = regexp.MustCompile(`(?i)\s(feat\.?|ft\.?|featuring)\s.*$`)
var dashRe = regexp.MustCompile(`\s+[-–—~]\s+|\s[-–—]|[-–—]\s`)
var trackNumRe = regexp.MustCompile(`^\s*(\d{1,3}|[a-d]\d)\s*[\.\-_)]\s*`)
var nonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)

var foldTransform = transform.Chain(norm.NFKD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

// Tokens lowercases, strips accents and punctuation, and drops stopwords.
func Tokens(s string) []string {
	s, _, _ = transform.String(foldTransform, s)
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "'", "")
	s = strings.ReplaceAll(s, "’", "")
	s = strings.ReplaceAll(s, "$", "s")
	var out []string
	for _, w := range strings.Fields(s) {
		for _, w := range nonWord.Split(deLeet(w), -1) {
			if w != "" && !stopwords[w] {
				out = append(out, w)
			}
		}
	}
	return out
}

var ordinalRe = regexp.MustCompile(`^\d+(st|nd|rd|th|s|am|pm|k|hz|bpm|x|d|ep|lp)$`)

var leet = strings.NewReplacer("0", "o", "1", "i", "3", "e", "4", "a", "5", "s", "7", "t", "@", "a")

// deLeet undoes the digit-for-letter spelling ("H0t 1n H3r3", "@tm05ph3r3")
// that bootleg uploads use to dodge copyright filters. A word is only decoded
// when it mixes letters with substitutes, so "2026", "808", "B2B" and "1st"
// are left alone.
func deLeet(w string) string {
	if ordinalRe.MatchString(w) {
		return w
	}
	letters, subs := 0, 0
	for _, r := range w {
		switch {
		case strings.ContainsRune("013457@", r):
			subs++
		case unicode.IsLetter(r):
			letters++
		}
	}
	if subs == 0 || letters == 0 {
		return w
	}
	return leet.Replace(w)
}

// Parse builds the plausible readings of a track from an artist hint (tag
// artist, uploader name) and a raw title that may itself contain
// "Artist - Title", promo junk, labels, and version annotations.
func Parse(artistHint, rawTitle string) []Key {
	title := cleanTitle(rawTitle)
	var keys []Key

	// "Tech House | Artist - Title", "Artist - Title | Label", "Club Remix | Title":
	// drop segments that are only genre/promo words; treat any remaining bar as a dash.
	if strings.Contains(title, "|") {
		var keep []string
		for _, seg := range strings.Split(title, "|") {
			if !onlyFiller(seg) {
				keep = append(keep, strings.TrimSpace(seg))
			}
		}
		for _, seg := range keep {
			if dashRe.MatchString(seg) {
				keep = []string{seg}
				break
			}
		}
		title = strings.Join(keep, " - ")
	}

	if loc := dashRe.FindStringIndex(title); loc != nil {
		a, t := title[:loc[0]], title[loc[1]:]
		if strings.TrimSpace(a) != "" && strings.TrimSpace(t) != "" {
			k := makeKey(a, t)
			keys = append(keys, k)
			// Some uploads are "Title - Artist".
			sw := makeKey(t, a)
			sw.Swapped = true
			keys = append(keys, sw)
			// Or the dash is part of the title and the hint is the artist.
			if artistHint != "" {
				keys = append(keys, makeKey(artistHint, title))
			}
			return dedupe(keys)
		}
	}
	k := makeKey(artistHint, title)
	keys = append(keys, k)
	// The hint is often a channel or label, not the artist. Allow an
	// artist-less reading when the title alone is distinctive.
	if len(k.Artist) > 0 && (len(k.Version) > 0 || len(k.Title) >= 3) {
		keys = append(keys, Key{Title: k.Title, Version: k.Version})
	}
	return dedupe(keys)
}

var fillerWords = set("house", "tech", "techno", "bass", "deep", "afro", "melodic", "progressive",
	"minimal", "club", "remix", "remixes", "edit", "edits", "bootleg", "bootlegs", "mashup", "mashups",
	"mushups", "free", "dl", "download", "premiere", "exclusive", "out", "now", "dnb", "drum", "garage",
	"ukg", "uk", "dubstep", "trance", "disco", "nu", "music", "electronic", "dance", "edm", "hardstyle",
	"jersey", "baile", "funk", "amapiano", "jungle", "breaks", "ibiza", "hard", "latin", "organic",
	"official", "audio", "video", "visualizer", "visualiser", "lyric", "lyrics", "hd", "hq", "4k", "stream")

func onlyFiller(seg string) bool {
	for _, w := range Tokens(junkRe.ReplaceAllString(seg, " ")) {
		if !fillerWords[w] {
			return false
		}
	}
	return true
}

// ParseFilename reads "Artist - Title" style filenames (extension already removed).
func ParseFilename(name string) []Key {
	name = trackNumRe.ReplaceAllString(name, "")
	name = strings.ReplaceAll(name, "_", " ")
	return Parse("", name)
}

func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	// "PREMIERE: ..." / "Premiere | ..." prefixes.
	s = regexp.MustCompile(`(?i)^\s*(premiere|exclusive|free\s*(dl|download)|out\s*now)\s*[:|\-–—]\s*`).ReplaceAllString(s, "")
	return s
}

func makeKey(artist, title string) Key {
	var version []string
	// Bracketed groups: keep version info, move "feat." to artist, drop everything else.
	var feat []string
	strip := func(str string) string {
		return bracketRe.ReplaceAllStringFunc(str, func(g string) string {
			_, open := utf8.DecodeRuneInString(g)
			_, close := utf8.DecodeLastRuneInString(g)
			inner := g[open : len(g)-close]
			toks := Tokens(inner)
			if hasAny(toks, versionWords) {
				version = append(version, toks...)
				return " "
			}
			low := strings.ToLower(strings.TrimSpace(inner))
			if strings.HasPrefix(low, "feat") || strings.HasPrefix(low, "ft") || strings.HasPrefix(low, "with ") {
				feat = append(feat, inner)
			}
			return " "
		})
	}
	title = strip(title)
	artist = strip(artist) + " " + strings.Join(feat, " ")
	// Unbracketed "Title - Someone Remix".
	for {
		loc := dashRe.FindAllStringIndex(title, -1)
		if len(loc) == 0 {
			break
		}
		last := loc[len(loc)-1]
		tail := Tokens(title[last[1]:])
		if !hasAny(tail, versionWords) {
			break
		}
		version = append(version, tail...)
		title = title[:last[0]]
	}
	if m := featRe.FindStringIndex(title); m != nil {
		artist += " " + title[m[0]:]
		title = title[:m[0]]
	}
	title = junkRe.ReplaceAllString(title, " ")
	artist = junkRe.ReplaceAllString(artist, " ")

	k := Key{Artist: Tokens(artist), Title: Tokens(title)}
	for _, v := range version {
		if !genericVersion[v] {
			k.Version = append(k.Version, v)
		}
	}
	// Signal "is a remix" even if the remixer tokens got stripped as generic.
	if hasAny(version, set("remix", "rmx", "bootleg", "vip", "flip", "rework", "refix", "mashup", "dub", "instrumental", "acapella", "acappella", "live")) {
		for _, v := range version {
			if v == "remix" || v == "rmx" {
				k.Version = append(k.Version, "remix")
				break
			}
		}
	}
	k.Version = uniq(k.Version)
	return k
}

func dedupe(keys []Key) []Key {
	var out []Key
	seen := map[string]bool{}
	for _, k := range keys {
		if len(k.Title) == 0 {
			continue
		}
		id := strings.Join(k.Artist, " ") + "|" + strings.Join(k.Title, " ") + "|" + strings.Join(k.Version, " ")
		if !seen[id] {
			seen[id] = true
			out = append(out, k)
		}
	}
	return out
}

func set(ws ...string) map[string]bool {
	m := make(map[string]bool, len(ws))
	for _, w := range ws {
		m[w] = true
	}
	return m
}

func hasAny(toks []string, m map[string]bool) bool {
	for _, t := range toks {
		if m[t] {
			return true
		}
	}
	return false
}

func uniq(ws []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range ws {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// SearchQuery is a clean "artist title version" string for store searches.
// Artists made only of genre/promo words ("Tech House") are left out.
func SearchQuery(keys []Key) string {
	var pick *Key
	for i := range keys {
		k := &keys[i]
		if k.Swapped || len(k.Title) == 0 {
			continue
		}
		if pick == nil {
			pick = k
		}
		if len(k.Artist) > 0 && !onlyFiller(strings.Join(k.Artist, " ")) {
			pick = k
			break
		}
	}
	if pick == nil {
		return ""
	}
	var parts []string
	if !onlyFiller(strings.Join(pick.Artist, " ")) {
		parts = append(parts, pick.Artist...)
	}
	parts = append(parts, pick.Title...)
	parts = append(parts, pick.Version...)
	return strings.Join(parts, " ")
}

// ArtistTitle splits an upload's title into artist and title the way
// uploaders write them ("Artist - Title", "Tech House | Artist - Title [FREE DL]",
// "PREMIERE: Artist - Title"), cleaned up for display: promo and genre tags are
// dropped, version info like "(Extended Mix)" and "(feat. X)" is kept. With no
// "Artist - Title" dash, fallback (the publisher's artist, uploader or channel)
// is the artist.
func ArtistTitle(raw, fallback string) (artist, title string) {
	s := cleanTitle(raw)
	if strings.Contains(s, "|") {
		var keep []string
		for _, seg := range strings.Split(s, "|") {
			if !onlyFiller(seg) {
				keep = append(keep, strings.TrimSpace(seg))
			}
		}
		for _, seg := range keep {
			if dashRe.MatchString(seg) {
				keep = []string{seg}
				break
			}
		}
		s = strings.Join(keep, " - ")
	}
	// The uploader's display name: "MediaCharger - Music For YouTube Videos" -> "MediaCharger".
	fb := strings.TrimSpace(fallback)
	if loc := dashRe.FindStringIndex(fb); loc != nil && loc[0] > 0 {
		fb = fb[:loc[0]]
	}
	fb = displayClean(fb)
	channel := set(Tokens(fallback)...)

	// Dash segments: drop promo-only ones, and ones that just name the
	// uploader ("Song - Mediacharger", "Waitz - Track"), remembering that the
	// uploader is then the artist.
	segs := dashRe.Split(s, -1)
	var keep []string
	named := false
	for _, seg := range segs {
		c := displayClean(seg)
		if c == "" || onlyFiller(c) {
			continue
		}
		if len(segs) > 1 && subsetOf(Tokens(c), channel) {
			named = true
			continue
		}
		keep = append(keep, c)
	}
	switch {
	case len(keep) == 0:
		artist, title = fb, displayClean(s)
	case named || len(keep) == 1:
		artist, title = fb, strings.Join(keep, " - ")
	default:
		artist, title = keep[0], strings.Join(keep[1:], " - ")
	}
	if artist == "" {
		artist = fb
	}
	if title == "" {
		title = strings.TrimSpace(raw)
	}
	return artist, title
}

func subsetOf(toks []string, m map[string]bool) bool {
	if len(toks) == 0 || len(m) == 0 {
		return false
	}
	for _, t := range toks {
		if !m[t] {
			return false
		}
	}
	return true
}

var starRe = regexp.MustCompile(`\*+`)
var spaceRe = regexp.MustCompile(`\s{2,}`)

// displayClean removes promo brackets and phrases but keeps the casing and
// any version or featuring brackets.
func displayClean(s string) string {
	s = bracketRe.ReplaceAllStringFunc(s, func(g string) string {
		_, open := utf8.DecodeRuneInString(g)
		_, close := utf8.DecodeLastRuneInString(g)
		inner := strings.TrimSpace(g[open : len(g)-close])
		// Drop brackets that are only promo ("[Free Download]", "(Official Audio)",
		// "[OUT NOW]"); keep everything else, including parts of the real title.
		if onlyFiller(inner) || strings.TrimSpace(nonWord.ReplaceAllString(inner, "")) == "" {
			return " "
		}
		return " (" + inner + ")"
	})
	s = junkRe.ReplaceAllString(s, " ")
	s = starRe.ReplaceAllString(s, " ")
	s = spaceRe.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "( ", "(")
	return strings.Trim(strings.TrimSpace(s), "-–—|:~ ")
}
