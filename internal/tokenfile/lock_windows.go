//go:build windows

package tokenfile

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// The lock covers the first byte of the (empty) lock file; LockFileEx locks
// byte ranges, not the file, and a range past the end is allowed.
func tryLock(f *os.File) error {
	var ovl windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ovl)
}

func unlock(f *os.File) error {
	var ovl windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ovl)
}

func isBusy(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING)
}
