package audio

import (
	"encoding/binary"
	"io"
	"os"
	"strings"

	"github.com/dhowden/tag"
)

// walkChunks iterates the chunks of an IFF (AIFF, big-endian) or RIFF (WAV,
// little-endian) file that start at offset 12.
func walkChunks(f *os.File, size int64, order binary.ByteOrder, fn func(id string, off, n int64) error) error {
	off := int64(12)
	hdr := make([]byte, 8)
	for off+8 <= size {
		if _, err := f.ReadAt(hdr, off); err != nil {
			return err
		}
		id := string(hdr[:4])
		n := int64(order.Uint32(hdr[4:]))
		body := off + 8
		// Some writers put 0 or 0xFFFFFFFF sizes on the final data chunk.
		if n == 0xFFFFFFFF || body+n > size {
			n = size - body
		}
		if err := fn(id, body, n); err != nil {
			return err
		}
		off = body + n + n%2
	}
	return nil
}

func readID3Chunk(f *os.File, in *Info, off, n int64) {
	if m, err := tag.ReadID3v2Tags(io.NewSectionReader(f, off, n)); err == nil {
		applyTags(in, m)
	}
}

func readAIFF(f *os.File, in *Info) error {
	head := make([]byte, 12)
	if _, err := f.ReadAt(head, 0); err != nil {
		return err
	}
	if string(head[:4]) != "FORM" || (string(head[8:12]) != "AIFF" && string(head[8:12]) != "AIFC") {
		return errFormat
	}
	in.Codec = "AIFF"
	in.Lossless = true
	aifc := string(head[8:12]) == "AIFC"
	var frames uint32
	return walkChunks(f, in.Size, binary.BigEndian, func(id string, off, n int64) error {
		switch strings.ToUpper(id) {
		case "COMM":
			b := make([]byte, min(n, 22))
			if _, err := f.ReadAt(b, off); err != nil || len(b) < 18 {
				return err
			}
			frames = binary.BigEndian.Uint32(b[2:6])
			in.BitDepth = int(binary.BigEndian.Uint16(b[6:8]))
			in.SampleRate = int(extendedToFloat(b[8:18]) + 0.5)
			if aifc && len(b) >= 22 {
				switch c := string(b[18:22]); c {
				case "NONE", "sowt", "twos", "fl32", "fl64", "in24", "in32":
				default: // compressed AIFF-C: treat as lossy of unknown codec
					in.Codec = "AIFC-" + strings.TrimSpace(c)
					in.Lossless = false
				}
			}
			if in.SampleRate > 0 {
				in.Duration = float64(frames) / float64(in.SampleRate)
			}
		case "ID3 ":
			readID3Chunk(f, in, off, n)
		}
		return nil
	})
}

func readWAV(f *os.File, in *Info) error {
	head := make([]byte, 12)
	if _, err := f.ReadAt(head, 0); err != nil {
		return err
	}
	if (string(head[:4]) != "RIFF" && string(head[:4]) != "RF64") || string(head[8:12]) != "WAVE" {
		return errFormat
	}
	in.Codec = "WAV"
	in.Lossless = true
	var byteRate uint32
	var dataLen int64
	info := map[string]string{}
	err := walkChunks(f, in.Size, binary.LittleEndian, func(id string, off, n int64) error {
		switch strings.ToLower(id) {
		case "fmt ":
			b := make([]byte, 16)
			if _, err := f.ReadAt(b, off); err != nil {
				return err
			}
			if fmtTag := binary.LittleEndian.Uint16(b[0:2]); fmtTag != 1 && fmtTag != 3 && fmtTag != 0xFFFE {
				in.Codec = "WAV-compressed"
				in.Lossless = false
			}
			in.SampleRate = int(binary.LittleEndian.Uint32(b[4:8]))
			byteRate = binary.LittleEndian.Uint32(b[8:12])
			in.BitDepth = int(binary.LittleEndian.Uint16(b[14:16]))
		case "data":
			dataLen = n
		case "id3 ":
			readID3Chunk(f, in, off, n)
		case "list":
			readListInfo(f, off, n, info)
		}
		return nil
	})
	if byteRate > 0 {
		in.Duration = float64(dataLen) / float64(byteRate)
		in.Bitrate = int(byteRate * 8 / 1000)
	}
	// LIST/INFO tags are a fallback for files without an ID3 chunk.
	if in.Artist == "" {
		in.Artist = info["IART"]
	}
	if in.Title == "" {
		in.Title = info["INAM"]
	}
	if in.Album == "" {
		in.Album = info["IPRD"]
	}
	if in.Comment == "" {
		in.Comment = info["ICMT"]
	}
	return err
}

func readListInfo(f *os.File, off, n int64, out map[string]string) {
	if n < 4 || n > 1<<20 {
		return
	}
	b := make([]byte, n)
	if _, err := f.ReadAt(b, off); err != nil || string(b[:4]) != "INFO" {
		return
	}
	for p := 4; p+8 <= len(b); {
		id := string(b[p : p+4])
		sz := int(binary.LittleEndian.Uint32(b[p+4:]))
		if p+8+sz > len(b) {
			break
		}
		out[id] = clean(string(b[p+8 : p+8+sz]))
		p += 8 + sz + sz%2
	}
}
