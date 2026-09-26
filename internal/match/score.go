package match

import (
	"math"
	"strings"
)

// Thresholds for Score.
const (
	Sure  = 0.80 // treat as the same recording
	Maybe = 0.60 // worth showing to the user
)

// Score compares two keys and returns 0..1.
func Score(a, b Key) float64 {
	if a.Swapped && b.Swapped {
		return 0 // same comparison as the two unswapped readings
	}
	t := dice(a.Title, b.Title)
	if strings.Join(a.Title, "") == strings.Join(b.Title, "") {
		t = 1
	}
	if t < 0.5 {
		return t * 0.5
	}

	art := 0.5 // unknown artist on either side is neutral
	if len(a.Artist) > 0 && len(b.Artist) > 0 {
		art = overlap(a.Artist, b.Artist)
		if art == 0 && strings.Join(a.Artist, "") == strings.Join(b.Artist, "") {
			art = 1
		}
	}
	s := 0.65*t + 0.35*art

	// A remix is a different recording from the original (or from another remix).
	if !sameSet(a.Version, b.Version) {
		if len(a.Version) == 0 || len(b.Version) == 0 {
			s *= 0.6
		} else {
			s *= 0.5 + 0.4*jaccard(a.Version, b.Version)
		}
	}

	if a.Swapped || b.Swapped {
		s *= 0.95
	}

	if a.Duration > 0 && b.Duration > 0 {
		d := math.Abs(a.Duration - b.Duration)
		switch {
		case d <= 3:
			s += 0.05
		case d > 90:
			s *= 0.85
		}
	}
	return math.Min(s, 1)
}

// Best returns the best score across every pairing of the two key sets.
func Best(as, bs []Key) float64 {
	best := 0.0
	for _, a := range as {
		for _, b := range bs {
			if s := Score(a, b); s > best {
				best = s
			}
		}
	}
	return best
}

func dice(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	return 2 * float64(inter(a, b)) / float64(len(uniq(a))+len(uniq(b)))
}

func overlap(a, b []string) float64 {
	n := min(len(uniq(a)), len(uniq(b)))
	if n == 0 {
		return 0
	}
	return float64(inter(a, b)) / float64(n)
}

func jaccard(a, b []string) float64 {
	i := inter(a, b)
	u := len(uniq(a)) + len(uniq(b)) - i
	if u == 0 {
		return 1
	}
	return float64(i) / float64(u)
}

// inter counts tokens of a found in b, allowing one typo in longer words
// ("herre" / "here", "amsterdam" / "amsterdamn").
func inter(a, b []string) int {
	m := set(b...)
	n := 0
	for _, w := range uniq(a) {
		if m[w] {
			n++
			continue
		}
		if len(w) >= 4 {
			for _, v := range b {
				if len(v) >= 4 && oneEdit(w, v) {
					n++
					break
				}
			}
		}
	}
	return n
}

// oneEdit reports whether a and b differ by at most one insert, delete or substitution.
func oneEdit(a, b string) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b)-len(a) > 1 {
		return false
	}
	i := 0
	for i < len(a) && a[i] == b[i] {
		i++
	}
	if len(a) == len(b) {
		return a[i+min(1, len(a)-i):] == b[i+min(1, len(b)-i):]
	}
	return a[i:] == b[i+1:]
}

func sameSet(a, b []string) bool {
	return len(uniq(a)) == len(uniq(b)) && inter(a, b) == len(uniq(a))
}

// SameFile is a stricter test for two files in the same library: the
// comparison must rest on known artists on both sides, or, when an artist is
// missing (untagged file, channel name as artist), on the lengths agreeing to
// within two seconds.
func SameFile(as, bs []Key) float64 {
	best := 0.0
	for _, a := range as {
		for _, b := range bs {
			switch {
			case len(a.Artist) > 0 && len(b.Artist) > 0:
				best = math.Max(best, Score(a, b))
			case a.Duration > 0 && math.Abs(a.Duration-b.Duration) <= 2:
				best = math.Max(best, Score(a, b))
			}
		}
	}
	return best
}
