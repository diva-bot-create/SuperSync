// Package fingerprint recognises the same recording in different files (an
// MP3 rip and a WAV, a re-upload with another name) by its sound.
//
// A fingerprint is one 32-bit word per 0.1 s of about a minute of audio from
// early in the track. Each bit says whether the energy difference between
// two neighbouring frequency bands (300-2000 Hz) grew or shrank since the
// last frame (Haitsma & Kalker's scheme). Encoding, bitrate and loudness
// barely move those signs, so two copies of one recording agree on most
// bits, and unrelated audio on about half.
package fingerprint

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math"
	"math/bits"

	"supersync/internal/audio"
	"supersync/internal/dsp"
)

const (
	rate    = 5000 // Hz the audio is resampled to
	frame   = 2048 // samples per spectrum (0.41 s)
	hop     = 500  // samples between frames (0.1 s)
	bands   = 33   // 33 bands give 32 bits
	loHz    = 300.0
	hiHz    = 2000.0
	winSecs = 60.0
)

// FP is a fingerprint. It marshals to JSON as base64 to keep caches small.
type FP []uint32

func (f FP) MarshalJSON() ([]byte, error) {
	b := make([]byte, 4*len(f))
	for i, v := range f {
		binary.LittleEndian.PutUint32(b[4*i:], v)
	}
	return []byte(`"` + base64.StdEncoding.EncodeToString(b) + `"`), nil
}

func (f *FP) UnmarshalJSON(j []byte) error {
	if len(j) < 2 || j[0] != '"' {
		*f = nil
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(string(j[1 : len(j)-1]))
	if err != nil || len(b)%4 != 0 {
		return errors.New("bad fingerprint")
	}
	out := make(FP, len(b)/4)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(b[4*i:])
	}
	*f = out
	return nil
}

// Start is where in a track of this length the fingerprint begins: past a
// typical intro's silence, but early enough that edits share it.
func Start(duration float64) float64 { return math.Min(20, duration*0.1) }

// File fingerprints the audio file at path.
func File(path string, duration float64) (FP, error) {
	samples, src, err := audio.DecodeWindow(path, Start(duration), winSecs)
	if err != nil {
		return nil, err
	}
	return Samples(samples, src), nil
}

// Samples fingerprints mono samples at sample rate src.
func Samples(samples []float32, src int) FP {
	if src <= 0 || len(samples) == 0 {
		return nil
	}
	// Resample to 5 kHz: average the source samples each output sample
	// covers (a crude lowpass that's plenty below 2 kHz).
	step := float64(src) / rate
	n := int(float64(len(samples)) / step)
	x := make([]float64, n)
	for i := range x {
		a, b := int(float64(i)*step), int(float64(i+1)*step)
		b = min(max(b, a+1), len(samples))
		var s float64
		for _, v := range samples[a:b] {
			s += float64(v)
		}
		x[i] = s / float64(b-a)
	}
	if len(x) < frame+hop {
		return nil
	}
	// Band edges on a log scale, as FFT bins.
	var edges [bands + 1]int
	for i := range edges {
		hz := loHz * math.Pow(hiHz/loHz, float64(i)/bands)
		edges[i] = int(math.Round(hz * frame / rate))
	}
	win := make([]float64, frame)
	for i := range win {
		win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(frame-1))
	}
	buf := make([]complex128, frame)
	var prev [bands]float64
	var out FP
	for f := 0; f+frame <= len(x); f += hop {
		for i := range buf {
			buf[i] = complex(x[f+i]*win[i], 0)
		}
		dsp.FFT(buf, false)
		var e [bands]float64
		for b := 0; b < bands; b++ {
			for k := edges[b]; k < max(edges[b+1], edges[b]+1); k++ {
				re, im := real(buf[k]), imag(buf[k])
				e[b] += re*re + im*im
			}
		}
		if f > 0 {
			var w uint32
			for b := 0; b < bands-1; b++ {
				if (e[b]-e[b+1])-(prev[b]-prev[b+1]) > 0 {
					w |= 1 << b
				}
			}
			out = append(out, w)
		}
		prev = e
	}
	return out
}

// Match is how two fingerprints line up best.
type Match struct {
	BER     float64 // share of bits that differ at the best lag: ~0.5 for unrelated audio
	Lag     int     // frames b is ahead of a
	Overlap int     // frames compared
}

// Same is the most bits two copies of one recording may disagree on.
const Same = 0.3

// maxLag is how far (in frames) two copies may be shifted: 8 s covers
// different lead-in silences and encoder delays.
const maxLag = 80

// Compare finds the lag (within 8 s) where a and b agree most.
func Compare(a, b FP) Match {
	best := Match{BER: 1}
	for lag := -maxLag; lag <= maxLag; lag++ {
		diff, n := 0, 0
		for i := range a {
			j := i + lag
			if j < 0 || j >= len(b) {
				continue
			}
			diff += bits.OnesCount32(a[i] ^ b[j])
			n++
		}
		if n < 150 {
			continue
		}
		if ber := float64(diff) / float64(32*n); ber < best.BER {
			best = Match{BER: ber, Lag: lag, Overlap: n}
		}
	}
	return best
}

// Candidates finds pairs of fingerprints worth comparing: ones sharing at
// least a few exact 32-bit words. Words that turn up in many tracks (silence,
// steady tones) don't count. It returns index pairs i < j.
func Candidates(fps []FP) [][2]int {
	where := map[uint32][]int32{}
	for i, f := range fps {
		seen := map[uint32]bool{}
		for _, w := range f {
			if !seen[w] {
				seen[w] = true
				where[w] = append(where[w], int32(i))
			}
		}
	}
	shared := map[[2]int]int{}
	for _, ids := range where {
		if len(ids) < 2 || len(ids) > 20 {
			continue
		}
		for x := 0; x < len(ids); x++ {
			for y := x + 1; y < len(ids); y++ {
				shared[[2]int{int(ids[x]), int(ids[y])}]++
			}
		}
	}
	var out [][2]int
	for p, n := range shared {
		if n >= 3 {
			out = append(out, p)
		}
	}
	return out
}
