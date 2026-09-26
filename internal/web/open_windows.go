package web

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// shellOpen opens a link in the default browser through the Windows shell
// (no helper program, so no console window).
func shellOpen(u string) {
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(u)
	windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// revealWindows opens Explorer with the file selected. Explorer needs the
// exact form /select,"C:\path\file" (Go's usual argument quoting breaks it,
// and then Explorer just opens a default folder).
func revealWindows(p string) error {
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + p + `"`}
	return cmd.Start()
}
