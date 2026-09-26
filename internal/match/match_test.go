package match

import "testing"

type side struct{ artist, title string }

func key(s side) []Key {
	if s.artist == "file" {
		return ParseFilename(s.title)
	}
	return Parse(s.artist, s.title)
}

func TestSameTrack(t *testing.T) {
	cases := []struct{ sc, lib side }{
		{side{"Some Label", "Fred again.. - Delilah (pull me out of this) [FREE DL]"}, side{"Fred again..", "Delilah (pull me out of this)"}},
		{side{"DJ Uploader", "PREMIERE: Chris Stussy - All Night Long"}, side{"Chris Stussy", "All Night Long (Original Mix)"}},
		{side{"Toolroom", "Toolroom | Mau P - Drugs From Amsterdam (Extended Mix)"}, side{"file", "Mau P - Drugs From Amsterdam"}},
		{side{"Kettama", "Kettama - It's A Thing [OUT NOW]"}, side{"KETTAMA", "Its A Thing"}},
		{side{"Uploader", "Bicep - Glue (Hammer Remix)"}, side{"Bicep", "Glue - Hammer Remix"}},
		{side{"Artist Name", "Untitled Track"}, side{"file", "01 - Artist Name - Untitled Track"}},
		{side{"Beyoncé", "Cuff It (Wetter Remix)"}, side{"Beyonce", "CUFF IT - WETTER Remix"}},
		{side{"Salute", "Salute ft. Sammy Virji - Peach"}, side{"Salute, Sammy Virji", "Peach"}},
		{side{"x", "Overmono - So U Kno"}, side{"file", "Overmono - So U Kno (1)"}},
		{side{"Tech House", "Tech House | H0t 1n H3r3 (JOHNY GAMBLE BACKWORK EDIT) *FREE DL*"}, side{"Nelly", "Hot In Herre (Johny Gamble Backwork Edit)"}},
		{side{"Remix Channel", "Bass House | @tm05ph3r3 (Saint Jabir Remix) *FREE DL*"}, side{"file", "Atmosphere (Saint Jabir Remix)"}},
		{side{"x", "Club Remix | Pump Up The Jam (Swartchback X Yolan Paris Bootleg) *FREE DL*"}, side{"Technotronic", "Pump Up The Jam (Swartchback x Yolan Paris Bootleg)"}},
		{side{"Tech House", "Tech House | Santino - Bora Bora"}, side{"Santino", "Bora Bora"}},
		{side{"Aitor Astíz", "Power To The People  - LEON Shaf Huse (Aitor Astiz Re-Fix)[Free Download]"}, side{"file", "LEON, Shaf Huse - Power To The People (Aitor Astiz Re-Fix)"}},
		{side{"AYO IT'S HOUSE TIME 🔥", "0ut 0f My M1nd (Colin Push Edit)"}, side{"Duke Dumont", "Out Of My Mind (Colin Push Edit)"}},
		{side{"Channel", "Skream - Midnight Request Line (Radio Edit) | FREE DOWNLOAD"}, side{"Skream", "Midnight Request Line"}},
	}
	for _, c := range cases {
		s := Best(key(c.sc), key(c.lib))
		t.Logf("%.2f %s | %s", s, c.sc.title, c.lib.title)
		if s < Sure {
			t.Errorf("%.2f  want same: %q  vs  %q\n  sc=%+v\n  lib=%+v", s, c.sc.title, c.lib.title, key(c.sc), key(c.lib))
		}
	}
}

func TestDifferentTrack(t *testing.T) {
	cases := []struct{ sc, lib side }{
		{side{"u", "Bicep - Glue (Hammer Remix)"}, side{"Bicep", "Glue"}},
		{side{"u", "Bicep - Glue (Hammer Remix)"}, side{"Bicep", "Glue (Ewan McVicar Remix)"}},
		{side{"u", "Fred again.. - Delilah"}, side{"Fred again..", "Jungle"}},
		{side{"u", "Four Tet - Baby"}, side{"Ellie Goulding", "Baby"}},
		{side{"u", "Skrillex - Rumble (VIP)"}, side{"Skrillex", "Rumble"}},
		{side{"Four Tet", "Baby"}, side{"Ellie Goulding", "Baby"}},
		{side{"u", "Bass House | Rumbl3 (HayaT Remix)"}, side{"file", "Rumble (Skrillex VIP)"}},
		{side{"u", "Area 808 - B2B"}, side{"file", "Area 808 - B2B Part 2"}},
	}
	for _, c := range cases {
		s := Best(key(c.sc), key(c.lib))
		t.Logf("%.2f %s | %s", s, c.sc.title, c.lib.title)
		if s >= Sure {
			t.Errorf("%.2f  want different: %q  vs  %q\n  sc=%+v\n  lib=%+v", s, c.sc.title, c.lib.title, key(c.sc), key(c.lib))
		}
	}
}
