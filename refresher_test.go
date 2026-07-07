package main

import (
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// TestStartTokenRefresher verifies the background refresher starts and its stop
// function is safe and idempotent. Home is redirected to an empty temp dir so the
// immediate refresh() finds no token and is a harmless no-op (no network).
func TestStartTokenRefresher(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	cachedToken = nil
	t.Cleanup(func() { cachedToken = nil })

	stop := startTokenRefresher(logger.NewLogger(10))
	// Must be safe to call, and idempotent (a defer plus an explicit stop must
	// not double-close the channel and panic).
	stop()
	stop()
}
