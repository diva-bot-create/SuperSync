//go:build windows

package window

import (
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

// The web view belongs to the thread that made it: keep it on the main one.
func init() { runtime.LockOSThread() }

var (
	mu          sync.Mutex
	cur         webview2.WebView
	keepRunning atomic.Bool
	quitting    atomic.Bool
	origProc    uintptr
	opts        Options
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	shell32             = syscall.NewLazyDLL("shell32.dll")
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	showWindow          = user32.NewProc("ShowWindow")
	setForegroundWindow = user32.NewProc("SetForegroundWindow")
	setWindowLongPtr    = user32.NewProc("SetWindowLongPtrW")
	callWindowProc      = user32.NewProc("CallWindowProcW")
	loadImage           = user32.NewProc("LoadImageW")
	createPopupMenu     = user32.NewProc("CreatePopupMenu")
	appendMenu          = user32.NewProc("AppendMenuW")
	trackPopupMenu      = user32.NewProc("TrackPopupMenu")
	destroyMenu         = user32.NewProc("DestroyMenu")
	getCursorPos        = user32.NewProc("GetCursorPos")
	postMessage         = user32.NewProc("PostMessageW")
	registerWindowMsg   = user32.NewProc("RegisterWindowMessageW")
	shellNotifyIcon     = shell32.NewProc("Shell_NotifyIconW")
	getModuleHandle     = kernel32.NewProc("GetModuleHandleW")
	keybdEvent          = user32.NewProc("keybd_event")
	setWindowPos        = user32.NewProc("SetWindowPos")
	isZoomed            = user32.NewProc("IsZoomed")
	releaseCapture      = user32.NewProc("ReleaseCapture")
	getSystemMetrics    = user32.NewProc("GetSystemMetrics")
	moveMemory          = kernel32.NewProc("RtlMoveMemory")
	isWindowVisible     = user32.NewProc("IsWindowVisible")
	isIconic            = user32.NewProc("IsIconic")
	getForegroundWindow = user32.NewProc("GetForegroundWindow")
)

const (
	wmClose       = 0x0010
	wmDestroy     = 0x0002
	wmNull        = 0x0000
	wmCommand     = 0x0111
	wmLButtonUp   = 0x0202
	wmLButtonDbl  = 0x0203
	wmRButtonUp   = 0x0205
	wmTray        = 0x8000 + 1 // WM_APP + 1
	wmNCCalcSize  = 0x0083
	wmNCLButton   = 0x00A1
	htCaption     = 2
	swMinimize    = 6
	swMaximize    = 3
	swHide        = 0
	swRestore     = 9
	swShow        = 5
	gwlpWndProc   = ^uintptr(3) // -4
	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nifInfo       = 0x10
	niifInfo      = 1
	ninBalloonClk = 0x0405 // NIN_BALLOONUSERCLICK
	nifMessage    = 1
	nifIcon       = 2
	nifTip        = 4
	mfString      = 0
	mfSeparator   = 0x800
	tpmReturnCmd  = 0x100
	tpmRightBtn   = 0x2
	cmdOpen       = 1
	cmdSync       = 2
	cmdQuit       = 3
	imageIcon     = 1
	lrDefaultSize = 0x40
	lrShared      = 0x8000
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

var tray notifyIconData

func Supported() bool { return true }

// SetKeepRunning chooses what closing the window does: hide it to the
// notification area and keep running (true), or quit.
func SetKeepRunning(keep bool) { keepRunning.Store(keep) }

// Run shows the window and returns when SuperSync quits (after OnClose). It
// reports false, without showing anything, if Edge WebView2 isn't installed.
func Run(o Options) bool {
	opts = o
	keepRunning.Store(o.KeepRunning)
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  o.DataDir,
		WindowOptions: webview2.WindowOptions{
			Title: o.Title, Width: uint(o.Width), Height: uint(o.Height), IconId: 1, Center: true,
		},
	})
	if w == nil {
		return false
	}
	mu.Lock()
	cur = w
	mu.Unlock()
	hwnd := uintptr(w.Window())
	if o.Hidden {
		showWindow.Call(hwnd, swHide)
	}
	// Watch the window's messages: the close button hides it, and the tray
	// icon's clicks arrive here.
	origProc, _, _ = setWindowLongPtr.Call(hwnd, gwlpWndProc, syscall.NewCallback(wndProc))
	// Drop the system title bar (the page's header replaces it) but keep the
	// frame, so the window still snaps, shadows and resizes from its sides.
	setWindowPos.Call(hwnd, 0, 0, 0, 0, 0, 0x0020|0x0002|0x0001|0x0004) // FRAMECHANGED|NOMOVE|NOSIZE|NOZORDER
	w.Bind("ssWindow", func(action string) { windowAction(hwnd, action) })
	addTray(hwnd, o.Title)
	w.SetSize(720, 520, webview2.HintMin)
	w.Navigate(o.URL)
	w.Run()
	removeTray()
	mu.Lock()
	cur = nil
	mu.Unlock()
	if o.OnClose != nil {
		o.OnClose()
	}
	w.Destroy()
	return true
}

