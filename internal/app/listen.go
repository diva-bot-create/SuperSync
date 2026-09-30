package app

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"supersync/internal/fingerprint"
	"supersync/internal/library"
)

// ListenPending counts files without an audio fingerprint yet.
func (a *App) ListenPending() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Lib == nil {
		return 0
	}
	n := 0
	for _, t := range a.Lib.Tracks {
		if len(t.FP) == 0 && !t.FPFailed {
			n++
		}
	}
	return n
}

var listening atomic.Bool

// Listen fingerprints every file that hasn't been yet, so duplicates can be
// recognised by how they sound. It runs in the background (one pass at a
// time), saving as it goes so a restart picks up where it left off.
func (a *App) Listen(progress library.Progress) error {
	if !listening.CompareAndSwap(false, true) {
		return nil
	}
	defer listening.Store(false)
	a.mu.Lock()
	lib := a.Lib
	var todo []*library.Track
	if lib != nil {
		for _, t := range lib.Tracks {
			if len(t.FP) == 0 && !t.FPFailed {
				todo = append(todo, t)
			}
		}
	}
	a.mu.Unlock()
	if len(todo) == 0 {
		return nil
	}
	type result struct {
		t  *library.Track
		fp fingerprint.FP
	}
	results := make(chan result)
	jobs := make(chan *library.Track)
	var wg sync.WaitGroup
	// Leave a core free: this is background work.
	for w := 0; w < max(1, runtime.NumCPU()-2); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				fp, _ := fingerprint.File(t.Path, t.Duration)
				results <- result{t, fp}
			}
		}()
	}
	go func() {
		for _, t := range todo {
			jobs <- t
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	done := 0
	lastSave := time.Now()
	for r := range results {
		a.mu.Lock()
		if len(r.fp) > 0 {
			r.t.FP = r.fp
		} else {
			r.t.FPFailed = true
		}
		a.mu.Unlock()
		done++
		if progress != nil && (done%10 == 0 || done == len(todo)) {
			progress(done, len(todo))
		}
		if time.Since(lastSave) > 30*time.Second {
			a.mu.Lock()
			lib.Save()
			a.mu.Unlock()
			lastSave = time.Now()
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return lib.Save()
}
