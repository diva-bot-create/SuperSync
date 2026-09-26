package rbdb

import (
	"errors"
	"time"
)

// ErrWouldNotQuit means rekordbox is still running even after a force-quit.
var ErrWouldNotQuit = errors.New("rekordbox didn't quit, even when forced; close it yourself")

// Quit closes rekordbox and returns the application to pass to Relaunch. It
// first asks rekordbox to quit normally (as its Quit menu item does), so an
// idle rekordbox can finish writing its library; if it's still open after
// grace (e.g. showing a dialog), it force-quits it. Only call this after the
// user has confirmed.
func Quit(grace time.Duration) (string, error) {
	if !Running() {
		return "", nil
	}
	app := appPath()
	if err := requestQuit(app); err != nil {
		return "", err
	}
	if !waitClosed(grace) {
		forceQuit()
		if !waitClosed(10 * time.Second) {
			return app, ErrWouldNotQuit
		}
	}
	// Let the file system settle before the library is read.
	time.Sleep(2 * time.Second)
	return app, nil
}

func waitClosed(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for Running() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
	return true
}
