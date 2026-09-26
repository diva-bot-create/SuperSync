package update

import "testing"

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.1.1", "v0.1", true},
		{"v0.1", "v0.1.1", false},
		{"v0.1", "v0.1.0", false},
		{"v0.2", "v0.1.9", true},
		{"v0.10", "v0.9", true},
		{"v1.0", "dev", false},
		{"v1.0", "v0.1-3-gabc", false},
		{"v0.1.1", "v0.1.1", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
