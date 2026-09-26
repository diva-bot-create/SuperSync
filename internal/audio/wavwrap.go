package audio

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
)

// AIFFAsWAV presents an AIFF file as a seekable WAV stream (same samples,
// byte order swapped), for browsers that can't play AIFF.
func AIFFAsWAV(path string) (io.ReadSeekCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, _ := f.Stat()
	w := &aiffWav{f: f}
	var rate int
	err = walkChunks(f, st.Size(), binary.BigEndian, func(id string, off, n int64) error {
		switch strings.ToUpper(id) {
		case "COMM":
			b := make([]byte, min(n, 22))
			if _, err := f.ReadAt(b, off); err != nil || len(b) < 18 {
				return errFormat
			}
			w.ch = int(binary.BigEndian.Uint16(b[0:2]))
			w.bits = int(binary.BigEndian.Uint16(b[6:8]))
			rate = int(extendedToFloat(b[8:18]) + 0.5)
			w.swap = true
			if len(b) >= 22 {
				switch string(b[18:22]) {
				case "sowt":
					w.swap = false
				case "fl32", "FL32":
					w.float, w.bits = true, 32
				case "fl64", "FL64":
					w.float, w.bits = true, 64
				case "NONE", "twos", "in24", "in32":
				default:
					return errors.New("compressed AIFF-C can't be played")
				}
			}
		case "SSND":
			b := make([]byte, 8)
			f.ReadAt(b, off)
			skip := int64(binary.BigEndian.Uint32(b[0:4]))
			w.dataOff, w.dataLen = off+8+skip, n-8-skip
		}
		return nil
	})
	if err == nil && (w.ch == 0 || rate == 0 || w.dataLen <= 0) {
		err = errFormat
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	w.bps = (w.bits + 7) / 8
	w.dataLen -= w.dataLen % int64(w.bps*w.ch)
	w.header = wavHeader(w.ch, rate, w.bps*8, w.float, w.dataLen)
	return w, nil
}

type aiffWav struct {
	f                *os.File
	header           []byte
	dataOff, dataLen int64
	ch, bits, bps    int
	swap, float      bool
	pos              int64
}

func wavHeader(ch, rate, bits int, float bool, dataLen int64) []byte {
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+dataLen))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	format := uint16(1)
	if float {
		format = 3
	}
	binary.LittleEndian.PutUint16(h[20:], format)
	binary.LittleEndian.PutUint16(h[22:], uint16(ch))
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*ch*bits/8))
	binary.LittleEndian.PutUint16(h[32:], uint16(ch*bits/8))
	binary.LittleEndian.PutUint16(h[34:], uint16(bits))
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(dataLen))
	return h
}

func (w *aiffWav) size() int64 { return int64(len(w.header)) + w.dataLen }

func (w *aiffWav) Read(p []byte) (int, error) {
	if w.pos >= w.size() {
		return 0, io.EOF
	}
	n := 0
	if w.pos < int64(len(w.header)) {
		n = copy(p, w.header[w.pos:])
		w.pos += int64(n)
		p = p[n:]
	}
	if len(p) == 0 {
		return n, nil
	}
	d := w.pos - int64(len(w.header)) // offset into the data
	want := min(int64(len(p)), w.dataLen-d)
	// Read whole samples around the requested range so bytes can be swapped.
	bps := int64(w.bps)
	start := d - d%bps
	end := d + want
	if r := end % bps; r != 0 {
		end += bps - r
	}
	end = min(end, w.dataLen)
	buf := make([]byte, end-start)
	if _, err := w.f.ReadAt(buf, w.dataOff+start); err != nil && err != io.EOF {
		return n, err
	}
	if w.swap && w.bps > 1 {
		for i := 0; i+w.bps <= len(buf); i += w.bps {
			s := buf[i : i+w.bps]
			for a, b := 0, len(s)-1; a < b; a, b = a+1, b-1 {
				s[a], s[b] = s[b], s[a]
			}
		}
	}
	m := copy(p, buf[d-start:])
	w.pos += int64(m)
	return n + m, nil
}

func (w *aiffWav) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		w.pos = off
	case io.SeekCurrent:
		w.pos += off
	case io.SeekEnd:
		w.pos = w.size() + off
	}
	if w.pos < 0 {
		w.pos = 0
		return 0, errors.New("negative seek")
	}
	return w.pos, nil
}

func (w *aiffWav) Close() error { return w.f.Close() }
