//go:build !windows

package tokenfile

import "os"

// Replace moves tmp over path. On Unix a rename replaces the target
// whatever handles are open on it.
func Replace(tmp, path string) error {
	return os.Rename(tmp, path)
}