type rect struct{ Left, Top, Right, Bottom int32 }

type ncCalcSizeParams struct {
	Rgrc  [3]rect
	Lppos uintptr
}

func wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch msg {
	case wmNCCalcSize:
		if wp != 0 {
			// Let Windows size the frame, then give the caption's space to
			// the page: its header is the title bar.
			var p ncCalcSizeParams
			size := unsafe.Sizeof(p)
			moveMemory.Call(uintptr(unsafe.Pointer(&p)), lp, size)
			top := p.Rgrc[0].Top
			callWindowProc.Call(origProc, hwnd, msg, wp, lp)
			moveMemory.Call(uintptr(unsafe.Pointer(&p)), lp, size)
			p.Rgrc[0].Top = top
			if z, _, _ := isZoomed.Call(hwnd); z != 0 {
				// Maximized windows hang over the screen edge by the frame.
				f, _, _ := getSystemMetrics.Call(33)   // SM_CYFRAME
				pad, _, _ := getSystemMetrics.Call(92) // SM_CXPADDEDBORDER
				p.Rgrc[0].Top += int32(f + pad)
			}
			moveMemory.Call(lp, uintptr(unsafe.Pointer(&p)), size)
			return 0
		}
	case wmClose:
		if keepRunning.Load() && !quitting.Load() {
			showWindow.Call(hwnd, swHide)
			return 0
		}
	case wmTray:
		switch lp & 0xffff {
		case wmLButtonUp, wmLButtonDbl, ninBalloonClk:
			show(hwnd)
		case wmRButtonUp:
			trayMenu(hwnd)
		}
		return 0
	}
	if msg == taskbarCreated() && msg != 0 {
		addTray(hwnd, opts.Title) // Explorer restarted: put the icon back
	}
	r, _, _ := callWindowProc.Call(origProc, hwnd, msg, wp, lp)
	return r
}

var tbc uintptr

func taskbarCreated() uintptr {
	if tbc == 0 {
		p, _ := syscall.UTF16PtrFromString("TaskbarCreated")
		tbc, _, _ = registerWindowMsg.Call(uintptr(unsafe.Pointer(p)))
	}
	return tbc
}

func show(hwnd uintptr) {
	showWindow.Call(hwnd, swRestore)
	showWindow.Call(hwnd, swShow)
	setForegroundWindow.Call(hwnd)
}

func addTray(hwnd uintptr, title string) {
	mod, _, _ := getModuleHandle.Call(0)
	icon, _, _ := loadImage.Call(mod, 1, imageIcon, 0, 0, lrDefaultSize|lrShared)
	tray = notifyIconData{HWnd: hwnd, UID: 1, UFlags: nifMessage | nifIcon | nifTip, UCallbackMessage: wmTray, HIcon: icon}
	tray.CbSize = uint32(unsafe.Sizeof(tray))
	tip, _ := syscall.UTF16FromString(title)
	copy(tray.SzTip[:len(tray.SzTip)-1], tip)
	shellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&tray)))
}

func removeTray() {
	if tray.HWnd != 0 {
		shellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&tray)))
	}
}

