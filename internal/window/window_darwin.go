//go:build darwin

package window

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=11.0
#cgo LDFLAGS: -framework Cocoa -framework WebKit -mmacosx-version-min=11.0
#include <stdlib.h>
void ssRun(const char *url, const char *title, int w, int h);
void ssFocus(void);
void ssClose(void);
*/
import "C"

import (
	"runtime"
	"unsafe"
)

// Cocoa must run on the process's main thread, which is where init runs.
func init() { runtime.LockOSThread() }

var onClose func()

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
	onClose = o.OnClose
	u, t := C.CString(o.URL), C.CString(o.Title)
	defer C.free(unsafe.Pointer(u))
	defer C.free(unsafe.Pointer(t))
	C.ssRun(u, t, C.int(o.Width), C.int(o.Height))
	return true
}

// Focus brings the window to the front (another launch of SuperSync).
func Focus() { C.ssFocus() }

// Close quits the app, as ⌘Q does.
func Close() { C.ssClose() }
