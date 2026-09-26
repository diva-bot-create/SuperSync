package soundcloud

import (
	"encoding/binary"
	"unicode/utf16"
)

// ID3v2 builds a minimal ID3v2.3 tag (title + artist) so a downloaded file
// scans and matches like any other track in the library.
func ID3v2(title, artist string) []byte {
	var frames []byte
	for _, f := range [][2]string{{"TIT2", title}, {"TPE1", artist}} {
		if f[1] == "" {
			continue
		}
		data := []byte{1, 0xFF, 0xFE} // UTF-16 with BOM: what every v2.3 reader handles
		for _, u := range utf16.Encode([]rune(f[1])) {
			data = binary.LittleEndian.AppendUint16(data, u)
		}
		frames = append(frames, f[0]...)
		frames = binary.BigEndian.AppendUint32(frames, uint32(len(data)))
		frames = append(frames, 0, 0)
		frames = append(frames, data...)
	}
	n := len(frames)
	hdr := []byte{'I', 'D', '3', 3, 0, 0, byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
	return append(hdr, frames...)
}
