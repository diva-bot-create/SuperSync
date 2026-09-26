package audio

import (
	"bytes"
	"encoding/binary"
	"os"
)

func readFLAC(f *os.File, in *Info) error {
	in.Codec = "FLAC"
	in.Lossless = true
	hdr := make([]byte, 4)
	var off int64
	if _, err := f.ReadAt(hdr, 0); err != nil {
		return err
	}
	// Some taggers prepend an ID3v2 tag to FLAC files.
	if bytes.Equal(hdr[:3], []byte("ID3")) {
		id3 := make([]byte, 10)
		f.ReadAt(id3, 0)
		off = 10 + (int64(id3[6])<<21 | int64(id3[7])<<14 | int64(id3[8])<<7 | int64(id3[9]))
		if _, err := f.ReadAt(hdr, off); err != nil {
			return err
		}
	}
	if string(hdr) != "fLaC" {
		return errFormat
	}
	// First metadata block must be STREAMINFO (type 0, 34 bytes).
	blk := make([]byte, 4+34)
	if _, err := f.ReadAt(blk, off+4); err != nil {
		return err
	}
	if blk[0]&0x7f != 0 {
		return errFormat
	}
	si := blk[4:]
	// Bytes 10..17: 20 bits sample rate, 3 bits channels-1, 5 bits bps-1, 36 bits total samples.
	v := binary.BigEndian.Uint64(si[10:18])
	in.SampleRate = int(v >> 44)
	in.BitDepth = int((v>>36)&0x1f) + 1
	total := v & 0xFFFFFFFFF
	if in.SampleRate > 0 {
		in.Duration = float64(total) / float64(in.SampleRate)
	}
	return nil
}
