package main

import (
	"os"
	"syscall"
)

// The Windows build has no console of its own (it's a windowed app). When
// it's run from a terminal with a command, borrow that terminal's console so
// the command's output shows up there.
func init() {
	if len(os.Args) < 2 {
		return
	}
	attach := syscall.NewLazyDLL("kernel32.dll").NewProc("AttachConsole")
	if r, _, _ := attach.Call(uintptr(^uint32(0))); r == 0 { // ATTACH_PARENT_PROCESS
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = f, f
	}
}
