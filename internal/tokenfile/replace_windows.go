//go:build windows

package tokenfile

import (
	"errors"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procReplaceFileW = kernel32.NewProc("ReplaceFileW")
)

const (
	replacefileIgnoreMergeErrors = 0x2
	replacefileIgnoreACLErrors   = 0x4
)

// Replace moves tmp over path. MoveFileEx, what os.Rename uses, fails while
// any handle is open on the target, even one opened with delete sharing;
// ReplaceFileW replaces the target under such handles (an open reader keeps
// the old contents, the path leads to the new file). A target that does not
// exist yet is simply renamed into place.
func Replace(tmp, path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return os.Rename(tmp, path)
	}
	replaced, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	replacement, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	r, _, e := procReplaceFileW.Call(
		uintptr(unsafe.Pointer(replaced)),
		uintptr(unsafe.Pointer(replacement)),
		0,
		replacefileIgnoreMergeErrors|replacefileIgnoreACLErrors,
		0,
		0,
	)
	if r != 0 {
		return nil
	}
	// the target vanished between the stat and the call: rename it in
	if errno, ok := e.(syscall.Errno); ok && errno == windows.ERROR_FILE_NOT_FOUND {
		return os.Rename(tmp, path)
	}
	return &os.LinkError{Op: "replace", Old: tmp, New: path, Err: e}
}
