package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// TestStartServerWithLogger_FailedBindKeepsPortFile pins the regression where a
// second proxy failing to bind the port deleted the RUNNING proxy's port file
// (via a defer registered before the bind). A failed start must leave any
// existing ~/.claude2kiro/proxy.port untouched.
func TestStartServerWithLogger_FailedBindKeepsPortFile(t *testing.T) {
	// Occupy the port the way startServerWithLogger binds it ("127.0.0.1:port"),
	// and serve /health so detectLiveProxy recognizes it as a live claude2kiro.
	// This MUST match the proxy's bind address: on Windows a wildcard (":port")
	// listener and a loopback-specific ("127.0.0.1:port") one coexist, so
	// occupying ":0" would NOT create the bind conflict this test needs.
	occupier, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to occupy a port: %v", err)
	}
	defer occupier.Close()
	go http.Serve(occupier, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	_, port, err := net.SplitHostPort(occupier.Addr().String())
	if err != nil {
		t.Fatalf("could not parse occupied port: %v", err)
	}

	// Point ~/.claude2kiro at a temp home holding a sentinel port file that the
	// "running" proxy owns.
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude2kiro")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	portFile := filepath.Join(dir, "proxy.port")
	if err := os.WriteFile(portFile, []byte(port), 0o644); err != nil {
		t.Fatal(err)
	}

	// Attempt to start a second server on the occupied port. It must fail to
	// bind and return WITHOUT deleting the port file.
	startServerWithLogger(port, logger.NewLogger(10))

	if _, err := os.Stat(portFile); err != nil {
		t.Fatalf("proxy.port was removed by a failed second-server start: %v", err)
	}
	got, _ := os.ReadFile(portFile)
	if string(got) != port {
		t.Fatalf("proxy.port clobbered: got %q, want %q", string(got), port)
	}

	// Sanity: the running proxy is detectable, so the failure path used the
	// clearer "already running" message rather than a raw bind error.
	if _, ok := detectLiveProxy(); !ok {
		t.Fatalf("expected the occupied /health server to be detected as a live proxy")
	}
}
