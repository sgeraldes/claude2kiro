// Package tokenfile serializes the writers of a token file across processes.
//
// The proxy refreshes a token while a `claude2kiro login` or `logout` in
// another process may replace or remove the same file. Memory counters cannot
// see that, so every writer takes the lock of the token file and re-reads the
// file under it before deciding what to write.
//
// The lock is the operating system's (flock on Unix, LockFileEx on Windows)
// on a file next to the token, <token>.lock. The kernel drops it when the
// holder exits, however it exits, so there is no stale lock to expire, no
// owner to lose and nothing to delete: the .lock file stays, empty, and only
// ever holds a lock. Two handles in one process contend like two processes
// do, so a path must never take the lock it already holds.
package tokenfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	lockSuffix = ".lock"
	// DefaultWait is how long a writer waits for a holder in another
	// process. A refresh holds the lock through its HTTP call, bounded by the
	// proxy's HTTP timeout (30 s by default), so the wait exceeds that.
	DefaultWait  = 45 * time.Second
	lockInterval = 20 * time.Millisecond
)

// ErrBusy is returned when another holder kept the lock for the whole wait.
var ErrBusy = errors.New("the token file is locked by another claude2kiro process")

// Lock takes the lock of the token file at path and returns the function
// that releases it. It waits DefaultWait for a holder in another process and
// returns an error after that: a caller that does not hold the lock writes
// nothing.
func Lock(path string) (func(), error) {
	return LockFor(path, DefaultWait)
}

// LockFor is Lock with an explicit wait.
func LockFor(path string, wait time.Duration) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	lock := path + lockSuffix
	if err := os.MkdirAll(filepath.Dir(lock), 0o700); err != nil {
		return nil, fmt.Errorf("token lock: %w", err)
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("token lock: %w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		err := tryLock(f)
		if err == nil {
			return func() {
				_ = unlock(f)
				_ = f.Close()
			}, nil
		}
		if !isBusy(err) {
			_ = f.Close()
			return nil, fmt.Errorf("token lock %s: %w", lock, err)
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("%w (%s, waited %s)", ErrBusy, lock, wait)
		}
		time.Sleep(lockInterval)
	}
}

// RenewedPath is the file next to a token file where a refresh keeps the
// token the provider issued when the token file itself could not be
// replaced (a program held it without delete sharing). The next writer or
// reader moves it into place; a login or a logout discards it.
func RenewedPath(path string) string {
	return path + ".renewed"
}
