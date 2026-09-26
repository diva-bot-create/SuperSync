package soundcloud

import (
	"reflect"
	"testing"
)

func TestExtractLinks(t *testing.T) {
	type want struct {
		url, kind, label, from string
		free                   bool
	}
	cases := []struct {
		desc, buyURL, buyTitle string
		want                   []want
	}{
		{ // buy button is a download gate
			"", "https://hypeddit.com/track/1tajuy", "Free Download",
			[]want{{"https://hypeddit.com/track/1tajuy", "download", "Hypeddit", "buy button", false}},
		},
		{ // same gate in button and description (with tracking) is listed once; label site and email ignored
			"✅ Free download  👉 https://hypeddit.com/track/86jbur?utm_source=sc\n\n🚀 Stay in orbit:\nwww.BaikonurRecordings.com\n\nmastering@baikonurrecordings.com",
			"https://hypeddit.com/track/86jbur?utm_source=sc", "✅ Free download",
			[]want{{"https://hypeddit.com/track/86jbur", "download", "Hypeddit", "buy button", false}},
		},
		{ // promo form and socials are noise; bare-domain Dropbox and a store are kept
			"Promotion Submissions: https://docs.google.com/forms/d/e/abc/viewform\nIG: https://instagram.com/someone\nWAV here: dropbox.com/s/xyz/track.wav?dl=0\nBuy: https://www.beatport.com/track/x/123.",
			"", "",
			[]want{
				{"https://dropbox.com/s/xyz/track.wav?dl=0", "download", "Dropbox", "description", false},
				{"https://www.beatport.com/track/x/123", "buy", "Beatport", "description", false},
			},
		},
		{ // Bandcamp name-your-price counts as free; unknown site behind "Buy" is a store
			"Name your price on bandcamp: https://artist.bandcamp.com/track/tune",
			"https://label.example.com/shop", "Buy",
			[]want{
				{"https://artist.bandcamp.com/track/tune", "buy", "Bandcamp", "description", true},
				{"https://label.example.com/shop", "buy", "label.example.com", "buy button", false},
			},
		},
		{ // store artist/label pages and a bare Bandcamp profile are dropped
			"https://www.traxsource.com/artist/86477/italobros\nhttps://classic.beatport.com/artist/italobros/245948\nhttps://miloch.bandcamp.com\nhttps://miloch.bandcamp.com/track/popular-edit",
			"", "",
			[]want{{"https://miloch.bandcamp.com/track/popular-edit", "buy", "Bandcamp", "description", false}},
		},
		{ // a download site's promo/sign-up pages are dropped, its track pages kept
			"Promote your music: https://hypeddit.com/hype-my-music.php\nSubscribe https://dpbr.me/subscribe\nhttps://dpbr.me/spotify\nAlso https://hypeddit.com/track/5nv3ke",
			"https://hypeddit.com/hotcityla", "Free DL",
			[]want{
				{"https://hypeddit.com/hotcityla", "download", "Hypeddit", "buy button", false},
				{"https://hypeddit.com/track/5nv3ke", "download", "Hypeddit", "description", false},
			},
		},
		{ // unknown host is only kept when the line talks about a free download
			"free dl: https://smarturl.example/abc\nmy site https://example.org",
			"", "",
			[]want{{"https://smarturl.example/abc", "download", "smarturl.example", "description", false}},
		},
	}
	for i, c := range cases {
		var got []want
		for _, l := range ExtractLinks(c.desc, c.buyURL, c.buyTitle) {
			got = append(got, want{l.URL, string(l.Kind), l.Label, l.From, l.Free})
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("case %d:\n got %+v\nwant %+v", i, got, c.want)
		}
	}
}
