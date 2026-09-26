// Package align measures the time offset between two recordings of the same
// audio (e.g. a SoundCloud rip and the purchased master) so cue points can be
// moved from one to the other.
package align

import (
	"math"
	"math/cmplx"

	"supersync/internal/dsp"
)

// Rate is the sample rate Offset expects (4 kHz: 0.25 ms per sample, refined
// further by interpolating the correlation peak).
const Rate = 4000

// Result of comparing two recordings. A cue at t seconds in a sits at
// t + Offset seconds in b.
type Result struct {
	Offset float64 `json:"offset"`
	// Confidence is the normalized correlation at the chosen offset (0..1).
	// Same master, different encodings: ~0.8-1.0. Unrelated audio: < 0.3.
	Confidence float64 `json:"confidence"`
	// Checks are offsets measured independently at later points in a. When
	// they disagree with Offset the files are different edits.
	Checks []float64 `json:"checks"`
	// SameEdit is true when every check agrees with Offset within 5 ms.
	SameEdit bool `json:"sameEdit"`
}

const (
	headSecs  = 90.0 // how much of a the global search looks for in b
	checkSecs = 8.0  // window for each spot check
	checkSpan = 0.5  // spot checks search +/- this many seconds around Offset
	agreeSecs = 0.005
)

// Offset finds where a's audio sits within b.
func Offset(a, b []float32) Result {
	if len(a) < Rate*20 || len(b) < Rate*20 {
		return Result{} // too short to line up reliably
	}
	da, db := diff(a), diff(b)

	// 1. Global search: where does a's opening minute and a half occur in b?
	// Searching all of b handles extended mixes (radio edit + longer intro).
	wa := da[:min(len(da), int(headSecs*Rate))]
	lag, conf := bestLag(wa, db, -len(wa)+Rate*10, len(db)-Rate*10)
	res := Result{Offset: lag / Rate, Confidence: conf, SameEdit: true}

	// 2. Spot checks at 25/50/75% of a, each searched near the global offset.
	for _, frac := range []float64{0.25, 0.5, 0.75} {
		start := int(frac * float64(len(da)))
		seg := da[start:min(len(da), start+int(checkSecs*Rate))]
		if len(seg) < Rate*2 {
			continue
		}
		center := start + int(math.Round(lag))
		lo := center - int(checkSpan*Rate)
		hi := center + len(seg) + int(checkSpan*Rate)
		if lo < 0 || hi > len(db) {
			res.SameEdit = false // b doesn't even reach this far
			res.Checks = append(res.Checks, math.NaN())
			continue
		}
		l, c := bestLag(seg, db[lo:hi], 0, hi-lo-len(seg))
		off := (float64(lo) + l - float64(start)) / Rate
		res.Checks = append(res.Checks, off)
		if c < 0.5 || math.Abs(off-res.Offset) > agreeSecs {
			res.SameEdit = false
		}
	}
	return res
}

// diff is a first-difference (pre-emphasis) of x. It removes DC and weights
// transients, which sharpens the correlation peak.
func diff(x []float32) []float64 {
	out := make([]float64, len(x))
	for i := 1; i < len(x); i++ {
		out[i] = float64(x[i] - x[i-1])
	}
	return out
}

// bestLag returns the lag L in [minLag, maxLag] maximizing the normalized
// correlation sum a[i]*b[i+L], with sub-sample precision, and that correlation.
func bestLag(a, b []float64, minLag, maxLag int) (float64, float64) {
	n := 1
	for n < len(a)+len(b) {
		n <<= 1
	}
	fa := make([]complex128, n)
	fb := make([]complex128, n)
	for i, v := range a {
		fa[i] = complex(v, 0)
	}
	for i, v := range b {
		fb[i] = complex(v, 0)
	}
	dsp.FFT(fa, false)
	dsp.FFT(fb, false)
	for i := range fa {
		fa[i] = cmplx.Conj(fa[i]) * fb[i]
	}
	dsp.FFT(fa, true)
	raw := func(l int) float64 { // correlation at lag l (negative lags wrap)
		return real(fa[(l%n+n)%n]) / float64(n)
	}

	// Prefix sums of squares for the energy of each overlap.
	pa := prefixSq(a)
	pb := prefixSq(b)
	norm := func(l int) float64 {
		i0, i1 := max(0, -l), min(len(a), len(b)-l)
		if i1-i0 <= 0 {
			return 0
		}
		ea := pa[i1] - pa[i0]
		eb := pb[i1+l] - pb[i0+l]
		if ea <= 0 || eb <= 0 {
			return 0
		}
		return raw(l) / math.Sqrt(ea*eb)
	}

	best, bestL := math.Inf(-1), 0
	for l := max(minLag, -len(a)+1); l <= min(maxLag, len(b)-1); l++ {
		if v := norm(l); v > best {
			best, bestL = v, l
		}
	}
	// Parabolic interpolation around the peak.
	y0, y1, y2 := norm(bestL-1), best, norm(bestL+1)
	frac := 0.0
	if d := y0 - 2*y1 + y2; d < 0 {
		frac = 0.5 * (y0 - y2) / d
	}
	return float64(bestL) + frac, math.Max(0, best)
}

func prefixSq(x []float64) []float64 {
	p := make([]float64, len(x)+1)
	for i, v := range x {
		p[i+1] = p[i] + v*v
	}
	return p
}
