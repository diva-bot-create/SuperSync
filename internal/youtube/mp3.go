package youtube

import (
	"encoding/binary"
	"errors"
	"fmt"

	aacpcm "github.com/tphakala/go-aac/pcm"
	"github.com/tphakala/go-mp3"

	"supersync/internal/soundcloud"
)

// MP3Bitrate is what YouTube's ~128 kbps AAC is re-encoded at. Converting
// lossy to lossy costs quality at the same bitrate, so this leaves headroom.
const MP3Bitrate = 192000

var ascRates = [...]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}

// mp3 decodes t's AAC and encodes it as a CBR mp3 with an ID3 tag, all in Go.
func (t *track) mp3(title, artist string) ([]byte, error) {
	if len(t.asc) < 2 {
		return nil, errors.New("no AAC decoder configuration in the file")
	}
	// AudioSpecificConfig: 5 bits object type, 4 bits sample-rate index.
	rateIdx := int(t.asc[0]&0x07)<<1 | int(t.asc[1]>>7)
	if rateIdx >= len(ascRates) {
		return nil, fmt.Errorf("unsupported AAC sample rate index %d", rateIdx)
	}
	rate := ascRates[rateIdx]
	if rate != 44100 && rate != 48000 && rate != 32000 {
		return nil, fmt.Errorf("mp3 can't hold %d Hz audio", rate)
	}
	dec, err := aacpcm.NewRawDecoder(t.asc)
	if err != nil {
		return nil, fmt.Errorf("AAC decoder: %w", err)
	}

	var enc *mp3.Encoder
	var planes [][]float32 // pending samples, one slice per channel
	out := soundcloud.ID3v2(title, artist)
	out = append(out, make([]byte, 0, len(t.data)*MP3Bitrate/128000)...)

	encode := func(final bool) error {
		for len(planes[0]) >= mp3.FrameSize || (final && len(planes[0]) > 0) {
			n := min(mp3.FrameSize, len(planes[0]))
			frame := make([][]float32, len(planes))
			for c := range planes {
				frame[c] = planes[c][:n]
			}
			if out, err = enc.EncodeFrame(out, frame); err != nil {
				return err
			}
			for c := range planes {
				// Keep the leftover at the front so the buffer stays one frame's worth.
				planes[c] = planes[c][:copy(planes[c], planes[c][n:])]
			}
		}
		return nil
	}

	var pcm []byte
	off := 0
	for _, size := range t.sizes {
		au := t.data[off : off+int(size)]
		off += int(size)
		var samples int
		pcm, samples, err = dec.DecodeFrame(pcm[:0], au)
		if err != nil {
			return nil, fmt.Errorf("AAC decode: %w", err)
		}
		if enc == nil {
			ch := min(dec.Channels(), 2)
			if ch < 1 {
				return nil, errors.New("AAC stream has no channels")
			}
			if enc, err = mp3.NewEncoder(mp3.EncoderConfig{
				SampleRate: rate, Channels: ch, Bitrate: MP3Bitrate,
				// About 4x faster than RateControlExact, at a small quality cost on
				// some frames: invisible next to re-encoding lossy audio at all.
				RateControl: mp3.RateControlFast,
			}); err != nil {
				return nil, err
			}
			planes = make([][]float32, ch)
		}
		// Interleaved S16 LE -> planar float32; extra channels (5.1) are dropped.
		stride := dec.Channels()
		for i := 0; i < samples; i++ {
			for c := range planes {
				v := int16(binary.LittleEndian.Uint16(pcm[(i*stride+c)*2:]))
				planes[c] = append(planes[c], float32(v)/32768)
			}
		}
		if err := encode(false); err != nil {
			return nil, err
		}
	}
	if enc == nil {
		return nil, errors.New("no audio")
	}
	if err := encode(true); err != nil {
		return nil, err
	}
	if out, err = enc.EncodeFrame(out, nil); err != nil { // drain the lookahead and bit reservoir
		return nil, err
	}
	JointStereo(out)
	return out, nil
}
