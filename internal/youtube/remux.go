package youtube

import (
	"encoding/binary"
	"errors"
)

// Remux turns YouTube's fragmented MP4 (moov with no samples, then
// moof+mdat pairs) into a plain .m4a: one moov with a full sample table,
// one mdat, and iTunes-style title/artist tags. The audio bytes are copied
// untouched. Input that isn't fragmented is returned as is.
func Remux(in []byte, title, artist string) ([]byte, error) {
	t, err := demux(in)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return in, nil
	}
	return t.mp4(title, artist), nil
}

// track is one audio track's codec description and samples, in order.
type track struct {
	stsd      []byte // the whole stsd box, copied into the output as is
	asc       []byte // AudioSpecificConfig, what the AAC decoder needs
	timescale uint32
	sizes     []uint32
	durs      []uint32
	data      []byte // the samples back to back
}

// demux reads the samples out of a fragmented MP4. It returns nil, nil for
// input that isn't fragmented.
func demux(in []byte) (*track, error) {
	top, err := boxes(in, 0, len(in))
	if err != nil {
		return nil, err
	}
	var moov []box
	var moofs []box
	for _, b := range top {
		switch b.typ {
		case "moov":
			if moov, err = boxes(in, b.body, b.end); err != nil {
				return nil, err
			}
		case "moof":
			moofs = append(moofs, b)
		}
	}
	if moov == nil {
		return nil, errors.New("no moov box")
	}
	if len(moofs) == 0 {
		return nil, nil
	}

	// Track setup: timescale and codec description from the (empty) moov.
	trak, ok := child(in, moov, "trak")
	if !ok {
		return nil, errors.New("no audio track")
	}
	stsd, ok := path(in, trak, "mdia", "minf", "stbl", "stsd")
	if !ok {
		return nil, errors.New("no sample description")
	}
	mdhd, ok := path(in, trak, "mdia", "mdhd")
	if !ok {
		return nil, errors.New("no mdhd")
	}
	var timescale uint32
	if in[mdhd.body] == 1 {
		timescale = be32(in, mdhd.body+20)
	} else {
		timescale = be32(in, mdhd.body+12)
	}
	var defDur, defSize uint32
	if mvex, ok := child(in, moov, "mvex"); ok {
		if trex, ok := path(in, mvex, "trex"); ok {
			defDur, defSize = be32(in, trex.body+12), be32(in, trex.body+16)
		}
	}

	// Samples, in order, from every fragment.
	var sizes, durs []uint32
	var data []byte
	for _, moof := range moofs {
		kids, err := boxes(in, moof.body, moof.end)
		if err != nil {
			return nil, err
		}
		for _, traf := range kids {
			if traf.typ != "traf" {
				continue
			}
			tk, err := boxes(in, traf.body, traf.end)
			if err != nil {
				return nil, err
			}
			base, dDur, dSize := moof.start, defDur, defSize
			for _, b := range tk {
				if b.typ != "tfhd" {
					continue
				}
				flags := be32(in, b.body) & 0xffffff
				o := b.body + 8 // version/flags, track_ID
				if flags&0x01 != 0 {
					base = int(binary.BigEndian.Uint64(in[o:]))
					o += 8
				}
				if flags&0x02 != 0 {
					o += 4
				}
				if flags&0x08 != 0 {
					dDur = be32(in, o)
					o += 4
				}
				if flags&0x10 != 0 {
					dSize = be32(in, o)
				}
			}
			for _, b := range tk {
				if b.typ != "trun" {
					continue
				}
				flags := be32(in, b.body) & 0xffffff
				n := int(be32(in, b.body+4))
				o := b.body + 8
				pos := base
				if flags&0x01 != 0 {
					pos = base + int(int32(be32(in, o)))
					o += 4
				}
				if flags&0x04 != 0 {
					o += 4
				}
				for i := 0; i < n; i++ {
					d, s := dDur, dSize
					if flags&0x100 != 0 {
						d = be32(in, o)
						o += 4
					}
					if flags&0x200 != 0 {
						s = be32(in, o)
						o += 4
					}
					if flags&0x400 != 0 {
						o += 4
					}
					if flags&0x800 != 0 {
						o += 4
					}
					if o > b.end || pos+int(s) > len(in) {
						return nil, errors.New("truncated fragment")
					}
					data = append(data, in[pos:pos+int(s)]...)
					pos += int(s)
					sizes = append(sizes, s)
					durs = append(durs, d)
				}
			}
		}
	}
	if len(sizes) == 0 {
		return nil, errors.New("no audio samples")
	}
	var total uint64
	for _, d := range durs {
		total += uint64(d)
	}
	if total > 0xffffffff {
		return nil, errors.New("track too long")
	}
	return &track{stsd: in[stsd.start:stsd.end], asc: esdsASC(in, stsd), timescale: timescale, sizes: sizes, durs: durs, data: data}, nil
}

