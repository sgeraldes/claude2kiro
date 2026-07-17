package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
)

// TestRefreshTokenSocial_TimesOutOnHangingEndpoint locks in that a Kiro refresh
// endpoint that accepts the connection but never responds fails the refresh
// after the configured HTTP timeout instead of blocking forever. Every command
// (run, server, TUI, request handler) refreshes an expiring token at startup,
// so an unbounded refresh call turns a transient endpoint outage into a
// process-wide freeze — the "claude2kiro prints a few lines then stops" bug.
// Mirrors cmd/token_refresh_test.go for this package's copy of the function.
func TestRefreshTokenSocial_TimesOutOnHangingEndpoint(t *testing.T) {
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hold the connection open, never respond
	}))
	t.Cleanup(func() {
		close(release) // unblock the handler so Close doesn't wait forever
		hang.Close()
	})

	orig := config.Get()
	cp := *orig
	cp.Advanced.KiroRefreshEndpoint = hang.URL
	cp.Network.HTTPTimeout = 250 * time.Millisecond
	config.Set(&cp)
	t.Cleanup(func() { config.Set(orig) })

	done := make(chan error, 1)
	go func() {
		_, err := refreshTokenSocial(TokenData{RefreshToken: "test-refresh-token"})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("refresh against a hanging endpoint must return an error, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refreshTokenSocial blocked on an unresponsive endpoint — this freezes every command at startup")
	}
}
