package align

import (
	"math"
	"os/exec"
	"path/filepath"
	"testing"

	"supersync/internal/audio"
)

// A loop-based "track": kick every beat, hats, a chord change every 2s, and
// a slow riser so no two bars are sample-identical (as in real music).
const song = "0.6*sin(2*PI*55*t)*exp(-9*mod(t,0.5))" +
	"+0.15*sin(2*PI*7000*t)*exp(-30*mod(t+0.25,0.5))" +
	"+0.2*sin(2*PI*(220+55*floor(mod(t/2,4)))*t)" +
	"+0.1*sin(2*PI*(300+t)*t)"

func gen(t *testing.T, dir, name, filter string, args ...string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	cmd := append([]string{"-loglevel", "error", "-y", "-f", "lavfi", "-i", filter}, args...)
	if out, err := exec.Command("ffmpeg", append(cmd, p)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %s", out)
	}
	return p
}

func decode(t *testing.T, p string) []float32 {
	t.Helper()
	s, _, err := audio.DecodeMono(p, Rate)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOffset(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	src := "aevalsrc='" + song + "':s=44100:d=240"
	master := gen(t, dir, "master.wav", src, "-c:a", "pcm_s16le")
	// The SoundCloud upload had 1.234s of extra silence at the start, then got ripped to MP3.
	rip := gen(t, dir, "rip.mp3", src+",adelay=1234:all=1", "-c:a", "libmp3lame", "-b:a", "128k")
	flac := gen(t, dir, "master.flac", src, "-c:a", "flac")
	aiff := gen(t, dir, "master.aiff", src, "-c:a", "pcm_s24be")
	// A different edit: 16 seconds cut out of the middle.
	edit := filepath.Join(dir, "edit.wav")
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-i", master,
		"-filter_complex", "[0]atrim=end=100[a];[0]atrim=start=116,asetpts=PTS-STARTPTS[b];[a][b]concat=n=2:v=0:a=1", edit).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %s", out)
	}
	// An extended mix: a 64-second DJ-friendly intro (kick only) in front of the same audio.
	ext := filepath.Join(dir, "ext.wav")
	if out, err := exec.Command("ffmpeg", "-loglevel", "error",
		"-f", "lavfi", "-i", "aevalsrc='0.6*sin(2*PI*55*t)*exp(-9*mod(t,0.5))':s=44100:d=64", "-i", master,
		"-filter_complex", "[0][1]concat=n=2:v=0:a=1", ext).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %s", out)
	}
	other := gen(t, dir, "other.wav", "aevalsrc='0.5*sin(2*PI*330*t)*exp(-5*mod(t,0.37))+0.1*random(0)':s=44100:d=240", "-c:a", "pcm_s16le")

	m := decode(t, master)

	// LAME adds 1105 samples of delay (576 encoder + 529 decoder), which
	// rekordbox keeps, so the rip's audio sits 1.234s + 25.06ms late.
	want := -(1.234 + 1105.0/44100)
	r := Offset(decode(t, rip), m)
	t.Logf("rip->master: %+.4f (want %+.4f) conf %.2f checks %v", r.Offset, want, r.Confidence, r.Checks)
	if math.Abs(r.Offset-want) > 0.001 || !r.SameEdit || r.Confidence < 0.7 {
		t.Errorf("rip->master: %+v", r)
	}

	for _, p := range []string{flac, aiff} {
		r := Offset(m, decode(t, p))
		if math.Abs(r.Offset) > 0.0005 || !r.SameEdit || r.Confidence < 0.95 {
			t.Errorf("%s: %+v", filepath.Base(p), r)
		}
	}

	r = Offset(m, decode(t, ext))
	t.Logf("radio->extended: %+v", r)
	if math.Abs(r.Offset-64) > 0.0005 || !r.SameEdit {
		t.Errorf("radio->extended: %+v", r)
	}

	r = Offset(m, decode(t, edit))
	t.Logf("edit: %+v", r)
	if r.SameEdit {
		t.Errorf("different edit not detected: %+v", r)
	}

	r = Offset(m, decode(t, other))
	t.Logf("unrelated: %+v", r)
	if r.Confidence > 0.4 || r.SameEdit {
		t.Errorf("unrelated audio looked similar: %+v", r)
	}
}
