package audio

import (
	"bytes"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Generates small files with ffmpeg (skipped when it isn't installed) and
// checks what Read reports for each format.
func TestRead(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	cases := []struct {
		file     string
		args     []string
		codec    string
		lossless bool
		kbps     int // 0 = don't check
		hz, bits int
	}{
		{"cbr.mp3", []string{"-c:a", "libmp3lame", "-b:a", "320k"}, "MP3", false, 320, 44100, 0},
		{"lo.mp3", []string{"-c:a", "libmp3lame", "-b:a", "128k", "-write_xing", "0"}, "MP3", false, 128, 44100, 0},
		{"a.flac", []string{"-c:a", "flac", "-sample_fmt", "s32", "-ar", "96000"}, "FLAC", true, 0, 96000, 24},
		{"a.aiff", []string{"-c:a", "pcm_s16be", "-write_id3v2", "1"}, "AIFF", true, 1411, 44100, 16},
		{"a.wav", []string{"-c:a", "pcm_s24le"}, "WAV", true, 2117, 44100, 24},
		{"a.m4a", []string{"-c:a", "alac", "-sample_fmt", "s16p"}, "ALAC", true, 0, 44100, 16},
		{"b.m4a", []string{"-c:a", "aac", "-b:a", "128k"}, "AAC", false, 0, 44100, 0},
		{"a.opus", []string{"-c:a", "libopus", "-b:a", "64k"}, "Opus", false, 0, 48000, 0},
	}
	for _, c := range cases {
		p := filepath.Join(dir, c.file)
		args := append([]string{"-loglevel", "error", "-y", "-f", "lavfi", "-i", "anoisesrc=d=12.5:a=0.2:r=44100", "-ac", "2",
			"-metadata", "artist=Some Artist", "-metadata", "title=Some Title"}, c.args...)
		if out, err := exec.Command("ffmpeg", append(args, p)...).CombinedOutput(); err != nil {
			t.Logf("skipping %s: ffmpeg can't encode it here: %s", c.file, out)
			continue
		}
		in, err := Read(p)
		if err != nil {
			t.Fatal(err)
		}
		if in.Err != "" || in.Codec != c.codec || in.Lossless != c.lossless || in.SampleRate != c.hz || in.BitDepth != c.bits {
			t.Errorf("%s: got %+v", c.file, in)
		}
		if math.Abs(in.Duration-12.5) > 0.1 {
			t.Errorf("%s: duration %.3f", c.file, in.Duration)
		}
		if c.kbps > 0 && math.Abs(float64(in.Bitrate-c.kbps)) > float64(c.kbps)/50 {
			t.Errorf("%s: bitrate %d, want ~%d", c.file, in.Bitrate, c.kbps)
		}
		if in.Artist != "Some Artist" || in.Title != "Some Title" {
			t.Errorf("%s: tags %q / %q", c.file, in.Artist, in.Title)
		}
	}
}

func TestAIFFAsWAV(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	aiff, ref := filepath.Join(dir, "a.aiff"), filepath.Join(dir, "ref.wav")
	for _, c := range [][]string{
		{"-f", "lavfi", "-i", "anoisesrc=d=3:r=44100", "-ac", "2", "-c:a", "pcm_s24be", aiff},
		{"-i", aiff, "-c:a", "pcm_s24le", ref},
	} {
		if out, err := exec.Command("ffmpeg", append([]string{"-loglevel", "error", "-y"}, c...)...).CombinedOutput(); err != nil {
			t.Fatalf("%s", out)
		}
	}
	r, err := AIFFAsWAV(aiff)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, _ := io.ReadAll(r)
	want, _ := os.ReadFile(ref)
	// Compare the sample data (ffmpeg's header may carry extra chunks).
	if len(got) < 44 || !bytes.Equal(got[44:], want[len(want)-(len(got)-44):]) {
		t.Fatalf("samples differ (got %d bytes, ref %d)", len(got), len(want))
	}
	// Seeking into the middle of a sample still yields the right bytes.
	r.Seek(1001, io.SeekStart)
	part := make([]byte, 77)
	io.ReadFull(r, part)
	if !bytes.Equal(part, got[1001:1078]) {
		t.Fatal("unaligned read differs")
	}
}
