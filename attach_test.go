package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractNoAttachFlag(t *testing.T) {
	tests := []struct {
		name      string
		in        []string
		wantArgs  []string
		wantFound bool
	}{
		{"absent", []string{"--version"}, []string{"--version"}, false},
		{"only flag", []string{"--no-attach"}, []string{}, true},
		{"leading", []string{"--no-attach", "--version"}, []string{"--version"}, true},
		{"middle", []string{"-p", "--no-attach", "hi"}, []string{"-p", "hi"}, true},
		{"empty", nil, []string{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := extractNoAttachFlag(tt.in)
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if strings.Join(got, " ") != strings.Join(tt.wantArgs, " ") {
				t.Fatalf("args = %v, want %v", got, tt.wantArgs)
			}
		})
	}
}

// writePortFile points proxyPortFilePath() at a temp home and writes the port.
func writePortFile(t *testing.T, port string) {
	t.Helper()
	home := t.TempDir()
	// os.UserHomeDir reads USERPROFILE on Windows, HOME elsewhere. Set both so
	// the test is hermetic on any platform.
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude2kiro")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if port != "" {
		if err := os.WriteFile(filepath.Join(dir, "proxy.port"), []byte(port), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetectLiveProxy_Healthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// srv.URL is http://127.0.0.1:<port>
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	writePortFile(t, port)

	url, ok := detectLiveProxy()
	if !ok {
		t.Fatalf("expected live proxy, got ok=false")
	}
	if url != "http://127.0.0.1:"+port {
		t.Fatalf("url = %q, want http://127.0.0.1:%s", url, port)
	}
}

func TestDetectLiveProxy_NoPortFile(t *testing.T) {
	writePortFile(t, "") // temp home with no proxy.port
	if _, ok := detectLiveProxy(); ok {
		t.Fatalf("expected ok=false with no port file")
	}
}

func TestDetectLiveProxy_StalePort(t *testing.T) {
	// A port file that points at a port with nothing listening must fail fast
	// (connection refused), not hang and not report a live proxy.
	writePortFile(t, "1") // port 1: nothing listening
	if _, ok := detectLiveProxy(); ok {
		t.Fatalf("expected ok=false for stale/dead port")
	}
}

func TestDetectLiveProxy_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	writePortFile(t, port)
	if _, ok := detectLiveProxy(); ok {
		t.Fatalf("expected ok=false when /health returns non-200")
	}
}
