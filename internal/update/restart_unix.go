//go:build !windows

package update

import (
	"os"
	"syscall"
)

// Restart replaces this process with the (newly installed) executable. It
// keeps the same process and terminal window, so closing that window still
// quits SuperSync. env is added to the environment.
func Restart(env ...string) error {
	exe, err := Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, append(os.Environ(), env...))
}
