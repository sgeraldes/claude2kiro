//go:build !windows

package tokenfile

import "os"

// ReadFile reads a token file. On Unix a rename over an open file is never
// blocked, so this is os.ReadFile.
func ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
