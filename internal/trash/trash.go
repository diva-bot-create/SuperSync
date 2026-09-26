// Package trash moves files to the system's Trash (Recycle Bin on Windows),
// so a deleted file can still be recovered.
package trash

import "errors"

// ErrUnsupported means this system has no Trash SuperSync can use.
var ErrUnsupported = errors.New("moving to the Trash isn't supported on this system")

// Move sends path to the Trash. It returns where the file ended up when the
// system says (macOS), or "" (Windows' Recycle Bin doesn't).
func Move(path string) (string, error) { return move(path) }
