package spectrum

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func ff(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("ffmpeg", append([]string{"-loglevel", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %s", args, out)
	}
}

func TestCutoff(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	d := t.TempDir()
	p := func(n string) string { return filepath.Join(d, n) }
	// Pink noise with a beat is a fair stand-in for a full-range master.
	src := "anoisesrc=color=pink:amplitude=0.4:r=44100:d=90,volume='0.4+0.6*lt(mod(t,0.5),0.25)':eval=frame"
	ff(t, "-f", "lavfi", "-i", src, "-c:a", "pcm_s16le", p("master.wav"))
	// ffmpeg's encoders don't lowpass like real-world ones, so set the cutoffs
	// they'd use: LAME 320 ~20.5 kHz, LAME 128 ~17 kHz, SoundCloud's 128 ~16 kHz.
	ff(t, "-i", p("master.wav"), "-b:a", "320k", "-cutoff", "20500", p("real320.mp3"))
	ff(t, "-i", p("master.wav"), "-b:a", "128k", "-cutoff", "16000", p("rip128.mp3"))
	ff(t, "-i", p("master.wav"), "-b:a", "128k", "-cutoff", "17000", p("lame128.mp3"))
	ff(t, "-i", p("rip128.mp3"), "-b:a", "320k", "-cutoff", "20500", p("fake320.mp3"))
	ff(t, "-i", p("lame128.mp3"), "-c:a", "pcm_s16le", p("fake.wav"))
	ff(t, "-i", p("master.wav"), "-c:a", "aac", "-b:a", "160k", "-cutoff", "16000", p("rip160.m4a"))
	ff(t, "-i", p("rip160.m4a"), "-c:a", "flac", p("fake.flac"))
	ff(t, "-i", p("master.wav"), "-c:a", "aac", "-b:a", "256k", p("real256.m4a"))
	// SoundCloud's own 128 kbps streams roll off over ~1.5 kHz instead of a brick
	// wall (measured on 30 real downloads); a steep filter stack mimics that.
	ff(t, "-i", p("master.wav"), "-af", strings.Repeat("lowpass=f=15800,", 5)+"volume=1", "-c:a", "pcm_s16le", p("scroll.wav"))
	// A dark master: gentle 12 dB/octave rolloff from 5 kHz, but no brick wall.
	ff(t, "-i", p("master.wav"), "-af", "lowpass=f=5000", "-c:a", "pcm_s16le", p("dark.wav"))

	cases := []struct {
		file   string
		lo, hi int // expected cutoff range; NoCutoff == -1
	}{
		{"master.wav", NoCutoff, NoCutoff},
		{"dark.wav", NoCutoff, NoCutoff},
		{"real320.mp3", 19500, 21000},
		{"real256.m4a", NoCutoff, 22050}, // none, or a high one
		{"rip128.mp3", 15800, 16300},
		{"fake320.mp3", 15800, 16300},
		{"fake.wav", 16800, 17300},
		{"fake.flac", 15500, 17000},  // AAC's transition band runs a little past its setting
		{"scroll.wav", 15500, 18600}, // detected as a cutoff, not full range
	}
	for _, c := range cases {
		got, err := Analyze(p(c.file), 90)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%-12s cutoff %d", c.file, got)
		if got < c.lo || got > c.hi {
			t.Errorf("%s: cutoff %d, want %d..%d", c.file, got, c.lo, c.hi)
		}
	}
}
