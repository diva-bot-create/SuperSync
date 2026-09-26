//go:build windows

package rbdb

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows everything here uses the system directly: starting helper
// programs (tasklist, taskkill, PowerShell) from a windowed app flashes a
// console window each time, and looks suspicious to antivirus heuristics.

func rekordboxPIDs() []uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var pids []uint32
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), "rekordbox.exe") {
			pids = append(pids, e.ProcessID)
		}
	}
	return pids
}

func running() bool { return len(rekordboxPIDs()) > 0 }

// appPath is the running rekordbox.exe's full path.
func appPath() string {
	for _, pid := range rekordboxPIDs() {
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			continue
		}
		buf := make([]uint16, windows.MAX_LONG_PATH)
		n := uint32(len(buf))
		err = windows.QueryFullProcessImageName(h, 0, &buf[0], &n)
		windows.CloseHandle(h)
		if err == nil {
			return windows.UTF16ToString(buf[:n])
		}
	}
	return ""
}

var (
	user32      = windows.NewLazySystemDLL("user32.dll")
	postMessage = user32.NewProc("PostMessageW")
)

// requestQuit asks rekordbox to close the way clicking its close button does.
func requestQuit(string) error {
	pids := map[uint32]bool{}
	for _, p := range rekordboxPIDs() {
		pids[p] = true
	}
	if len(pids) == 0 {
		return nil
	}
	cb := syscall.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		var pid uint32
		windows.GetWindowThreadProcessId(hwnd, &pid)
		if pids[pid] && windows.IsWindowVisible(hwnd) {
			postMessage.Call(uintptr(hwnd), 0x0010, 0, 0) // WM_CLOSE
		}
		return 1 // keep going
	})
	windows.EnumWindows(cb, nil)
	return nil
}

func forceQuit() {
	for _, pid := range rekordboxPIDs() {
		if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid); err == nil {
			windows.TerminateProcess(h, 1)
			windows.CloseHandle(h)
		}
	}
}

// Relaunch opens rekordbox again.
func Relaunch(app string) error {
	if app == "" {
		return errors.New("don't know where rekordbox is installed; open it yourself")
	}
	cmd := exec.Command(app)
	cmd.Dir = filepath.Dir(app)
	return cmd.Start()
}
