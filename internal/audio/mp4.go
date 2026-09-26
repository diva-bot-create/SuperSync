package audio

import (
	"encoding/binary"
	"os"
)

// readMP4 walks the ISO-BMFF box tree for mvhd (duration) and the first audio
// sample entry in stsd (codec, sample rate, bit depth).
func readMP4(f *os.File, in *Info) error {
	head := make([]byte, 8)
	if _, err := f.ReadAt(head, 4); err != nil {
		return err
	}
	if string(head[:4]) != "ftyp" {
		return errFormat
	}
	found := false
	var walk func(start, end int64, depth int) error
	walk = func(start, end int64, depth int) error {
		hdr := make([]byte, 16)
		for off := start; off+8 <= end; {
			if _, err := f.ReadAt(hdr[:8], off); err != nil {
				return err
			}
			size := int64(binary.BigEndian.Uint32(hdr[:4]))
			typ := string(hdr[4:8])
			hl := int64(8)
			if size == 1 {
				if _, err := f.ReadAt(hdr[8:16], off+8); err != nil {
					return err
				}
				size = int64(binary.BigEndian.Uint64(hdr[8:16]))
				hl = 16
			} else if size == 0 {
				size = end - off
			}
			if size < hl || off+size > end {
				return nil
			}
			body, bodyEnd := off+hl, off+size
			switch typ {
			case "moov", "trak", "mdia", "minf", "stbl":
				if depth < 8 {
					if err := walk(body, bodyEnd, depth+1); err != nil {
						return err
					}
				}
			case "mvhd":
				b := make([]byte, 32)
				f.ReadAt(b, body)
				var ts, dur uint64
				if b[0] == 1 {
					ts = uint64(binary.BigEndian.Uint32(b[20:24]))
					dur = binary.BigEndian.Uint64(b[24:32])
				} else {
					ts = uint64(binary.BigEndian.Uint32(b[12:16]))
					dur = uint64(binary.BigEndian.Uint32(b[16:20]))
				}
				if ts > 0 {
					in.Duration = float64(dur) / float64(ts)
				}
			case "stsd":
				if found {
					break
				}
				// full box (4) + entry count (4) + sample entry: size(4) type(4) ...
				b := make([]byte, 8+36)
				if _, err := f.ReadAt(b, body); err != nil {
					break
				}
				e := b[8:]
				switch string(e[4:8]) {
				case "mp4a":
					in.Codec = "AAC"
				case "alac":
					in.Codec, in.Lossless = "ALAC", true
				case "fLaC":
					in.Codec, in.Lossless = "FLAC", true
				case "Opus":
					in.Codec = "Opus"
				case "ac-3", "ec-3":
					in.Codec = "AC3"
				default:
					continue
				}
				found = true
				// AudioSampleEntry: 6 reserved + 2 dref idx + 8 reserved + 2 ch + 2 bits + 4 + 4 rate (16.16)
				in.BitDepth = int(binary.BigEndian.Uint16(e[26:28]))
				in.SampleRate = int(binary.BigEndian.Uint32(e[32:36]) >> 16)
				if !in.Lossless {
					in.BitDepth = 0
				}
				if in.Codec == "AAC" {
					entrySize := int64(binary.BigEndian.Uint32(e[0:4]))
					if entrySize > 36 && entrySize < 1<<16 {
						eb := make([]byte, entrySize)
						if _, err := f.ReadAt(eb, body+8); err == nil {
							in.Bitrate = esdsBitrate(eb[36:])
						}
					}
				}
			}
			off = bodyEnd
		}
		return nil
	}
	if err := walk(0, in.Size, 0); err != nil {
		return err
	}
	if in.Codec == "" {
		in.Codec = "AAC"
	}
	return nil
}

// esdsBitrate finds the encoder's nominal average bitrate (kbps) in the esds
// box among an mp4a sample entry's children, or returns 0.
func esdsBitrate(b []byte) int {
	for len(b) >= 8 {
		size := int(binary.BigEndian.Uint32(b[:4]))
		if size < 8 || size > len(b) {
			return 0
		}
		if string(b[4:8]) == "esds" {
			return decoderConfigBitrate(b[12:size]) // skip version/flags
		}
		b = b[size:]
	}
	return 0
}

// decoderConfigBitrate walks MPEG-4 descriptors: ES_Descriptor (0x03)
// contains DecoderConfigDescriptor (0x04) with maxBitrate and avgBitrate.
func decoderConfigBitrate(b []byte) int {
	for len(b) >= 2 {
		tag := b[0]
		n, i := 0, 1
		for ; i < 5 && i < len(b); i++ {
			n = n<<7 | int(b[i]&0x7f)
			if b[i]&0x80 == 0 {
				i++
				break
			}
		}
		if i+n > len(b) {
			n = len(b) - i
		}
		body := b[i : i+n]
		switch tag {
		case 0x03: // ES_ID(2) flags(1) [+ optional fields]
			if len(body) < 3 {
				return 0
			}
			flags, off := body[2], 3
			if flags&0x80 != 0 {
				off += 2
			}
			if flags&0x40 != 0 && off < len(body) {
				off += 1 + int(body[off])
			}
			if flags&0x20 != 0 {
				off += 2
			}
			if off > len(body) {
				return 0
			}
			b = body[off:]
			continue
		case 0x04: // objectType(1) streamType(1) bufferSize(3) maxBitrate(4) avgBitrate(4)
			if len(body) < 13 {
				return 0
			}
			avg := int(binary.BigEndian.Uint32(body[9:13]))
			if avg == 0 {
				avg = int(binary.BigEndian.Uint32(body[5:9]))
			}
			return (avg + 500) / 1000
		}
		b = b[i+n:]
	}
	return 0
}
