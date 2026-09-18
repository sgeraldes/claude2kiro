//go:build !windows

package main

import "testing"

// holdFileUnreadable has no Unix counterpart: an open file stays readable.
func holdFileUnreadable(t *testing.T, path string) func() {
	t.Helper()
	return nil
}
