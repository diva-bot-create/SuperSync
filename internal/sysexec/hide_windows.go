package sysexec

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// Hide keeps a command from opening a console window.
func Hide(c *exec.Cmd) *exec.Cmd { hide(c); return c }

func hide(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
