package fingerprint

import (
	"encoding/json"
	"math"
	"math/rand"
	"testing"
)

// song makes a minute and a half of "music": notes from a seeded melody over
// a beat, so two seeds give two different songs.
func song(seed int64, rate int) []float32 {
	r := rand.New(rand.NewSource(seed))
	out := make([]float32, 90*rate)
	note := 0.0
	for i := range out {
		if i%(rate/4) == 0 {
			note = 220 * math.Pow(2, float64(r.Intn(24))/12)
		}
		t := float64(i) / float64(rate)
		beat := math.Exp(-float64(i%(rate/2)) / float64(rate) * 20)
		out[i] = float32(0.4*math.Sin(2*math.Pi*note*t) + 0.2*math.Sin(2*math.Pi*note*1.5*t) + 0.3*beat*(r.Float64()-0.5))
	}
	return out
}

func TestSameAndDifferent(t *testing.T) {
	a := song(1, 44100)
	// The same song: quieter, with noise, starting 1.2 s later, at 48 kHz.
	b := make([]float32, 0, len(a))
	b = append(b, make([]float32, int(1.2*44100))...)
	r := rand.New(rand.NewSource(9))
	for _, v := range a {
		b = append(b, v*0.5+float32(r.NormFloat64()*0.01))
	}
	b48 := make([]float32, len(b)*48000/44100)
	for i := range b48 {
		b48[i] = b[i*44100/48000]
	}
	c := song(2, 44100)

	fa, fb, fc := Samples(a[20*44100:], 44100), Samples(b48[20*48000:], 48000), Samples(c[20*44100:], 44100)
	if m := Compare(fa, fb); m.BER > Same || m.Lag < 10 || m.Lag > 14 {
		t.Errorf("same song: %+v", m)
	}
	if m := Compare(fa, fc); m.BER < 0.4 {
		t.Errorf("different songs: %+v", m)
	}
	if got := Candidates([]FP{fa, fc, fb}); len(got) != 1 || got[0] != [2]int{0, 2} {
		t.Errorf("candidates: %v", got)
	}

	j, _ := json.Marshal(fa)
	var back FP
	if err := json.Unmarshal(j, &back); err != nil || len(back) != len(fa) || back[5] != fa[5] {
		t.Errorf("json round trip: %v", err)
	}
}
