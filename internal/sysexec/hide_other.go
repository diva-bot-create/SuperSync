//go:build !windows

package sysexec

import "os/exec"

// Hide keeps a command from opening a console window (Windows only).
func Hide(c *exec.Cmd) *exec.Cmd { return c }

func hide(*exec.Cmd) {}
