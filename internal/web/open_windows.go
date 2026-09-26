package web

import "golang.org/x/sys/windows"

// shellOpen opens a link in the default browser through the Windows shell
// (no helper program, so no console window).
func shellOpen(u string) {
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(u)
	windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
