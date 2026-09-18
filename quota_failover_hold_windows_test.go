//go:build windows

package main

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// holdFileUnreadable opens path with no sharing, so every other read of it
// fails, and returns the function that releases it; nil when the platform
// still lets the file be read.
func holdFileUnreadable(t *testing.T, path string) func() {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(path); err == nil {
		windows.CloseHandle(h)
		return nil
	}
	return func() { windows.CloseHandle(h) }
}
