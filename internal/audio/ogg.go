package audio

import (
	"bytes"
	"encoding/binary"
	"os"
)

func readOgg(f *os.File, in *Info) error {
	first := make([]byte, 128)
	n, _ := f.ReadAt(first, 0)
	first = first[:n]
	if n < 28+19 || string(first[:4]) != "OggS" {
		return errFormat
	}
	segs := int(first[26])
	pkt := first[27+segs:]
	var rate int
	var preskip int64
	switch {
	case len(pkt) >= 16 && string(pkt[1:7]) == "vorbis":
		in.Codec = "Vorbis"
		rate = int(binary.LittleEndian.Uint32(pkt[12:16]))
		in.SampleRate = rate
	case len(pkt) >= 16 && string(pkt[:8]) == "OpusHead":
		in.Codec = "Opus"
		preskip = int64(binary.LittleEndian.Uint16(pkt[10:12]))
		rate = 48000 // Opus granule positions are always 48 kHz
		in.SampleRate = int(binary.LittleEndian.Uint32(pkt[12:16]))
		if in.SampleRate == 0 {
			in.SampleRate = 48000
		}
	case len(pkt) >= 13 && string(pkt[1:5]) == "FLAC":
		in.Codec, in.Lossless = "FLAC", true
		// Ogg FLAC: STREAMINFO follows a 13-byte mapping header + 4-byte block header.
		if len(pkt) >= 13+4+18 {
			v := binary.BigEndian.Uint64(pkt[13+4+10:])
			in.SampleRate = int(v >> 44)
			in.BitDepth = int((v>>36)&0x1f) + 1
			rate = in.SampleRate
		}
	default:
		return errFormat
	}
	if rate == 0 {
		return errFormat
	}

	// Duration = granule position of the last page.
	tailLen := int64(64 * 1024)
	if tailLen > in.Size {
		tailLen = in.Size
	}
	tail := make([]byte, tailLen)
	if _, err := f.ReadAt(tail, in.Size-tailLen); err != nil {
		return err
	}
	i := bytes.LastIndex(tail, []byte("OggS"))
	if i < 0 || i+14 > len(tail) {
		return errFormat
	}
	granule := int64(binary.LittleEndian.Uint64(tail[i+6 : i+14]))
	in.Duration = float64(granule-preskip) / float64(rate)
	return nil
}