func trayMenu(hwnd uintptr) {
	m, _, _ := createPopupMenu.Call()
	add := func(id uintptr, label string) {
		p, _ := syscall.UTF16PtrFromString(label)
		appendMenu.Call(m, mfString, id, uintptr(unsafe.Pointer(p)))
	}
	add(cmdOpen, "Open "+opts.Title)
	add(cmdSync, "Sync all playlists now")
	appendMenu.Call(m, mfSeparator, 0, 0)
	add(cmdQuit, "Quit "+opts.Title)
	var pt struct{ X, Y int32 }
	getCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	setForegroundWindow.Call(hwnd) // so the menu closes when you click elsewhere
	cmd, _, _ := trackPopupMenu.Call(m, tpmReturnCmd|tpmRightBtn, uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	postMessage.Call(hwnd, wmNull, 0, 0)
	destroyMenu.Call(m)
	switch cmd {
	case cmdOpen:
		show(hwnd)
	case cmdSync:
		if opts.OnSyncAll != nil {
			go opts.OnSyncAll()
		}
	case cmdQuit:
		quit()
	}
}

func with(f func(w webview2.WebView)) {
	mu.Lock()
	w := cur
	mu.Unlock()
	if w != nil {
		w.Dispatch(func() { f(w) })
	}
}

// Focus brings the window back (another launch of SuperSync, or the tray).
func Focus() { with(func(w webview2.WebView) { show(uintptr(w.Window())) }) }

// windowAction does what the page's title bar asks: drag the window,
// minimize, maximize/restore (also a double-click), or close.
func windowAction(hwnd uintptr, action string) {
	switch action {
	case "drag":
		releaseCapture.Call()
		postMessage.Call(hwnd, wmNCLButton, htCaption, 0)
	case "min":
		showWindow.Call(hwnd, swMinimize)
	case "max", "zoom":
		if z, _, _ := isZoomed.Call(hwnd); z != 0 {
			showWindow.Call(hwnd, swRestore)
		} else {
			showWindow.Call(hwnd, swMaximize)
		}
	case "close":
		postMessage.Call(hwnd, wmClose, 0, 0)
	}
}

// Edit runs an edit command (paste, cut, copy, selectAll, undo, redo) in the
// focused text field by pressing its shortcut. Pages can't read the
// clipboard themselves, so Paste in the right-click menu goes through here.
func Edit(action string) {
	key := map[string]uintptr{"paste": 'V', "cut": 'X', "copy": 'C', "selectAll": 'A', "undo": 'Z', "redo": 'Y'}[action]
	if key == 0 {
		return
	}
	with(func(webview2.WebView) {
		const vkControl, keyUp = 0x11, 0x2
		keybdEvent.Call(vkControl, 0, 0, 0)
		keybdEvent.Call(key, 0, 0, 0)
		keybdEvent.Call(key, 0, keyUp, 0)
		keybdEvent.Call(vkControl, 0, keyUp, 0)
	})
}

// Active reports whether the window is on screen and in front.
func Active() bool {
	mu.Lock()
	w := cur
	mu.Unlock()
	if w == nil {
		return false
	}
	h := uintptr(w.Window())
	vis, _, _ := isWindowVisible.Call(h)
	min, _, _ := isIconic.Call(h)
	fg, _, _ := getForegroundWindow.Call()
	return vis != 0 && min == 0 && fg == h
}

// Notify shows a notification from the tray icon (clicking it opens the
// window).
func Notify(title, body string) {
	with(func(webview2.WebView) {
		if tray.HWnd == 0 {
			return
		}
		n := tray
		n.UFlags = nifInfo
		n.DwInfoFlags = niifInfo
		t, _ := syscall.UTF16FromString(title)
		b, _ := syscall.UTF16FromString(body)
		copy(n.SzInfoTitle[:len(n.SzInfoTitle)-1], t)
		copy(n.SzInfo[:len(n.SzInfo)-1], b)
		shellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&n)))
	})
}

// Close quits SuperSync.
func Close() { with(func(webview2.WebView) { quit() }) }

func quit() {
	quitting.Store(true)
	with(func(w webview2.WebView) { w.Terminate() })
}