// mp4 writes t as a plain, tagged .m4a.
func (t *track) mp4(title, artist string) []byte {
	sizes, durs, data, timescale := t.sizes, t.durs, t.data, t.timescale
	var total uint64
	for _, d := range durs {
		total += uint64(d)
	}
	dur := uint32(total)

	// stts: runs of equal durations.
	var stts []byte
	var runs uint32
	for i := 0; i < len(durs); {
		j := i
		for j < len(durs) && durs[j] == durs[i] {
			j++
		}
		stts = u32(stts, uint32(j-i), durs[i])
		runs++
		i = j
	}
	stsz := u32(nil, 0, uint32(len(sizes)))
	for _, s := range sizes {
		stsz = u32(stsz, s)
	}

	ftyp := mk("ftyp", []byte("M4A "), u32(nil, 0), []byte("M4A mp42isom"))
	build := func(chunkOffset uint32) []byte {
		stbl := mk("stbl",
			t.stsd,
			full("stts", u32(nil, runs), stts),
			full("stsc", u32(nil, 1, 1, uint32(len(sizes)), 1)),
			full("stsz", stsz),
			full("stco", u32(nil, 1, chunkOffset)),
		)
		minf := mk("minf",
			full("smhd", u32(nil, 0)),
			mk("dinf", full("dref", u32(nil, 1), mk("url ", u32(nil, 1)))),
			stbl,
		)
		mdia := mk("mdia",
			full("mdhd", u32(nil, 0, 0, timescale, dur), []byte{0x55, 0xc4, 0, 0}), // language "und"
			full("hdlr", u32(nil, 0), []byte("soun"), make([]byte, 12), []byte("SoundHandler\x00")),
			minf,
		)
		tkhd := full7("tkhd", u32(nil, 0, 0, 1, 0, dur, 0, 0), []byte{0, 0, 0, 0, 1, 0, 0, 0}, matrix(), u32(nil, 0, 0))
		mvhd := full("mvhd", u32(nil, 0, 0, timescale, dur, 0x00010000), []byte{1, 0}, make([]byte, 10), matrix(), make([]byte, 24), u32(nil, 2))
		return mk("moov", mvhd, mk("trak", tkhd, mdia), tags(title, artist))
	}
	moovOut := build(0)
	moovOut = build(uint32(len(ftyp) + len(moovOut) + 8)) // same size, now with the real offset

	out := make([]byte, 0, len(ftyp)+len(moovOut)+8+len(data))
	out = append(out, ftyp...)
	out = append(out, moovOut...)
	out = binary.BigEndian.AppendUint32(out, uint32(8+len(data)))
	out = append(out, "mdat"...)
	return append(out, data...)
}

// esdsASC digs the AudioSpecificConfig out of stsd > mp4a > esds: the
// DecoderSpecificInfo descriptor (tag 5) inside DecoderConfig (tag 4) inside
// ES_Descriptor (tag 3). nil if it isn't there.
func esdsASC(in []byte, stsd box) []byte {
	// stsd: version/flags + entry count, then sample entries.
	entries, err := boxes(in, stsd.body+8, stsd.end)
	if err != nil || len(entries) == 0 {
		return nil
	}
	// An audio sample entry has 28 bytes of fixed fields before its child boxes.
	kids, err := boxes(in, entries[0].body+28, entries[0].end)
	if err != nil {
		return nil
	}
	esds, ok := child(in, kids, "esds")
	if !ok {
		return nil
	}
	return descriptor(in[esds.body+4:esds.end], 5)
}

