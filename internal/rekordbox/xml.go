// Package rekordbox reads a rekordbox collection export (File > Export
// Collection in xml format) and writes playlist XML that rekordbox can import
// through its "rekordbox xml" sidebar.
package rekordbox

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// CollectionTrack is what SuperSync needs to know about a track rekordbox already has.
type CollectionTrack struct {
	ID        string // the library's own ID for the track
	Path      string
	Name      string
	Artist    string
	Cues      int // hot cues + memory cues
	PlayCount int
	Playlists []string

	// The entry exactly as exported, so it can be carried to another file.
	Attrs  []xml.Attr // every TRACK attribute
	Tempos []Elem     // beatgrid (TEMPO)
	Marks  []Elem     // hot cues, memory cues, loops (POSITION_MARK)
}

// Elem is an XML element kept as its raw attributes.
type Elem struct {
	Attrs []xml.Attr `xml:",any,attr"`
}

// Get returns an attribute value, or "".
func (e Elem) Get(name string) string { return getAttr(e.Attrs, name) }

func getAttr(attrs []xml.Attr, name string) string {
	for _, a := range attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// SetAttr replaces (or appends) an attribute.
func SetAttr(attrs []xml.Attr, name, value string) []xml.Attr {
	for i := range attrs {
		if attrs[i].Name.Local == name {
			attrs[i].Value = value
			return attrs
		}
	}
	return append(attrs, xml.Attr{Name: xml.Name{Local: name}, Value: value})
}

// Collection maps normalized file path -> track.
type Collection map[string]*CollectionTrack

type xmlDoc struct {
	XMLName    xml.Name `xml:"DJ_PLAYLISTS"`
	Collection struct {
		Tracks []struct {
			Attrs  []xml.Attr `xml:",any,attr"`
			Tempos []Elem     `xml:"TEMPO"`
			Marks  []Elem     `xml:"POSITION_MARK"`
		} `xml:"TRACK"`
	} `xml:"COLLECTION"`
	Playlists struct {
		Root xmlNode `xml:"NODE"`
	} `xml:"PLAYLISTS"`
}

type xmlNode struct {
	Name   string    `xml:"Name,attr"`
	Type   string    `xml:"Type,attr"`
	Nodes  []xmlNode `xml:"NODE"`
	Tracks []struct {
		Key string `xml:"Key,attr"`
	} `xml:"TRACK"`
}

// ReadCollection parses a rekordbox XML export.
func ReadCollection(path string) (Collection, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var doc xmlDoc
	if err := xml.NewDecoder(f).Decode(&doc); err != nil {
		return nil, fmt.Errorf("not a rekordbox XML export: %w", err)
	}
	c := Collection{}
	byID := map[string]*CollectionTrack{}
	for _, t := range doc.Collection.Tracks {
		p := PathFromLocation(getAttr(t.Attrs, "Location"))
		if p == "" {
			continue
		}
		ct := &CollectionTrack{
			Path: p, Name: getAttr(t.Attrs, "Name"), Artist: getAttr(t.Attrs, "Artist"),
			Attrs: t.Attrs, Tempos: t.Tempos, Marks: t.Marks,
		}
		ct.PlayCount, _ = strconv.Atoi(getAttr(t.Attrs, "PlayCount"))
		ct.Cues = len(t.Marks) // hot cues and memory cues both count as prep work
		c[NormPath(p)] = ct
		byID[getAttr(t.Attrs, "TrackID")] = ct
	}
	var walk func(n xmlNode, prefix string)
	walk = func(n xmlNode, prefix string) {
		name := n.Name
		if prefix != "" {
			name = prefix + " / " + n.Name
		}
		if n.Name == "ROOT" && prefix == "" {
			name = ""
		}
		for _, t := range n.Tracks {
			if ct := byID[t.Key]; ct != nil {
				ct.Playlists = append(ct.Playlists, name)
			}
		}
		for _, ch := range n.Nodes {
			walk(ch, name)
		}
	}
	walk(doc.Playlists.Root, "")
	return c, nil
}

// Lookup finds a collection entry for a local file path.
func (c Collection) Lookup(path string) *CollectionTrack {
	if c == nil {
		return nil
	}
	return c[NormPath(path)]
}

// NormPath makes paths comparable across rekordbox's and the OS's spelling.
func NormPath(p string) string {
	p = filepath.ToSlash(filepath.Clean(p))
	// macOS filesystems and rekordbox may disagree on Unicode normalization and case.
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return norm.NFC.String(p)
}

// PathFromLocation converts "file://localhost/Users/me/Music/a%20b.mp3" (or
// "file://localhost/C:/Music/a.mp3") into a local path.
func PathFromLocation(loc string) string {
	u, err := url.Parse(loc)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	p := u.Path
	// Windows drive letter: "/C:/Music" -> "C:/Music"
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

// LocationFromPath is the inverse of PathFromLocation, encoded the way rekordbox writes it.
func LocationFromPath(p string) string {
	p = filepath.ToSlash(p)
	if len(p) >= 2 && p[1] == ':' { // Windows drive letter
		p = "/" + p
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if i == 1 && len(s) == 2 && s[1] == ':' {
			continue
		}
		segs[i] = url.PathEscape(s)
	}
	return "file://localhost" + strings.Join(segs, "/")
}

// PlaylistTrack is one entry for WritePlaylists.
type PlaylistTrack struct {
	Path     string
	Name     string
	Artist   string
	Album    string
	Duration float64
	Bitrate  int
	Kind     string

	// Optional full rekordbox data (e.g. carried over from another file).
	// Attrs are written first; the fields above override them when set.
	Attrs  []xml.Attr
	Tempos []Elem
	Marks  []Elem
}

type xmlTrack struct {
	Attrs  []xml.Attr `xml:",any,attr"`
	Tempos []Elem     `xml:"TEMPO"`
	Marks  []Elem     `xml:"POSITION_MARK"`
}

func (t PlaylistTrack) element(id int) xmlTrack {
	attrs := append([]xml.Attr(nil), t.Attrs...)
	attrs = SetAttr(attrs, "TrackID", strconv.Itoa(id))
	set := func(name, v string) {
		if v != "" {
			attrs = SetAttr(attrs, name, v)
		}
	}
	set("Name", t.Name)
	set("Artist", t.Artist)
	set("Album", t.Album)
	set("Kind", t.Kind)
	if t.Duration > 0 {
		set("TotalTime", strconv.Itoa(int(t.Duration+0.5)))
	}
	if t.Bitrate > 0 {
		set("BitRate", strconv.Itoa(t.Bitrate))
	}
	attrs = SetAttr(attrs, "Location", LocationFromPath(t.Path))
	return xmlTrack{Attrs: attrs, Tempos: t.Tempos, Marks: t.Marks}
}

// Playlist is a named list of tracks for WritePlaylists.
type Playlist struct {
	Name   string
	Tracks []PlaylistTrack
}

// WritePlaylists writes a rekordbox XML containing the given playlists, placed
// inside a folder named folder (e.g. "SuperSync").
func WritePlaylists(w io.Writer, folder string, lists []Playlist) error {
	type key struct {
		Key int `xml:"Key,attr"`
	}
	type list struct {
		Type    int    `xml:"Type,attr"`
		Name    string `xml:"Name,attr"`
		KeyType int    `xml:"KeyType,attr"`
		Entries int    `xml:"Entries,attr"`
		Tracks  []key  `xml:"TRACK"`
	}
	type folderNode struct {
		Type  int    `xml:"Type,attr"`
		Name  string `xml:"Name,attr"`
		Count int    `xml:"Count,attr"`
		Lists []list `xml:"NODE"`
	}
	type rootNode struct {
		Type   int          `xml:"Type,attr"`
		Name   string       `xml:"Name,attr"`
		Count  int          `xml:"Count,attr"`
		Folder []folderNode `xml:"NODE"`
	}
	type doc struct {
		XMLName xml.Name `xml:"DJ_PLAYLISTS"`
		Version string   `xml:"Version,attr"`
		Product struct {
			Name    string `xml:"Name,attr"`
			Version string `xml:"Version,attr"`
			Company string `xml:"Company,attr"`
		} `xml:"PRODUCT"`
		Collection struct {
			Entries int        `xml:"Entries,attr"`
			Tracks  []xmlTrack `xml:"TRACK"`
		} `xml:"COLLECTION"`
		Playlists struct {
			Root rootNode `xml:"NODE"`
		} `xml:"PLAYLISTS"`
	}

	var d doc
	d.Version = "1.0.0"
	d.Product.Name, d.Product.Version, d.Product.Company = "SuperSync", "1.0", "SuperSync"
	ids := map[string]int{}
	fn := folderNode{Type: 0, Name: folder}
	for _, pl := range lists {
		l := list{Type: 1, Name: pl.Name}
		for _, t := range pl.Tracks {
			id, ok := ids[t.Path]
			if !ok {
				id = len(ids) + 1
				ids[t.Path] = id
				d.Collection.Tracks = append(d.Collection.Tracks, t.element(id))
			}
			l.Tracks = append(l.Tracks, key{id})
		}
		l.Entries = len(l.Tracks)
		fn.Lists = append(fn.Lists, l)
	}
	fn.Count = len(fn.Lists)
	d.Collection.Entries = len(d.Collection.Tracks)
	d.Playlists.Root = rootNode{Type: 0, Name: "ROOT", Count: 1, Folder: []folderNode{fn}}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	return enc.Encode(d)
}

// Kind returns rekordbox's file-kind label for a container format.
func Kind(format string) string {
	switch format {
	case "mp3":
		return "MP3 File"
	case "flac":
		return "FLAC File"
	case "aiff":
		return "AIFF File"
	case "wav":
		return "WAV File"
	case "m4a":
		return "M4A File"
	}
	return ""
}
