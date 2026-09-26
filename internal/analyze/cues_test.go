package analyze

import (
	"encoding/xml"
	"testing"

	"supersync/internal/rekordbox"
)

func elem(kv ...string) rekordbox.Elem {
	var e rekordbox.Elem
	for i := 0; i < len(kv); i += 2 {
		e.Attrs = append(e.Attrs, xml.Attr{Name: xml.Name{Local: kv[i]}, Value: kv[i+1]})
	}
	return e
}

func TestShiftMarks(t *testing.T) {
	marks := []rekordbox.Elem{
		elem("Name", "intro", "Type", "0", "Start", "0.500", "Num", "0"),
		elem("Name", "drop", "Type", "0", "Start", "61.025", "Num", "1"),
		elem("Name", "loop", "Type", "4", "Start", "90.000", "End", "97.500", "Num", "-1"),
		elem("Name", "outro", "Type", "0", "Start", "299.000", "Num", "2"),
	}
	out, kept, dropped := shiftMarks(marks, -1.259, 297)
	if kept != 2 || dropped != 2 {
		t.Fatalf("kept %d dropped %d: %+v", kept, dropped, out)
	}
	if out[0].Get("Start") != "59.766" || out[1].Get("Start") != "88.741" || out[1].Get("End") != "96.241" {
		t.Errorf("shifted: %+v", out)
	}
	if out[0].Get("Name") != "drop" || out[0].Get("Num") != "1" {
		t.Errorf("attributes lost: %+v", out[0])
	}
}

func TestShiftTempos(t *testing.T) {
	// 128 BPM (beat = 0.46875s), grid starts at 0.2s on beat 1; a tempo change later.
	grid := []rekordbox.Elem{
		elem("Inizio", "0.200", "Bpm", "128.00", "Metro", "4/4", "Battito", "1"),
		elem("Inizio", "120.200", "Bpm", "130.00", "Metro", "4/4", "Battito", "1"),
	}
	// Shifted 1s earlier: first marker at -0.8s -> walk forward 2 beats to ~0.1375s, beat 3.
	out := shiftTempos(grid, -1)
	if len(out) != 2 || out[0].Get("Inizio") != "0.137" || out[0].Get("Battito") != "3" || out[1].Get("Inizio") != "119.200" {
		t.Errorf("got %+v", out)
	}
	// Lands a hair before 0:00: snap to 0, don't jump a beat.
	out = shiftTempos(grid, -0.2001)
	if out[0].Get("Inizio") != "0.000" || out[0].Get("Battito") != "1" {
		t.Errorf("got %+v", out)
	}
	// Radio edit -> extended mix with a 64s intro: grid extends back to the start.
	out = shiftTempos(grid[:1], 64)
	if out[0].Get("Inizio") != "0.450" || out[0].Get("Battito") != "1" { // 64.2s = 136 beats + 0.45s
		t.Errorf("got %+v", out)
	}
	// Shifted later: plain move.
	out = shiftTempos(grid, 0.025)
	if out[0].Get("Inizio") != "0.225" || out[0].Get("Battito") != "1" {
		t.Errorf("got %+v", out)
	}
}
