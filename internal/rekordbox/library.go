package rekordbox

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Library is a whole rekordbox XML document kept in memory: the collection
// and the playlist tree. SuperSync uses it as its own library when rekordbox
// isn't installed (rekordbox can import the file later).
type Library struct {
	Path   string
	Tracks []*LibTrack
	Root   *LibNode
	nextID int
}

type LibTrack struct {
	ID     string
	Attrs  []xml.Attr
	Tempos []Elem
	Marks  []Elem
}

func (t *LibTrack) Get(name string) string { return getAttr(t.Attrs, name) }
func (t *LibTrack) Path() string           { return PathFromLocation(t.Get("Location")) }

// LibNode is a folder (Folder=true) or playlist. IDs are positional paths
// ("0/2/1") so they stay stable as long as the tree isn't reordered.
type LibNode struct {
	ID       string
	Name     string
	Folder   bool
	Keys     []string // track IDs
	Children []*LibNode
}

type libDoc struct {
	XMLName    xml.Name `xml:"DJ_PLAYLISTS"`
	Version    string   `xml:"Version,attr"`
	Product    *Elem    `xml:"PRODUCT"`
	Collection struct {
		Entries int        `xml:"Entries,attr"`
		Tracks  []xmlTrack `xml:"TRACK"`
	} `xml:"COLLECTION"`
	Playlists struct {
		Root libXMLNode `xml:"NODE"`
	} `xml:"PLAYLISTS"`
}

type libXMLNode struct {
	Type    int          `xml:"Type,attr"`
	Name    string       `xml:"Name,attr"`
	Count   *int         `xml:"Count,attr,omitempty"`
	KeyType *int         `xml:"KeyType,attr,omitempty"`
	Entries *int         `xml:"Entries,attr,omitempty"`
	Nodes   []libXMLNode `xml:"NODE"`
	Tracks  []struct {
		Key string `xml:"Key,attr"`
	} `xml:"TRACK"`
}

// LoadLibrary reads a rekordbox XML file, or returns an empty library if it doesn't exist.
func LoadLibrary(path string) (*Library, error) {
	l := &Library{Path: path, Root: &LibNode{ID: "root", Name: "ROOT", Folder: true}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	} else if err != nil {
		return nil, err
	}
	var d libDoc
	if err := xml.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%s isn't a rekordbox XML library: %w", path, err)
	}
	for _, t := range d.Collection.Tracks {
		lt := &LibTrack{ID: getAttr(t.Attrs, "TrackID"), Attrs: t.Attrs, Tempos: t.Tempos, Marks: t.Marks}
		l.Tracks = append(l.Tracks, lt)
		if n, err := strconv.Atoi(lt.ID); err == nil && n >= l.nextID {
			l.nextID = n + 1
		}
	}
	var conv func(x libXMLNode, id string) *LibNode
	conv = func(x libXMLNode, id string) *LibNode {
		n := &LibNode{ID: id, Name: x.Name, Folder: x.Type == 0}
		for _, t := range x.Tracks {
			n.Keys = append(n.Keys, t.Key)
		}
		for i, c := range x.Nodes {
			cid := strconv.Itoa(i)
			if id != "root" {
				cid = id + "/" + cid
			}
			n.Children = append(n.Children, conv(c, cid))
		}
		return n
	}
	l.Root = conv(d.Playlists.Root, "root")
	return l, nil
}

// Node finds a node by ID.
func (l *Library) Node(id string) *LibNode {
	var find func(n *LibNode) *LibNode
	find = func(n *LibNode) *LibNode {
		if n.ID == id {
			return n
		}
		for _, c := range n.Children {
			if f := find(c); f != nil {
				return f
			}
		}
		return nil
	}
	return find(l.Root)
}

// Track finds a track by ID.
func (l *Library) Track(id string) *LibTrack {
	for _, t := range l.Tracks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// AddTrack adds a file (or returns the existing entry for that path).
func (l *Library) AddTrack(pt PlaylistTrack) *LibTrack {
	for _, t := range l.Tracks {
		if NormPath(t.Path()) == NormPath(pt.Path) {
			return t
		}
	}
	if l.nextID == 0 {
		l.nextID = 1
	}
	id := strconv.Itoa(l.nextID)
	l.nextID++
	t := &LibTrack{ID: id}
	x := pt.element(0)
	t.Attrs = SetAttr(x.Attrs, "TrackID", id)
	t.Tempos, t.Marks = x.Tempos, x.Marks
	l.Tracks = append(l.Tracks, t)
	return t
}

// Child finds or creates a folder/playlist under parent.
func (l *Library) Child(parent *LibNode, name string, folder bool) *LibNode {
	for _, c := range parent.Children {
		if c.Name == name && c.Folder == folder {
			return c
		}
	}
	// An ID no sibling has (deleting a playlist leaves gaps).
	id := ""
	for i := len(parent.Children); ; i++ {
		id = strconv.Itoa(i)
		if parent.ID != "root" {
			id = parent.ID + "/" + id
		}
		if l.Node(id) == nil {
			break
		}
	}
	n := &LibNode{ID: id, Name: name, Folder: folder}
	parent.Children = append(parent.Children, n)
	return n
}

// Parent is the node containing id (nil for the root or an unknown id).
func (l *Library) Parent(id string) *LibNode {
	var find func(n *LibNode) *LibNode
	find = func(n *LibNode) *LibNode {
		for _, c := range n.Children {
			if c.ID == id {
				return n
			}
			if f := find(c); f != nil {
				return f
			}
		}
		return nil
	}
	return find(l.Root)
}

// Delete removes a playlist or folder (tracks stay in the collection).
func (l *Library) Delete(id string) bool {
	p := l.Parent(id)
	if p == nil {
		return false
	}
	for i, c := range p.Children {
		if c.ID == id {
			p.Children = append(p.Children[:i], p.Children[i+1:]...)
			return true
		}
	}
	return false
}

// Save writes the library atomically.
func (l *Library) Save() error {
	var d libDoc
	d.Version = "1.0.0"
	d.Product = &Elem{Attrs: []xml.Attr{{Name: xml.Name{Local: "Name"}, Value: "SuperSync"}, {Name: xml.Name{Local: "Version"}, Value: "1.0"}, {Name: xml.Name{Local: "Company"}, Value: "SuperSync"}}}
	for _, t := range l.Tracks {
		d.Collection.Tracks = append(d.Collection.Tracks, xmlTrack{Attrs: t.Attrs, Tempos: t.Tempos, Marks: t.Marks})
	}
	d.Collection.Entries = len(l.Tracks)
	var conv func(n *LibNode) libXMLNode
	conv = func(n *LibNode) libXMLNode {
		x := libXMLNode{Name: n.Name}
		if n.Folder {
			c := len(n.Children)
			x.Type, x.Count = 0, &c
			for _, ch := range n.Children {
				x.Nodes = append(x.Nodes, conv(ch))
			}
		} else {
			k, e := 0, len(n.Keys)
			x.Type, x.KeyType, x.Entries = 1, &k, &e
			for _, key := range n.Keys {
				x.Tracks = append(x.Tracks, struct {
					Key string `xml:"Key,attr"`
				}{key})
			}
		}
		return x
	}
	d.Playlists.Root = conv(l.Root)
	d.Playlists.Root.Name = "ROOT"
	var sb strings.Builder
	sb.WriteString(xml.Header)
	enc := xml.NewEncoder(&sb)
	enc.Indent("", "  ")
	if err := enc.Encode(d); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o755); err != nil {
		return err
	}
	tmp := l.Path + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.Path)
}
