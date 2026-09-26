//go:build windows

package window

import (
	"runtime"
	"sync"
	"syscall"

	webview2 "github.com/jchv/go-webview2"
)

// The web view belongs to the thread that made it: keep it on the main one.
func init() { runtime.LockOSThread() }

var (
	mu  sync.Mutex
	cur webview2.WebView
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	showWindow          = user32.NewProc("ShowWindow")
	setForegroundWindow = user32.NewProc("SetForegroundWindow")
)

func Supported() bool { return true }

// Run shows the window and returns when it's closed (after OnClose). It
// reports false, without showing anything, if Edge WebView2 isn't installed.
func Run(o Options) bool {
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
	w.SetSize(720, 520, webview2.HintMin)
	w.Navigate(o.URL)
	w.Run()
	mu.Lock()
	cur = nil
	mu.Unlock()
	if o.OnClose != nil {
		o.OnClose()
	}
	w.Destroy()
	return true
}

func with(f func(w webview2.WebView)) {
	mu.Lock()
	w := cur
	mu.Unlock()
	if w != nil {
		w.Dispatch(func() { f(w) })
	}
}

// Focus brings the window to the front (another launch of SuperSync).
func Focus() {
	with(func(w webview2.WebView) {
		h := uintptr(w.Window())
		showWindow.Call(h, 9) // SW_RESTORE
		setForegroundWindow.Call(h)
	})
}

// Close closes the window, which quits SuperSync.
func Close() { with(func(w webview2.WebView) { w.Terminate() }) }
