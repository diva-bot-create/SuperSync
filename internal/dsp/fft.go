// Package dsp has the signal-processing primitives SuperSync needs.
package dsp

import (
	"math"
	"math/cmplx"
)

// FFT is an in-place iterative radix-2 FFT; len(x) must be a power of two.
// The inverse is unscaled.
func FFT(x []complex128, inverse bool) {
	n := len(x)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	sign := -1.0
	if inverse {
		sign = 1
	}
	for size := 2; size <= n; size <<= 1 {
		w := cmplx.Rect(1, sign*2*math.Pi/float64(size))
		for start := 0; start < n; start += size {
			wk := complex(1, 0)
			for k := 0; k < size/2; k++ {
				u := x[start+k]
				v := x[start+k+size/2] * wk
				x[start+k] = u + v
				x[start+k+size/2] = u - v
				wk *= w
			}
		}
	}
}
