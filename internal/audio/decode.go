package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"

	"github.com/hajimehoshi/go-mp3"
	"github.com/mewkiz/flac"
)

// Timing says how closely a decode follows rekordbox's own timeline for the file.
type Timing int

const (
	// Exact: sample 0 is the sample rekordbox calls 0:00.000.
	Exact Timing = iota
	// Approximate: decoded by ffmpeg from a lossy AAC/Opus/Vorbis file whose
	// encoder priming may be handled differently by rekordbox (up to ~50 ms).
	Approximate
)

// ErrNoDecoder means the format needs ffmpeg, which isn't installed.
var ErrNoDecoder = errors.New("can't decode this format without ffmpeg installed")

// DecodeMono decodes the whole file to mono float32 samples at rate Hz, on
// the same timeline rekordbox uses for cue points: for MP3 the Xing/LAME
// header frame is skipped and the encoder delay is kept, which is what
// rekordbox has done since 1.5.4.
func DecodeMono(path string, rate int) ([]float32, Timing, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, Exact, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, Exact, err
	}
	in, err := Read(path)
	if err != nil {
		return nil, Exact, err
	}
	m := &mixer{dst: float64(rate)}
	return decodeInto(path, f, st.Size(), in, m)
}

// DecodeWindow decodes durSecs of mono audio starting at startSecs, at the
// file's own sample rate (returned). Decoding stops as soon as the window is full.
func DecodeWindow(path string, startSecs, durSecs float64) ([]float32, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	in, err := Read(path)
	if err != nil {
		return nil, 0, err
	}
	m := &mixer{startSecs: startSecs, durSecs: durSecs}
	out, _, err := decodeInto(path, f, st.Size(), in, m)
	return out, int(m.src), err
}

func decodeInto(path string, f *os.File, size int64, in *Info, m *mixer) ([]float32, Timing, error) {
	var err error
	switch {
	case in.Format == "mp3":
		err = decodeMP3(f, size, m)
	case in.Format == "flac":
		err = decodeFLAC(f, m)
	case in.Format == "wav":
		err = decodeWAV(f, size, m)
	case in.Format == "aiff":
		err = decodeAIFF(f, size, m)
	default:
		err = decodeFFmpeg(path, in.SampleRate, m)
		if err == nil && !in.Lossless {
			m.finish()
			return m.out, Approximate, nil
		}
	}
	if err != nil {
		return nil, Exact, err
	}
	m.finish()
	return m.out, Exact, nil
}

// mixer folds interleaved frames to mono and downsamples by averaging each
// output period (a box filter, plenty for alignment work).
// With dst 0 it keeps the source rate; startSecs/durSecs select a window
// (durSecs 0 = to the end), after which full is set and decoders stop.
type mixer struct {
	src, dst   float64
	ch         int
	startSecs  float64
	durSecs    float64
	start, end int64
	full       bool
	out        []float32
	sum        float64
	n          int
	idx        int64
	bucket     int64
}

func (m *mixer) format(srcRate, channels int) {
	m.src, m.ch = float64(srcRate), channels
	if m.dst == 0 {
		m.dst = m.src
	}
	m.start = int64(m.startSecs * m.src)
	if m.durSecs > 0 {
		m.end = m.start + int64(m.durSecs*m.src)
	}
}

// add takes one frame's worth of samples (len == channels), each in -1..1.
func (m *mixer) add(frame []float64) {
	if m.idx < m.start {
		m.idx++
		return
	}
	if m.end > 0 && m.idx >= m.end {
		m.full = true
		return
	}
	v := 0.0
	for _, s := range frame {
		v += s
	}
	v /= float64(len(frame))
	b := int64(float64(m.idx-m.start) * m.dst / m.src)
	if b != m.bucket {
		m.flush()
		m.bucket = b
	}
	m.sum += v
	m.n++
	m.idx++
}

