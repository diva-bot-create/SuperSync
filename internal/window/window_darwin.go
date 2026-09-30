//go:build darwin

package window

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=11.0
#cgo LDFLAGS: -framework Cocoa -framework WebKit -framework UserNotifications -mmacosx-version-min=11.0
#include <stdlib.h>
void ssRun(const char *url, const char *title, int w, int h, int hidden);
void ssSetKeepRunning(int keep);
void ssFocus(void);
void ssClose(void);
void ssEdit(const char *sel);
int ssActive(void);
void ssNotify(const char *title, const char *body);
*/
import "C"

import (
	"runtime"
	"unsafe"
)

// Cocoa must run on the process's main thread, which is where init runs.
func init() { runtime.LockOSThread() }

var onClose func()
var onSyncAll func()

//export ssMenuAction
func ssMenuAction(action C.int) {
	if action == 1 && onSyncAll != nil {
		go onSyncAll()
	}
}

// SetKeepRunning chooses what closing the window does: hide it and keep
// running in the menu bar (true), or quit.
func SetKeepRunning(keep bool) {
	k := 0
	if keep {
		k = 1
	}
	C.ssSetKeepRunning(C.int(k))
}

//export ssWillTerminate
func ssWillTerminate() {
	if onClose != nil {
		onClose()
	}
}

func Supported() bool { return true }

// Run shows the window and runs the app until it quits. It must be called
// from the main goroutine, and it doesn't return: quitting exits the process
// after OnClose has run.
func Run(o Options) bool {
	onClose, onSyncAll = o.OnClose, o.OnSyncAll
	SetKeepRunning(o.KeepRunning)
	u, t := C.CString(o.URL), C.CString(o.Title)
	defer C.free(unsafe.Pointer(u))
	defer C.free(unsafe.Pointer(t))
	hidden := 0
	if o.Hidden {
		hidden = 1
	}
	C.ssRun(u, t, C.int(o.Width), C.int(o.Height), C.int(hidden))
	return true
}

// Focus brings the window to the front (another launch of SuperSync).
func Focus() { C.ssFocus() }

// Edit runs an edit command (paste, cut, copy, selectAll, undo, redo) in the
// focused text field, as the Edit menu does. Pages can't read the clipboard
// themselves, so Paste in SuperSync's right-click menu goes through here.
func Edit(action string) {
	sel := map[string]string{"paste": "paste:", "cut": "cut:", "copy": "copy:", "selectAll": "selectAll:", "undo": "undo:", "redo": "redo:"}[action]
	if sel == "" {
		return
	}
	c := C.CString(sel)
	defer C.free(unsafe.Pointer(c))
	C.ssEdit(c)
}

// Active reports whether the window is on screen and in front.
func Active() bool { return C.ssActive() != 0 }

// Notify shows a system notification (clicking it opens the window).
func Notify(title, body string) {
	t, b := C.CString(title), C.CString(body)
	defer C.free(unsafe.Pointer(t))
	defer C.free(unsafe.Pointer(b))
	C.ssNotify(t, b)
}

// Close quits the app, as ⌘Q does.
func Close() { C.ssClose() }
