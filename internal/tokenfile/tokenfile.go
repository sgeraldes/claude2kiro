// Package tokenfile serializes the writers of a token file across processes.
//
// The proxy refreshes a token while a `claude2kiro login` or `logout` in
// another process may replace or remove the same file. Memory counters cannot
// see that, so every writer takes a lock file next to the token (created
// exclusively, removed when done) and re-reads the file under it before
// deciding what to write. A lock left behind by a crashed process expires.
package tokenfile

import (
	"errors"
	"os"
	"time"
)

const (
	lockSuffix   = ".lock"
	lockWait     = 3 * time.Second
	lockStale    = 15 * time.Second
	lockInterval = 20 * time.Millisecond
)

// Lock takes the lock of the token file at path and returns the function
// that releases it. It waits a few seconds for a holder in another process;
// after that it proceeds anyway, so a lost lock never blocks a login.
func Lock(path string) func() {
	if path == "" {
		return func() {}
	}
	lock := path + lockSuffix
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(lock) }
		}
		if !errors.Is(err, os.ErrExist) {
			return func() {}
		}
		if info, serr := os.Stat(lock); serr == nil && time.Since(info.ModTime()) > lockStale {
			_ = os.Remove(lock) // a process that died with the lock
			continue
		}
		if time.Now().After(deadline) {
			return func() {}
		}
		time.Sleep(lockInterval)
	}
}
