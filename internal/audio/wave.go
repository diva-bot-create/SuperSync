package audio

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// Waveform is an overview for display: per column, the peak level and how
// that energy splits between lows, mids and highs (rekordbox-style colouring).
type Waveform struct {
	Duration float64 `json:"secs"`
	Peak     []uint8 `json:"peak"`
	Low      []uint8 `json:"low"`
	Mid      []uint8 `json:"mid"`
	High     []uint8 `json:"high"`
}

const waveRate = 11025

// ComputeWaveform decodes the file and summarises it into n columns. Results
// are cached in cacheDir keyed by the file's path, size and modification time.
func ComputeWaveform(path string, n int, cacheDir string) (*Waveform, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d|%d", path, st.Size(), st.ModTime().UnixNano(), n)))
	cache := filepath.Join(cacheDir, hex.EncodeToString(sum[:10])+".json")
	if b, err := os.ReadFile(cache); err == nil {
		var w Waveform
		if json.Unmarshal(b, &w) == nil {
			return &w, nil
		}
	}
	s, _, err := DecodeMono(path, waveRate)
	if err != nil {
		return nil, err
	}
	w := summarise(s, n)
	if b, err := json.Marshal(w); err == nil {
		os.MkdirAll(cacheDir, 0o755)
		os.WriteFile(cache, b, 0o644)
	}
	return w, nil
}

func summarise(s []float32, n int) *Waveform {
	w := &Waveform{Duration: float64(len(s)) / waveRate, Peak: make([]uint8, n), Low: make([]uint8, n), Mid: make([]uint8, n), High: make([]uint8, n)}
	if len(s) == 0 || n == 0 {
		return w
	}
	// One-pole filters split the signal into bands at ~200 Hz and ~2.5 kHz.
	coef := func(fc float64) float64 { return 1 - math.Exp(-2*math.Pi*fc/waveRate) }
	a1, a2 := coef(200), coef(2500)
	var lp1, lp2 float64
	peak := make([]float64, n)
	lo, mi, hi := make([]float64, n), make([]float64, n), make([]float64, n)
	per := float64(len(s)) / float64(n)
	for i, v := range s {
		x := float64(v)
		lp1 += a1 * (x - lp1)
		lp2 += a2 * (x - lp2)
		col := min(int(float64(i)/per), n-1)
		peak[col] = math.Max(peak[col], math.Abs(x))
		lo[col] += lp1 * lp1
		mi[col] += (lp2 - lp1) * (lp2 - lp1)
		hi[col] += (x - lp2) * (x - lp2)
	}
	maxPeak := 1e-9
	for _, p := range peak {
		maxPeak = math.Max(maxPeak, p)
	}
	for c := 0; c < n; c++ {
		w.Peak[c] = uint8(math.Min(255, peak[c]/maxPeak*255))
		// Band shares, with highs lifted a little so hats show up.
		l, m, h := math.Sqrt(lo[c]), math.Sqrt(mi[c]), math.Sqrt(hi[c])*1.6
		t := l + m + h
		if t > 0 {
			w.Low[c], w.Mid[c], w.High[c] = uint8(l/t*255), uint8(m/t*255), uint8(h/t*255)
		}
	}
	return w
}
