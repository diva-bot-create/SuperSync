package anlz

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Uses pyrekordbox's sample analysis files (MIT): set REKORDBOX_ANLZ to a
// folder of *-ANLZ0000.DAT/.EXT pairs.
func TestRead(t *testing.T) {
	dir := os.Getenv("REKORDBOX_ANLZ")
	if dir == "" {
		t.Skip("REKORDBOX_ANLZ not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.DAT"))
	if len(files) == 0 {
		t.Fatal("no .DAT files")
	}
	for _, f := range files {
		a, err := Read(f)
		if err != nil {
			t.Fatal(err)
		}
		if len(a.Grid) < 50 {
			t.Fatalf("%s: only %d beats", f, len(a.Grid))
		}
		for i, b := range a.Grid {
			if b.Bar < 1 || b.Bar > 4 || b.BPM < 40 || b.BPM > 250 {
				t.Fatalf("%s beat %d: %+v", f, i, b)
			}
			if i > 0 {
				prev := a.Grid[i-1]
				// Beats advance by one, wrapping 4 -> 1, spaced by the tempo.
				if b.Bar != prev.Bar%4+1 {
					t.Fatalf("%s beat %d: bar %d after %d", f, i, b.Bar, prev.Bar)
				}
				if gap, want := float64(b.Ms-prev.Ms), 60000/prev.BPM; math.Abs(gap-want) > 2 {
					t.Fatalf("%s beat %d: %v ms after previous, tempo says %.1f", f, i, gap, want)
				}
			}
		}
		secs := float64(a.Grid[len(a.Grid)-1].Ms) / 1000
		if len(a.Detail) == 0 || math.Abs(float64(len(a.Detail))/DetailRate-secs) > 30 {
			t.Errorf("%s: detail waveform %d columns for ~%.0fs of beats", f, len(a.Detail), secs)
		}
		t.Logf("%s: %d beats, %.2f BPM, first beat %d ms (bar %d), detail %d cols (%.0f s)",
			filepath.Base(f), len(a.Grid), a.Grid[0].BPM, a.Grid[0].Ms, a.Grid[0].Bar, len(a.Detail), float64(len(a.Detail))/DetailRate)
	}
}
