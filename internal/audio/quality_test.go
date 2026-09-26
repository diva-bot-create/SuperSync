package audio

import "testing"

func TestUpscaleScoring(t *testing.T) {
	real320 := &Info{Codec: "MP3", Bitrate: 320, Cutoff: 20200}
	fake320 := &Info{Codec: "MP3", Bitrate: 320, Cutoff: 16100}
	real256aac := &Info{Codec: "AAC", Bitrate: 256, Cutoff: -1}
	fakeWAV := &Info{Codec: "WAV", Lossless: true, BitDepth: 16, SampleRate: 44100, Cutoff: 16800}
	realWAV := &Info{Codec: "WAV", Lossless: true, BitDepth: 16, SampleRate: 44100, Cutoff: -1}
	rip128 := &Info{Codec: "MP3", Bitrate: 128, Cutoff: 16100}
	unchecked := &Info{Codec: "MP3", Bitrate: 320}

	if fake320.TrueKbps() != 128 || fake320.Upscaled() == "" || real320.Upscaled() != "" {
		t.Errorf("mp3: fake %d %q, real %q", fake320.TrueKbps(), fake320.Upscaled(), real320.Upscaled())
	}
	if rip128.Upscaled() != "" { // a 128 that cuts off at 16 kHz is honest
		t.Errorf("honest 128 flagged: %q", rip128.Upscaled())
	}
	if fakeWAV.TrueKbps() != 128 || realWAV.Upscaled() != "" || unchecked.Upscaled() != "" {
		t.Errorf("wav: fake %d, real %q, unchecked %q", fakeWAV.TrueKbps(), realWAV.Upscaled(), unchecked.Upscaled())
	}
	// Ranking: real WAV > real 320 = real AAC 256 (counted as equivalent) > fake 320.
	if !(realWAV.Quality() > real320.Quality() && real320.Quality() == real256aac.Quality() && real256aac.Quality() > fake320.Quality()) {
		t.Errorf("ranking: wav %d, 320 %d, aac %d, fake %d", realWAV.Quality(), real320.Quality(), real256aac.Quality(), fake320.Quality())
	}
	if fakeWAV.Quality() != rip128.Quality() || !fakeWAV.ClaimsHigh() || rip128.ClaimsHigh() {
		t.Errorf("fake WAV %d vs rip %d", fakeWAV.Quality(), rip128.Quality())
	}
}
