package analyze

import (
	"math"
	"strconv"
	"sync"

	"supersync/internal/fingerprint"
	"supersync/internal/library"
)

// Two files whose fingerprints differ on more than this share of bits are
// different recordings, even if their names say otherwise (a remix and the
// original, two songs with one title).
const differentSound = 0.42

// soundPairs finds files that sound the same (by fingerprint) and are about
// the same length, as index pairs into lib.Tracks. It's cached until the
// library or its fingerprints change.
func soundPairs(lib *library.Library) [][2]int {
	var fps []fingerprint.FP
	var idx []int
	for i, t := range lib.Tracks {
		if len(t.FP) > 0 {
			fps = append(fps, t.FP)
			idx = append(idx, i)
		}
	}
	key := lib.Key + "|" + lib.ScannedAt.String() + "|" + strconv.Itoa(len(fps)) + "|" + strconv.Itoa(len(lib.Tracks))
	soundCache.Lock()
	defer soundCache.Unlock()
	if soundCache.key == key {
		return soundCache.pairs
	}
	var out [][2]int
	for _, c := range fingerprint.Candidates(fps) {
		i, j := idx[c[0]], idx[c[1]]
		a, b := lib.Tracks[i], lib.Tracks[j]
		if a.Duration > 0 && b.Duration > 0 && math.Abs(a.Duration-b.Duration) > 15 {
			continue // an edit and an extended mix sharing an intro
		}
		if fingerprint.Compare(a.FP, b.FP).BER <= fingerprint.Same {
			out = append(out, [2]int{i, j})
		}
	}
	soundCache.key, soundCache.pairs = key, out
	return out
}

var soundCache struct {
	sync.Mutex
	key   string
	pairs [][2]int
}

// soundsDifferent reports whether both files have fingerprints and they
// clearly don't match.
func soundsDifferent(a, b *library.Track) bool {
	if len(a.FP) == 0 || len(b.FP) == 0 {
		return false
	}
	return fingerprint.Compare(a.FP, b.FP).BER > differentSound
}