func (m *mixer) flush() {
	if m.n > 0 {
		m.out = append(m.out, float32(m.sum/float64(m.n)))
	}
	m.sum, m.n = 0, 0
}

func (m *mixer) finish() { m.flush() }

func decodeMP3(f *os.File, size int64, m *mixer) error {
	l, err := locateMP3(f, size)
	if err != nil {
		return err
	}
	start := l.audioStart
	if l.tagFrame {
		start += int64(l.h.frameLen())
	}
	d, err := mp3.NewDecoder(io.NewSectionReader(f, start, l.audioEnd-start))
	if err != nil {
		return err
	}
	m.format(d.SampleRate(), 2) // go-mp3 always outputs 16-bit stereo
	buf := make([]byte, 64*1024)
	frame := make([]float64, 2)
	var carry []byte
	for {
		n, err := d.Read(buf)
		data := append(carry, buf[:n]...)
		i := 0
		for ; i+4 <= len(data); i += 4 {
			frame[0] = float64(int16(binary.LittleEndian.Uint16(data[i:]))) / 32768
			frame[1] = float64(int16(binary.LittleEndian.Uint16(data[i+2:]))) / 32768
			m.add(frame)
		}
		carry = append(carry[:0], data[i:]...)
		if err == io.EOF || m.full {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func decodeFLAC(f *os.File, m *mixer) error {
	s, err := flac.New(f)
	if err != nil {
		return err
	}
	ch := int(s.Info.NChannels)
	m.format(int(s.Info.SampleRate), ch)
	scale := math.Ldexp(1, int(s.Info.BitsPerSample)-1)
	frame := make([]float64, ch)
	for {
		fr, err := s.ParseNext()
		if err == io.EOF || m.full {
			return nil
		}
		if err != nil {
			return err
		}
		for i := 0; i < int(fr.BlockSize); i++ {
			for c := 0; c < ch; c++ {
				frame[c] = float64(fr.Subframes[c].Samples[i]) / scale
			}
			m.add(frame)
		}
	}
}

type pcmFormat struct {
	channels, bits int
	float, big     bool
	unsigned8      bool
}

func decodePCM(r io.Reader, p pcmFormat, m *mixer) error {
	bps := p.bits / 8
	if bps < 1 || bps > 8 || p.channels < 1 {
		return fmt.Errorf("unsupported PCM layout (%d-bit, %d channels)", p.bits, p.channels)
	}
	var order binary.ByteOrder = binary.LittleEndian
	if p.big {
		order = binary.BigEndian
	}
	frameBytes := bps * p.channels
	buf := make([]byte, frameBytes*8192)
	frame := make([]float64, p.channels)
	for {
		n, err := io.ReadFull(r, buf)
		n -= n % frameBytes
		for off := 0; off < n; off += frameBytes {
			for c := 0; c < p.channels; c++ {
				b := buf[off+c*bps : off+(c+1)*bps]
				frame[c] = pcmSample(b, p, order)
			}
			m.add(frame)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF || m.full {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func pcmSample(b []byte, p pcmFormat, order binary.ByteOrder) float64 {
	if p.float {
		if len(b) == 4 {
			return float64(math.Float32frombits(order.Uint32(b)))
		}
		return math.Float64frombits(order.Uint64(b))
	}
	if len(b) == 1 {
		if p.unsigned8 {
			return (float64(b[0]) - 128) / 128
		}
		return float64(int8(b[0])) / 128
	}
	// Assemble a big-endian two's-complement integer of len(b) bytes.
	var v int64
	if p.big {
		for _, x := range b {
			v = v<<8 | int64(x)
		}
	} else {
		for i := len(b) - 1; i >= 0; i-- {
			v = v<<8 | int64(b[i])
		}
	}
	shift := 64 - 8*len(b)
	v = v << shift >> shift // sign-extend
	return float64(v) / math.Ldexp(1, 8*len(b)-1)
}

func decodeWAV(f *os.File, size int64, m *mixer) error {
	var p pcmFormat
	var rate int
	var dataOff, dataLen int64 = -1, 0
	err := walkChunks(f, size, binary.LittleEndian, func(id string, off, n int64) error {
		switch strings.ToLower(id) {
		case "fmt ":
			b := make([]byte, min(n, 40))
			if _, err := f.ReadAt(b, off); err != nil || len(b) < 16 {
				return errFormat
			}
			tag := binary.LittleEndian.Uint16(b[0:2])
			if tag == 0xFFFE && len(b) >= 26 { // WAVE_FORMAT_EXTENSIBLE: real format in the SubFormat GUID
				tag = binary.LittleEndian.Uint16(b[24:26])
			}
			switch tag {
			case 1:
			case 3:
				p.float = true
			default:
				return fmt.Errorf("compressed WAV (format %#x) isn't supported", tag)
			}
			p.channels = int(binary.LittleEndian.Uint16(b[2:4]))
			rate = int(binary.LittleEndian.Uint32(b[4:8]))
			p.bits = int(binary.LittleEndian.Uint16(b[14:16]))
			p.unsigned8 = true
		case "data":
			dataOff, dataLen = off, n
		}
		return nil
	})
	if err != nil {
		return err
	}
	if dataOff < 0 || rate == 0 {
		return errFormat
	}
	m.format(rate, p.channels)
	return decodePCM(io.NewSectionReader(f, dataOff, dataLen), p, m)
}

func decodeAIFF(f *os.File, size int64, m *mixer) error {
	p := pcmFormat{big: true}
	var rate int
	var dataOff, dataLen int64 = -1, 0
	err := walkChunks(f, size, binary.BigEndian, func(id string, off, n int64) error {
		switch strings.ToUpper(id) {
		case "COMM":
			b := make([]byte, min(n, 22))
			if _, err := f.ReadAt(b, off); err != nil || len(b) < 18 {
				return errFormat
			}
			p.channels = int(binary.BigEndian.Uint16(b[0:2]))
			p.bits = int(binary.BigEndian.Uint16(b[6:8]))
			rate = int(extendedToFloat(b[8:18]) + 0.5)
			if len(b) >= 22 {
				switch string(b[18:22]) {
				case "NONE", "twos", "in24", "in32":
				case "sowt":
					p.big = false
				case "fl32", "FL32":
					p.float, p.bits = true, 32
				case "fl64", "FL64":
					p.float, p.bits = true, 64
				default:
					return fmt.Errorf("compressed AIFF-C (%s) isn't supported", string(b[18:22]))
				}
			}
		case "SSND":
			b := make([]byte, 8)
			if _, err := f.ReadAt(b, off); err != nil {
				return err
			}
			skip := int64(binary.BigEndian.Uint32(b[0:4]))
			dataOff, dataLen = off+8+skip, n-8-skip
		}
		return nil
	})
	if err != nil {
		return err
	}
	if dataOff < 0 || rate == 0 {
		return errFormat
	}
	p.bits = (p.bits + 7) / 8 * 8 // e.g. 20-bit samples are stored in 3 bytes
	m.format(rate, p.channels)
	return decodePCM(io.NewSectionReader(f, dataOff, dataLen), p, m)
}

// decodeFFmpeg handles AAC/ALAC/Opus/Vorbis when ffmpeg is on the PATH.
func decodeFFmpeg(path string, rate int, m *mixer) error {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return ErrNoDecoder
	}
	if rate <= 0 {
		rate = 48000
	}
	cmd := exec.Command(bin, "-v", "error", "-i", path, "-f", "f32le", "-ac", "1", "-ar", fmt.Sprint(rate), "-")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	m.format(rate, 1)
	derr := decodePCM(out, pcmFormat{channels: 1, bits: 32, float: true}, m)
	if m.full {
		cmd.Process.Kill()
		cmd.Wait()
		return derr
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg couldn't decode it: %w", err)
	}
	return derr
}
