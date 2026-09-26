// Package sysexec runs helper programs without flashing a console window
// (a windowed Windows app gets a new console for every command it starts).
package sysexec

import "os/exec"

// Command is exec.Command, with no console window on Windows.
func Command(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	hide(c)
	return c
}
