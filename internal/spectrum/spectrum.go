// Package spectrum detects lossy-encoder lowpass cutoffs, which reveal files
// that were converted up from a lower quality (a "320" made from a 128 kbps
// rip, or a WAV made from an MP3).
package spectrum

import (
	"math"
	"math/cmplx"

	"supersync/internal/audio"
	"supersync/internal/dsp"
)

const (
	frameSize = 4096
	bandHz    = 100.0 // spectrum is summarized in 100 Hz bands
	minCliff  = 25.0  // dB drop within a few hundred Hz that counts as a cutoff
	lowestHz  = 11000 // cutoffs below this aren't encoder lowpasses
)

// NoCutoff means the spectrum runs to the top without a sharp cliff.
const NoCutoff = -1

// Analyze returns the frequency (Hz) where the file's spectrum falls off a
// cliff, or NoCutoff. It looks at up to 45 seconds from early in the track.
func Analyze(path string, duration float64) (int, error) {
	start := math.Min(30, duration*0.15)
	samples, rate, err := audio.DecodeWindow(path, start, 45)
	if err != nil {
		return 0, err
	}
	return Cutoff(samples, rate), nil
}

// Cutoff finds where the spectrum drops onto a flat, empty floor: the
// highest band clearly above that floor, provided the level falls at least
// minCliff dB between 1-2 kHz below it and 600 Hz above it. Real encoders (e.g. SoundCloud's 128 kbps
// MP3s) roll off over 1-2 kHz rather than as a brick wall; mastering EQ and
// dark recordings slope far more gently than that.
func Cutoff(samples []float32, rate int) int {
	bands := Bands(samples, rate)
	if bands == nil {
		return NoCutoff
	}
	top := len(bands) - 1
	lo := int(lowestHz / bandHz)
	if top-lo < 20 {
		return NoCutoff
	}
	floor := math.Inf(1)
	for b := lo; b <= top; b++ {
		floor = math.Min(floor, bands[b])
	}
	c := -1
	for b := top; b >= lo; b-- {
		if bands[b] > floor+15 {
			c = b
			break
		}
	}
	if c < lo+20 || c >= top-6 {
		return NoCutoff // nothing above 11 kHz, or content right up to the top
	}
	// Level 1-2 kHz below the edge vs. the loudest band from 600 Hz above it.
	below := 0.0
	for b := c - 20; b <= c-10; b++ {
		below += bands[b]
	}
	below /= 11
	above := math.Inf(-1)
	for b := c + 6; b <= top; b++ {
		above = math.Max(above, bands[b])
	}
	if below < -100 || below-above < minCliff {
		return NoCutoff
	}
	return int(float64(c+1) * bandHz)
}

// Bands returns the average power spectrum in dB per 100 Hz band, from
// frames that aren't near-silent. nil if there's too little audio.
func Bands(samples []float32, rate int) []float64 {
	if rate <= 0 || len(samples) < frameSize*8 {
		return nil
	}
	win := make([]float64, frameSize)
	for i := range win {
		win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(frameSize-1))
	}
	power := make([]float64, frameSize/2)
	buf := make([]complex128, frameSize)
	frames := 0
	for off := 0; off+frameSize <= len(samples); off += frameSize / 2 {
		rms := 0.0
		for i := 0; i < frameSize; i++ {
			v := float64(samples[off+i])
			rms += v * v
		}
		if math.Sqrt(rms/frameSize) < 0.005 { // quieter than about -46 dBFS
			continue
		}
		for i := range buf {
			buf[i] = complex(float64(samples[off+i])*win[i], 0)
		}
		dsp.FFT(buf, false)
		for i := range power {
			a := cmplx.Abs(buf[i])
			power[i] += a * a
		}
		frames++
	}
	if frames < 4 {
		return nil
	}
	binHz := float64(rate) / frameSize
	nb := int(float64(rate) / 2 / bandHz)
	sum := make([]float64, nb)
	cnt := make([]float64, nb)
	for i, p := range power {
		b := int(float64(i) * binHz / bandHz)
		if b < nb {
			sum[b] += p / float64(frames)
			cnt[b]++
		}
	}
	out := make([]float64, nb)
	for b := range out {
		out[b] = -200
		if cnt[b] > 0 && sum[b] > 0 {
			out[b] = 10 * math.Log10(sum[b]/cnt[b]/(frameSize*frameSize))
		}
	}
	return out
}
