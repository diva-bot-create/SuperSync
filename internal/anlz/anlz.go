// Package anlz reads rekordbox's analysis files (ANLZ0000.DAT / .EXT): the
// beatgrid and rekordbox's own colour waveform. Layouts follow the Deep
// Symmetry analysis (https://djl-analysis.deepsymmetry.org) as implemented by
// pyrekordbox.
package anlz

import (
	"encoding/binary"
	"errors"
	"os"
	"strings"
)

// Beat is one beatgrid marker.
type Beat struct {
	Ms  int     `json:"ms"`  // time from the start
	Bar int     `json:"bar"` // position in the bar, 1-4 (1 = downbeat)
	BPM float64 `json:"bpm"` // tempo at this beat
}

// Analysis is what SuperSync shows from a track's rekordbox analysis.
type Analysis struct {
	Grid []Beat `json:"grid,omitempty"`
	// Detail is rekordbox's colour waveform: 150 columns per second, each
	// 16 bits (red, green, blue 3 bits each, height 5 bits).
	Detail []uint16 `json:"-"`
}

// DetailRate is the number of Detail columns per second.
const DetailRate = 150

var errFormat = errors.New("not a rekordbox analysis file")

// Read parses a track's .DAT file and, if present, the matching .EXT file.
func Read(datPath string) (*Analysis, error) {
	a := &Analysis{}
	if err := a.parse(datPath); err != nil {
		return nil, err
	}
	ext := strings.TrimSuffix(datPath, ".DAT") + ".EXT"
	if strings.HasSuffix(datPath, ".dat") {
		ext = strings.TrimSuffix(datPath, ".dat") + ".ext"
	}
	if _, err := os.Stat(ext); err == nil {
		a.parse(ext) // optional: colour waveform
	}
	return a, nil
}

func (a *Analysis) parse(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(b) < 12 || string(b[:4]) != "PMAI" {
		return errFormat
	}
	be := binary.BigEndian
	off := int(be.Uint32(b[4:8]))
	for off+12 <= len(b) {
		typ := string(b[off : off+4])
		hl, tl := int(be.Uint32(b[off+4:])), int(be.Uint32(b[off+8:]))
		if tl < 12 || off+tl > len(b) || hl > tl {
			break
		}
		tag := b[off : off+tl]
		switch typ {
		case "PQTZ":
			if len(tag) >= 24 && len(a.Grid) == 0 {
				n := int(be.Uint32(tag[20:24]))
				for i := 0; i < n && hl+8*(i+1) <= len(tag); i++ {
					e := tag[hl+8*i:]
					a.Grid = append(a.Grid, Beat{
						Bar: int(be.Uint16(e[0:2])), BPM: float64(be.Uint16(e[2:4])) / 100, Ms: int(be.Uint32(e[4:8])),
					})
				}
			}
		case "PWV5":
			if len(tag) >= 24 && be.Uint32(tag[12:16]) == 2 {
				n := int(be.Uint32(tag[16:20]))
				a.Detail = make([]uint16, 0, n)
				for i := 0; i < n && hl+2*(i+1) <= len(tag); i++ {
					a.Detail = append(a.Detail, be.Uint16(tag[hl+2*i:]))
				}
			}
		}
		off += tl
	}
	return nil
}
