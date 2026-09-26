package youtube

import (
	"math"
	"math/rand"
	"testing"

	"github.com/tphakala/go-mp3"

	"supersync/internal/soundcloud"
)

// A stream whose frames switch between stereo and joint stereo comes out all
// joint stereo, decoding to exactly the same samples.
func TestJointStereo(t *testing.T) {
	enc, err := mp3.NewEncoder(mp3.EncoderConfig{SampleRate: 44100, Channels: 2, Bitrate: 192000, RateControl: mp3.RateControlFast})
	if err != nil {
		t.Fatal(err)
	}
	out := soundcloud.ID3v2("title", "artist")
	rnd := rand.New(rand.NewSource(1))
	for f := 0; f < 80; f++ {
		l, r := make([]float32, mp3.FrameSize), make([]float32, mp3.FrameSize)
		for i := range l {
			s := float32(math.Sin(float64(f*mp3.FrameSize+i) * 0.05))
			l[i] = s * 0.5
			if f/10%2 == 0 {
				r[i] = s * 0.5 // the same both sides: M/S
			} else {
				r[i] = (rnd.Float32() - 0.5) * 0.8 // unrelated: L/R
			}
		}
		if out, err = enc.EncodeFrame(out, [][]float32{l, r}); err != nil {
			t.Fatal(err)
		}
	}
	if out, err = enc.EncodeFrame(out, nil); err != nil {
		t.Fatal(err)
	}
	modes := func(b []byte) map[byte]int {
		m := map[byte]int{}
		for i := len(soundcloud.ID3v2("title", "artist")); i < len(b); {
			n := frameLen(b[i:])
			if n == 0 {
				break
			}
			m[b[i+3]>>6]++
			i += n
		}
		return m
	}
	if m := modes(out); m[0] == 0 || m[1] == 0 {
		t.Skipf("encoder didn't mix modes: %v", m)
	}
	fixed := append([]byte(nil), out...)
	if !JointStereo(fixed) {
		t.Fatal("nothing changed")
	}
	if m := modes(fixed); m[0] != 0 || m[1] == 0 {
		t.Fatalf("modes after: %v", m)
	}
	if JointStereo(fixed) {
		t.Error("second pass changed it again")
	}
	if a, b := decode(t, out), decode(t, fixed); len(a) != len(b) {
		t.Fatalf("%d vs %d samples", len(a), len(b))
	} else {
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("sample %d differs", i)
			}
		}
	}
}

func decode(t *testing.T, b []byte) []float32 {
	d := mp3.NewDecoder()
	var all []float32
	pcm := make([]float32, 2*mp3.FrameSize)
	for len(b) > 0 {
		n, info, err := d.DecodeFrame(b, pcm)
		if err != nil || info.FrameBytes == 0 {
			break
		}
		all = append(all, pcm[:n*info.Channels]...)
		b = b[info.FrameOffset+info.FrameBytes:]
	}
	if len(all) == 0 {
		t.Fatal("decoded nothing")
	}
	return all
}
