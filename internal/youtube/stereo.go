package youtube

import (
	"os"
	"path/filepath"
	"strings"
)

// JointStereo marks every stereo frame of an MPEG-1 Layer III stream as joint
// stereo, as LAME does. The encoder picks plain L/R or M/S coding frame by
// frame and says so in the channel mode; rekordbox can refuse a file whose
// channel mode changes mid-stream. A joint-stereo frame with M/S and
// intensity off is the same bits decoded the same way, so the audio doesn't
// change. It reports whether anything changed; b is edited in place.
func JointStereo(b []byte) bool {
	i := 0
	if len(b) >= 10 && string(b[:3]) == "ID3" {
		i = 10 + (int(b[6]&0x7f)<<21 | int(b[7]&0x7f)<<14 | int(b[8]&0x7f)<<7 | int(b[9]&0x7f))
		if b[5]&0x10 != 0 {
			i += 10 // footer
		}
	}
	var modes [4]int
	var plain []int
	for i+4 <= len(b) {
		n := frameLen(b[i:])
		if n == 0 {
			break
		}
		mode := b[i+3] >> 6
		if isTagFrame(b[i:i+n], mode) {
			i += n // LAME's Xing/Info frame: no audio, left as is
			continue
		}
		modes[mode]++
		if mode == 0 {
			plain = append(plain, i)
		}
		i += n
	}
	// Only a stream that mixes the two: a plain-stereo file stays as it is.
	if len(plain) == 0 || modes[1] == 0 {
		return false
	}
	for _, at := range plain {
		b[at+3] = b[at+3]&0x0f | 0x40 // mode 01, mode extension 00 (no M/S, no intensity)
	}
	return true
}

// isTagFrame says whether a frame is a Xing/Info header frame rather than audio.
func isTagFrame(f []byte, mode byte) bool {
	at := 4 + 32 // header + stereo side info
	if mode == 3 {
		at = 4 + 17
	}
	if len(f) < at+4 {
		return false
	}
	tag := string(f[at : at+4])
	return tag == "Xing" || tag == "Info"
}

// frameLen is the length of the MPEG-1 Layer III frame at the start of h, or
// 0 if h doesn't start with one.
func frameLen(h []byte) int {
	if len(h) < 4 || h[0] != 0xff || h[1]&0xfe != 0xfa { // sync, MPEG-1, Layer III
		return 0
	}
	kbps := [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}[h[2]>>4]
	rate := [4]int{44100, 48000, 32000, 0}[h[2]>>2&3]
	if kbps == 0 || rate == 0 {
		return 0
	}
	return 144*kbps*1000/rate + int(h[2]>>1&1)
}

// FixStereo applies JointStereo to an mp3 file on disk (in place: only
// header bits change, so the size and audio stay the same).
func FixStereo(path string) (bool, error) {
	if !strings.EqualFold(filepath.Ext(path), ".mp3") {
		return false, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	orig := append([]byte(nil), b...)
	if !JointStereo(b) {
		return false, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return false, err
	}
	for i := range b {
		if b[i] != orig[i] {
			if _, err := f.WriteAt(b[i:i+1], int64(i)); err != nil {
				f.Close()
				return false, err
			}
		}
	}
	return true, f.Close()
}
