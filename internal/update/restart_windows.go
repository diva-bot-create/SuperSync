//go:build windows

package update

import (
	"os"
	"os/exec"
)

// Restart starts the (newly installed) executable in this console window and
// exits. The window stays open while the new process is attached to it.
func Restart(env ...string) error {
	exe, err := Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
