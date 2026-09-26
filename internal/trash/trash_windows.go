package trash

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

var shFileOperation = syscall.NewLazyDLL("shell32.dll").NewProc("SHFileOperationW")

type shFileOpStruct struct {
	Hwnd                 uintptr
	Func                 uint32
	From                 *uint16
	To                   *uint16
	Flags                uint16
	AnyOperationsAborted int32
	NameMappings         uintptr
	ProgressTitle        *uint16
}

const (
	foDelete          = 3
	fofSilent         = 0x0004
	fofNoConfirmation = 0x0010
	fofAllowUndo      = 0x0040 // to the Recycle Bin, not gone
	fofNoErrorUI      = 0x0400
)

func move(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// pFrom is a list ending in two NULs.
	from, err := syscall.UTF16FromString(abs)
	if err != nil {
		return "", err
	}
	from = append(from, 0)
	op := shFileOpStruct{Func: foDelete, From: &from[0], Flags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI}
	r, _, _ := shFileOperation.Call(uintptr(unsafe.Pointer(&op)))
	if r != 0 || op.AnyOperationsAborted != 0 {
		return "", fmt.Errorf("couldn't move it to the Recycle Bin (error %d)", r)
	}
	return "", nil
}
