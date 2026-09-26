package app

import (
	"io/fs"
	"log"
	"path/filepath"
	"strings"

	"supersync/internal/rekordbox"
	"supersync/internal/youtube"
)

// fixStereo repairs mp3s that older versions made from YouTube audio (and
// "Use another link" YouTube downloads): their channel mode switched between
// stereo and joint stereo mid-file, which rekordbox can refuse to play. Only
// header bits change and the audio decodes the same. Files with cue points
// are left alone: rekordbox plays those, and their cues stay where they are.
// It runs once; files that can't be written yet (open in rekordbox) are
// tried again next start.
func (a *App) fixStereo() {
	if a.Cfg.StereoFixed || a.Cfg.MusicDir == "" {
		return
	}
	cued := map[string]bool{}
	if a.Src != nil {
		for _, t := range a.Src.Tracks() {
			if t.Cues > 0 {
				cued[rekordbox.NormPath(t.Path)] = true
			}
		}
	}
	done, failed := 0, 0
	for _, sub := range []string{"YouTube", "SoundCloud"} {
		filepath.WalkDir(filepath.Join(a.Cfg.MusicDir, sub), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".mp3") || cued[rekordbox.NormPath(p)] {
				return nil
			}
			switch ok, err := youtube.FixStereo(p); {
			case err != nil:
				failed++
			case ok:
				done++
			}
			return nil
		})
	}
	if done > 0 {
		log.Printf("repaired %d YouTube mp3s so rekordbox can play them", done)
	}
	if failed == 0 {
		a.mu.Lock()
		a.Cfg.StereoFixed = true
		a.Cfg.Save()
		a.mu.Unlock()
	}
}