// descriptor finds the MPEG-4 descriptor with tag want, looking inside
// ES_Descriptor and DecoderConfigDescriptor, and returns its payload.
func descriptor(b []byte, want byte) []byte {
	for len(b) >= 2 {
		tag := b[0]
		n, i := 0, 1
		for ; i < len(b) && i <= 4; i++ { // length: up to 4 bytes of 7 bits
			n = n<<7 | int(b[i]&0x7f)
			if b[i]&0x80 == 0 {
				i++
				break
			}
		}
		if i+n > len(b) {
			return nil
		}
		body := b[i : i+n]
		switch {
		case tag == want:
			return body
		case tag == 3 && len(body) >= 3: // ES_ID(2) + flags(1), then optional fields
			skip, flags := 3, body[2]
			if flags&0x80 != 0 {
				skip += 2
			}
			if flags&0x40 != 0 && len(body) > skip {
				skip += 1 + int(body[skip])
			}
			if flags&0x20 != 0 {
				skip += 2
			}
			if skip <= len(body) {
				if r := descriptor(body[skip:], want); r != nil {
					return r
				}
			}
		case tag == 4 && len(body) >= 13: // objectType, streamType, buffer, bitrates
			if r := descriptor(body[13:], want); r != nil {
				return r
			}
		}
		b = b[i+n:]
	}
	return nil
}

// tags is udta/meta/ilst with ©nam and ©ART, the atoms every player reads.
func tags(title, artist string) []byte {
	item := func(name, v string) []byte {
		if v == "" {
			return nil
		}
		return mk(name, mk("data", u32(nil, 1, 0), []byte(v))) // type 1 = UTF-8
	}
	ilst := mk("ilst", item("\xa9nam", title), item("\xa9ART", artist))
	hdlr := full("hdlr", u32(nil, 0), []byte("mdirappl"), make([]byte, 9))
	return mk("udta", full("meta", hdlr, ilst))
}

func matrix() []byte { return u32(nil, 0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000) }

type box struct {
	typ              string
	start, body, end int
}

func boxes(b []byte, start, end int) ([]box, error) {
	var out []box
	for o := start; o+8 <= end; {
		size := int(be32(b, o))
		hl := 8
		switch size {
		case 1:
			if o+16 > end {
				return nil, errors.New("truncated box")
			}
			size, hl = int(binary.BigEndian.Uint64(b[o+8:])), 16
		case 0:
			size = end - o
		}
		if size < hl || o+size > end {
			return nil, errors.New("truncated box")
		}
		out = append(out, box{string(b[o+4 : o+8]), o, o + hl, o + size})
		o += size
	}
	return out, nil
}

func child(b []byte, kids []box, typ string) (box, bool) {
	for _, k := range kids {
		if k.typ == typ {
			return k, true
		}
	}
	return box{}, false
}

// path finds a descendant of parent by box types.
func path(b []byte, parent box, types ...string) (box, bool) {
	cur := parent
	for _, t := range types {
		kids, err := boxes(b, cur.body, cur.end)
		if err != nil {
			return box{}, false
		}
		var ok bool
		if cur, ok = child(b, kids, t); !ok {
			return box{}, false
		}
	}
	return cur, true
}

func be32(b []byte, o int) uint32 { return binary.BigEndian.Uint32(b[o:]) }

func u32(b []byte, vs ...uint32) []byte {
	for _, v := range vs {
		b = binary.BigEndian.AppendUint32(b, v)
	}
	return b
}

func mk(typ string, parts ...[]byte) []byte {
	n := 8
	for _, p := range parts {
		n += len(p)
	}
	out := binary.BigEndian.AppendUint32(make([]byte, 0, n), uint32(n))
	out = append(out, typ...)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// full is a FullBox with version 0 and no flags.
func full(typ string, parts ...[]byte) []byte {
	return mk(typ, append([][]byte{{0, 0, 0, 0}}, parts...)...)
}

// full7 is a FullBox with flags 7 (track enabled, in movie, in preview).
func full7(typ string, parts ...[]byte) []byte {
	return mk(typ, append([][]byte{{0, 0, 0, 7}}, parts...)...)
}
