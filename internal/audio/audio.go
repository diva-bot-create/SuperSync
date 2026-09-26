// Package audio reads tags and stream properties (duration, bitrate, codec)
// from the audio formats DJs commonly use, without any external tools.
package audio

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dhowden/tag"
)

// Info describes one audio file.
type Info struct {
	Path       string  `json:"path"`
	Size       int64   `json:"size"`
	ModTime    int64   `json:"mtime"`
	Format     string  `json:"format"`   // container: mp3, flac, aiff, wav, m4a, ogg
	Codec      string  `json:"codec"`    // MP3, FLAC, PCM, AAC, ALAC, Vorbis, Opus
	Lossless   bool    `json:"lossless"` //
	Bitrate    int     `json:"kbps"`     // average kbps
	SampleRate int     `json:"hz"`       //
	BitDepth   int     `json:"bits,omitempty"`
	Duration   float64 `json:"secs"`
	Artist     string  `json:"artist,omitempty"`
	Title      string  `json:"title,omitempty"`
	Album      string  `json:"album,omitempty"`
	Comment    string  `json:"comment,omitempty"`
	Err        string  `json:"err,omitempty"`
	// Cutoff is where the spectrum drops off a cliff (see package spectrum):
	// 0 not checked yet, -1 no cutoff, -2 couldn't check, otherwise Hz.
	Cutoff int `json:"cutoff,omitempty"`
}

// ClaimsHigh reports whether the file presents itself as high quality
// (lossless, or lossy at 224 kbps MP3-equivalent or more), i.e. whether a
// spectrum check could reveal it as upscaled.
func (in *Info) ClaimsHigh() bool {
	return in.Lossless || in.nominalQuality() >= 22400
}

// TrueKbps estimates the bitrate a file was really encoded at from its
// cutoff, or 0 when the cutoff doesn't indicate a lower quality than claimed.
// Typical encoder lowpasses: 128 kbps ~16-17 kHz, 160 ~17.5, 192 ~18.6,
// 256 ~19.7, 320 ~20.5.
func (in *Info) TrueKbps() int {
	c := in.Cutoff
	var est int
	switch {
	case c <= 0 || c >= 19000:
		return 0
	case c >= 18000:
		est = 192
	case c >= 17250:
		est = 160
	case c >= 15500:
		est = 128
	default:
		est = 96
	}
	if !in.Lossless && est*100 >= in.nominalQuality() {
		return 0 // cutoff is normal for what the file says it is
	}
	return est
}

// Upscaled describes a file that's really lower quality than it claims,
// e.g. "really ~128 kbps (cuts off at 16.1 kHz)", or "".
func (in *Info) Upscaled() string {
	k := in.TrueKbps()
	if k == 0 {
		return ""
	}
	return "really ~" + strconv.Itoa(k) + " kbps (cuts off at " + strconv.FormatFloat(float64(in.Cutoff)/1000, 'f', 1, 64) + " kHz)"
}

// Extensions supported by Read.
var Extensions = map[string]string{
	".mp3": "mp3", ".flac": "flac", ".aif": "aiff", ".aiff": "aiff", ".aifc": "aiff",
	".wav": "wav", ".wave": "wav", ".m4a": "m4a", ".mp4": "m4a", ".aac": "m4a", ".alac": "m4a",
	".ogg": "ogg", ".oga": "ogg", ".opus": "ogg",
}

// IsAudio reports whether path has a supported extension.
func IsAudio(path string) bool {
	_, ok := Extensions[strings.ToLower(filepath.Ext(path))]
	return ok
}

var errFormat = errors.New("unrecognized audio data")

// Read parses the file at path. Tag or stream errors are recorded in Info.Err
// rather than returned, so a single damaged file never aborts a scan; only a
// failure to open or stat the file returns an error.
func Read(path string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	in := &Info{
		Path:    path,
		Size:    st.Size(),
		ModTime: st.ModTime().Unix(),
		Format:  Extensions[strings.ToLower(filepath.Ext(path))],
	}

	var streamErr error
	switch in.Format {
	case "mp3":
		streamErr = readMP3(f, in)
	case "flac":
		streamErr = readFLAC(f, in)
	case "aiff":
		streamErr = readAIFF(f, in)
	case "wav":
		streamErr = readWAV(f, in)
	case "m4a":
		streamErr = readMP4(f, in)
	case "ogg":
		streamErr = readOgg(f, in)
	default:
		streamErr = errFormat
	}

	// AIFF and WAV tags are read by their own parsers (embedded ID3 chunk).
	if in.Format != "aiff" && in.Format != "wav" {
		if _, err := f.Seek(0, io.SeekStart); err == nil {
			if m, err := tag.ReadFrom(f); err == nil {
				applyTags(in, m)
			}
		}
	}

	if in.Bitrate == 0 && in.Duration > 0 {
		in.Bitrate = int(float64(in.Size) * 8 / in.Duration / 1000)
	}
	if streamErr != nil {
		in.Err = streamErr.Error()
	}
	return in, nil
}

func applyTags(in *Info, m tag.Metadata) {
	in.Artist = clean(m.Artist())
	if in.Artist == "" {
		in.Artist = clean(m.AlbumArtist())
	}
	in.Title = clean(m.Title())
	in.Album = clean(m.Album())
	in.Comment = clean(m.Comment())
}

func clean(s string) string {
	return strings.TrimSpace(strings.Trim(s, "\x00"))
}

// Quality returns a sortable score: lossless beats any lossy file, then higher
// bitrate, then higher sample rate / bit depth. Files revealed as upscaled
// score as what they really are.
func (in *Info) Quality() int {
	if k := in.TrueKbps(); k > 0 {
		return k * 100
	}
	return in.nominalQuality()
}

func (in *Info) nominalQuality() int {
	q := 0
	if in.Lossless {
		q += 100000
		q += in.BitDepth*100 + in.SampleRate/1000
	} else {
		br := in.Bitrate
		// Opus and AAC are more efficient than MP3 at the same bitrate.
		switch in.Codec {
		case "Opus":
			br = br * 3 / 2
		case "AAC":
			br = br * 4 / 3
		}
		if br > 320 {
			br = 320
		}
		q += br * 100
	}
	return q
}

// QualityLabel is a short human-readable description, e.g. "FLAC 16/44.1" or "MP3 128".
func (in *Info) QualityLabel() string {
	if in.Codec == "" {
		return strings.ToUpper(in.Format)
	}
	if in.Lossless {
		s := in.Codec
		if in.BitDepth > 0 && in.SampleRate > 0 {
			s += " " + strconv.Itoa(in.BitDepth) + "/" + khz(in.SampleRate)
		}
		return s
	}
	return in.Codec + " " + strconv.Itoa(in.Bitrate)
}

func khz(hz int) string {
	whole := hz / 1000
	frac := (hz % 1000) / 100
	if frac == 0 {
		return strconv.Itoa(whole)
	}
	return strconv.Itoa(whole) + "." + strconv.Itoa(frac)
}
