package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sgeraldes/claude2kiro/cmd"
	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/kiroide"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// The proxy (main) and the commands (cmd) present the same Kiro IDE version.
func TestMainAndCmdSendTheSameUserAgent(t *testing.T) {
	var mu sync.Mutex
	agents := map[string]string{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		agents[r.URL.Path] = r.Header.Get("User-Agent")
		mu.Unlock()
		if r.URL.Path == "/credits" {
			w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(cwFrame(`{"content":"hi"}`))
	}))
	t.Cleanup(fake.Close)
	withIdentities(t, map[string]TokenData{"": primaryToken()})
	failoverConfig(t, fake.URL+"/generate")
	cfg := *config.Get()
	cfg.Advanced.CreditsEndpoint = fake.URL + "/credits"
	withConfig(t, &cfg)

	handleNonStreamRequestCtx(context.Background(), httptest.NewRecorder(), testRequest(), logger.NewLogger(50), "sess", "req")
	cmd.GetCreditsInfo()

	mu.Lock()
	defer mu.Unlock()
	proxyUA, cmdUA := agents["/generate"], agents["/credits"]
	if proxyUA == "" || cmdUA == "" {
		t.Fatalf("missing a request: %v", agents)
	}
	if proxyUA != cmdUA || !strings.Contains(proxyUA, kiroide.Version) {
		t.Fatalf("proxy User-Agent %q, cmd User-Agent %q: want both to carry %s", proxyUA, cmdUA, kiroide.Version)
	}
}
