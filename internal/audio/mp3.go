package audio

import (
	"bytes"
	"encoding/binary"
	"os"
)

var mp3Bitrates = [2][3][16]int{
	{ // MPEG-1
		{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0}, // Layer I
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0},    // Layer II
		{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},     // Layer III
	},
	{ // MPEG-2 / 2.5
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	},
}

var mp3SampleRates = [4][3]int{
	{11025, 12000, 8000},  // MPEG 2.5
	{0, 0, 0},             // reserved
	{22050, 24000, 16000}, // MPEG 2
	{44100, 48000, 32000}, // MPEG 1
}

type mp3Header struct {
	version    int // 3 = MPEG1, 2 = MPEG2, 0 = MPEG2.5
	layer      int // 1..3
	bitrate    int // kbps
	sampleRate int
	padding    int
	mono       bool
}

func parseMP3Header(b []byte) (h mp3Header, ok bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return h, false
	}
	h.version = int(b[1]>>3) & 3
	layerBits := int(b[1]>>1) & 3
	if h.version == 1 || layerBits == 0 {
		return h, false
	}
	h.layer = 4 - layerBits
	brIdx := int(b[2] >> 4)
	srIdx := int(b[2]>>2) & 3
	if brIdx == 0 || brIdx == 15 || srIdx == 3 {
		return h, false
	}
	vi := 0
	if h.version != 3 {
		vi = 1
	}
	h.bitrate = mp3Bitrates[vi][h.layer-1][brIdx]
	h.sampleRate = mp3SampleRates[h.version][srIdx]
	h.padding = int(b[2]>>1) & 1
	h.mono = b[3]>>6 == 3
	return h, true
}

func (h mp3Header) samplesPerFrame() int {
	switch {
	case h.layer == 1:
		return 384
	case h.layer == 2 || h.version == 3:
		return 1152
	default:
		return 576
	}
}

func (h mp3Header) frameLen() int {
	if h.layer == 1 {
		return (12*h.bitrate*1000/h.sampleRate + h.padding) * 4
	}
	return h.samplesPerFrame()/8*h.bitrate*1000/h.sampleRate + h.padding
}

// mp3Layout is where the audio frames of an MP3 file are.
type mp3Layout struct {
	h          mp3Header
	audioStart int64 // first frame (possibly the Xing/Info tag frame)
	audioEnd   int64 // excludes a trailing ID3v1 tag
	tagFrame   bool  // first frame is a Xing/Info/VBRI header, not audio
	frames     int   // frame count from the tag frame, or 0
}

func locateMP3(f *os.File, size int64) (*mp3Layout, error) {
	// Skip any ID3v2 tag(s).
	var start int64
	for {
		hdr := make([]byte, 10)
		if _, err := f.ReadAt(hdr, start); err != nil {
			return nil, err
		}
		if !bytes.Equal(hdr[:3], []byte("ID3")) {
			break
		}
		n := int64(hdr[6])<<21 | int64(hdr[7])<<14 | int64(hdr[8])<<7 | int64(hdr[9])
		start += 10 + n
		if hdr[5]&0x10 != 0 { // footer present
			start += 10
		}
	}

	// Scan up to 256 KB for the first frame whose successor is also valid.
	buf := make([]byte, 256*1024)
	n, _ := f.ReadAt(buf, start)
	buf = buf[:n]
	l := &mp3Layout{}
	pos := -1
	for i := 0; i+4 <= len(buf); i++ {
		hh, ok := parseMP3Header(buf[i:])
		if !ok {
			continue
		}
		next := i + hh.frameLen()
		if next+4 <= len(buf) {
			if h2, ok2 := parseMP3Header(buf[next:]); !ok2 || h2.sampleRate != hh.sampleRate {
				continue
			}
		}
		l.h, pos = hh, i
		break
	}
	if pos < 0 {
		return nil, errFormat
	}
	l.audioStart = start + int64(pos)
	l.audioEnd = size
	if tail := make([]byte, 3); size > 128 {
		if _, err := f.ReadAt(tail, size-128); err == nil && string(tail) == "TAG" {
			l.audioEnd -= 128
		}
	}

	frame := buf[pos:]
	h := l.h
	// Xing / Info header (VBR or LAME CBR) sits after the side info.
	var sideInfo int
	switch {
	case h.version == 3 && !h.mono:
		sideInfo = 32
	case h.version == 3 || !h.mono:
		sideInfo = 17
	default:
		sideInfo = 9
	}
	if x := 4 + sideInfo; x+12 <= len(frame) {
		id := string(frame[x : x+4])
		if id == "Xing" || id == "Info" {
			l.tagFrame = true
			if flags := binary.BigEndian.Uint32(frame[x+4:]); flags&1 != 0 {
				l.frames = int(binary.BigEndian.Uint32(frame[x+8:]))
			}
		}
	}
	// VBRI header (Fraunhofer) is always 32 bytes after the frame header.
	if !l.tagFrame && 36+18 <= len(frame) && string(frame[36:40]) == "VBRI" {
		l.tagFrame = true
		l.frames = int(binary.BigEndian.Uint32(frame[36+14:]))
	}
	return l, nil
}

func readMP3(f *os.File, in *Info) error {
	in.Codec = "MP3"
	l, err := locateMP3(f, in.Size)
	if err != nil {
		return err
	}
	h := l.h
	in.SampleRate = h.sampleRate
	audioBytes := l.audioEnd - l.audioStart
	if l.frames > 0 {
		in.Duration = float64(l.frames*h.samplesPerFrame()) / float64(h.sampleRate)
		if in.Duration > 0 {
			in.Bitrate = int(float64(audioBytes) * 8 / in.Duration / 1000)
		}
	} else {
		in.Bitrate = h.bitrate
		in.Duration = float64(audioBytes) * 8 / float64(h.bitrate*1000)
	}
	return nil
}
