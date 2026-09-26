package youtube

import "testing"

func TestIDs(t *testing.T) {
	for _, c := range []struct{ link, pl, v string }{
		{"https://www.youtube.com/watch?v=jNQXAC9IVRw", "", "jNQXAC9IVRw"},
		{"youtu.be/jNQXAC9IVRw?si=x", "", "jNQXAC9IVRw"},
		{"https://www.youtube.com/shorts/jNQXAC9IVRw", "", "jNQXAC9IVRw"},
		{"https://www.youtube.com/playlist?list=PLabc", "PLabc", ""},
		{"https://music.youtube.com/watch?v=jNQXAC9IVRw&list=OLAKxyz", "OLAKxyz", "jNQXAC9IVRw"},
		{"https://www.youtube.com/watch?v=jNQXAC9IVRw&list=RDjNQXAC9IVRw", "", "jNQXAC9IVRw"}, // mixes are endless
	} {
		pl, v, err := ids(c.link)
		if err != nil || pl != c.pl || v != c.v {
			t.Errorf("%s: got %q %q %v", c.link, pl, v, err)
		}
	}
	if _, _, err := ids("https://www.youtube.com/@someone"); err == nil {
		t.Error("channel page accepted as a video")
	}
	if !IsLink("m.youtube.com/watch?v=x") || IsLink("soundcloud.com/a/b") {
		t.Error("IsLink")
	}
}

func TestClock(t *testing.T) {
	if clock("1:02:03") != 3723 || clock("4:05") != 245 || clock("LIVE") != 0 {
		t.Error("clock")
	}
}
