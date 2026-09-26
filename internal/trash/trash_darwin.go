package trash

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=11.0
#cgo LDFLAGS: -framework Foundation -mmacosx-version-min=11.0
#import <Foundation/Foundation.h>
#include <stdlib.h>

// Returns the file's new path in the Trash (malloc'd), or NULL with *err set.
static char *ssTrash(const char *path, char **err) {
  @autoreleasepool {
    NSURL *u = [NSURL fileURLWithPath:[NSString stringWithUTF8String:path]];
    NSURL *out = nil;
    NSError *e = nil;
    if (![[NSFileManager defaultManager] trashItemAtURL:u resultingItemURL:&out error:&e]) {
      *err = strdup(e ? e.localizedDescription.UTF8String : "couldn't move it to the Trash");
      return NULL;
    }
    return strdup(out ? out.path.UTF8String : "");
  }
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

func move(path string) (string, error) {
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	var e *C.char
	r := C.ssTrash(p, &e)
	if r == nil {
		defer C.free(unsafe.Pointer(e))
		return "", errors.New(C.GoString(e))
	}
	defer C.free(unsafe.Pointer(r))
	return C.GoString(r), nil
}
