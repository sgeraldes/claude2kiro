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

// holdFileUndeletable opens path readable but without delete sharing, so it
// can be read but not removed or replaced; nil when the platform still
// lets it be removed.
func holdFileUndeletable(t *testing.T, path string) func() {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	probe := path + ".probe"
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
	return func() { _ = f.Close() }
}
