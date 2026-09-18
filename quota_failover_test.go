package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/profile"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// One Kiro subscription is one monthly credit pool. When the pool is exhausted
// the backend answers 402 MONTHLY_REQUEST_COUNT to every request; the only way
// to keep working is another subscribed identity. These tests drive the real
// handlers against a fake backend that exhausts the primary identity and
// assert the proxy moves to the fallback profile's token transparently.

const quotaBody = `{"message":"You have reached the limit.","reason":"MONTHLY_REQUEST_COUNT"}`

// fakeQuotaBackend answers 402 to the bearers in exhausted and 200 to any
// other, recording every bearer and profileArn it sees, in order.
type fakeQuotaBackend struct {
	mu        sync.Mutex
	exhausted map[string]bool
	bearers   []string
	arns      []string
	server    *httptest.Server
}

func newFakeQuotaBackend(t *testing.T, exhausted ...string) *fakeQuotaBackend {
	t.Helper()
	f := &fakeQuotaBackend{exhausted: map[string]bool{}}
	for _, b := range exhausted {
		f.exhausted[b] = true
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		body, _ := io.ReadAll(r.Body)
		var parsed CodeWhispererRequest
		_ = json.Unmarshal(body, &parsed)
		f.mu.Lock()
		f.bearers = append(f.bearers, bearer)
		f.arns = append(f.arns, parsed.ProfileArn)
		isExhausted := f.exhausted[bearer]
		f.mu.Unlock()
		if isExhausted {
			w.WriteHeader(http.StatusPaymentRequired)
			io.WriteString(w, quotaBody)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeQuotaBackend) seen() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bearers...), append([]string(nil), f.arns...)
}

// withIdentities points the token directory at a temp home holding one token
// file per identity ("" is the default profile) and resets every piece of
// identity state the proxy keeps when the test ends.
func withIdentities(t *testing.T, tokens map[string]TokenData) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".aws", "sso", "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, tok := range tokens {
		data, err := json.Marshal(tok)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, profile.TokenFileNameFor(name)), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	resetIdentityState()
	t.Cleanup(resetIdentityState)
}

func primaryToken() TokenData {
	return TokenData{AccessToken: "primary-token", AuthMethod: "IdC", ProfileArn: "arn:primary"}
}

func fallbackToken() TokenData {
	return TokenData{AccessToken: "kiro2-token", AuthMethod: "IdC", ProfileArn: "arn:kiro2"}
}

func failoverConfig(t *testing.T, backend string, fallbacks ...string) {
	t.Helper()
	cfg := *config.Get()
	cfg.Advanced.CodeWhispererEndpoint = backend
	cfg.Auth.FallbackProfiles = fallbacks
	withConfig(t, &cfg)
}

func streamOnce(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := AnthropicRequest{Model: "claude-sonnet-4-5", Messages: []AnthropicRequestMessage{{Role: "user", Content: "hi"}}, MaxTokens: 10}
	tok, err := getToken()
	if err != nil {
		t.Fatal(err)
	}
	handleStreamRequestWithLogger(rec, req, tok, logger.NewLogger(50), "sess", "req", nil)
	return rec
}

func TestIsMonthlyQuotaExceeded(t *testing.T) {
	if !isMonthlyQuotaExceeded(402, []byte(quotaBody)) {
		t.Fatal("402 MONTHLY_REQUEST_COUNT must count as quota exhaustion")
	}
	if !isMonthlyQuotaExceeded(402, []byte(`{"message":"no subscription"}`)) {
		t.Fatal("any 402 means this identity cannot serve the request")
	}
	if isMonthlyQuotaExceeded(403, []byte(quotaBody)) {
		t.Fatal("403 is not a quota answer")
	}
	if isMonthlyQuotaExceeded(200, nil) {
		t.Fatal("200 is not a quota answer")
	}
}

func TestStreamSwitchesToFallbackIdentityOn402(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": fallbackToken()})
	backend := newFakeQuotaBackend(t, "primary-token")
	failoverConfig(t, backend.server.URL, "kiro2")

	rec := streamOnce(t)

	bearers, arns := backend.seen()
	if len(bearers) != 2 || bearers[0] != "primary-token" || bearers[1] != "kiro2-token" {
		t.Fatalf("bearers: %v", bearers)
	}
	if arns[1] != "arn:kiro2" {
		t.Fatalf("the retry must carry the fallback identity's profileArn, got %v", arns)
	}
	if got := profile.Active(); got != "kiro2" {
		t.Fatalf("active identity after failover: %q", got)
	}
	if strings.Contains(rec.Body.String(), "MONTHLY_REQUEST_COUNT") {
		t.Fatalf("the client must not see the quota error:\n%s", rec.Body.String())
	}
}

func TestFailoverIsStickyForLaterRequests(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": fallbackToken()})
	backend := newFakeQuotaBackend(t, "primary-token")
	failoverConfig(t, backend.server.URL, "kiro2")

	streamOnce(t)
	streamOnce(t)

	bearers, _ := backend.seen()
	if len(bearers) != 3 || bearers[2] != "kiro2-token" {
		t.Fatalf("the second request must go straight to the fallback: %v", bearers)
	}
}

func TestNoFallbackConfiguredSurfacesTheQuotaError(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": fallbackToken()})
	backend := newFakeQuotaBackend(t, "primary-token")
	failoverConfig(t, backend.server.URL)

	rec := streamOnce(t)

	bearers, _ := backend.seen()
	if len(bearers) != 1 {
		t.Fatalf("no retry without fallbacks: %v", bearers)
	}
	if got := profile.Active(); got != "" {
		t.Fatalf("identity must not move: %q", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "invalid_request_error") || !strings.Contains(body, "MONTHLY_REQUEST_COUNT") {
		t.Fatalf("expected a non-retryable quota error:\n%s", body)
	}
}

func TestAllIdentitiesExhaustedNamesThemAll(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": fallbackToken()})
	backend := newFakeQuotaBackend(t, "primary-token", "kiro2-token")
	failoverConfig(t, backend.server.URL, "kiro2")

	rec := streamOnce(t)

	bearers, _ := backend.seen()
	if len(bearers) != 2 {
		t.Fatalf("expected one attempt per identity: %v", bearers)
	}
	body := rec.Body.String()
	for _, want := range []string{"invalid_request_error", "default", "kiro2"} {
		if !strings.Contains(body, want) {
			t.Fatalf("error must name every exhausted identity (%q missing):\n%s", want, body)
		}
	}
}

func TestFallbackWithoutTokenFileIsSkipped(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken(), "ops": fallbackToken()})
	backend := newFakeQuotaBackend(t, "primary-token")
	failoverConfig(t, backend.server.URL, "kiro2", "ops")

	streamOnce(t)

	if got := profile.Active(); got != "ops" {
		t.Fatalf("a fallback that was never logged in must be skipped, got %q", got)
	}
	bearers, _ := backend.seen()
	if len(bearers) != 2 || bearers[1] != "kiro2-token" {
		t.Fatalf("bearers: %v", bearers)
	}
}

func TestNonStreamSwitchesToFallbackIdentityOn402(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": fallbackToken()})
	backend := newFakeQuotaBackend(t, "primary-token")
	failoverConfig(t, backend.server.URL, "kiro2")

	rec := httptest.NewRecorder()
	req := AnthropicRequest{Model: "claude-sonnet-4-5", Messages: []AnthropicRequestMessage{{Role: "user", Content: "hi"}}, MaxTokens: 10}
	tok, err := getToken()
	if err != nil {
		t.Fatal(err)
	}
	status := handleNonStreamRequest(rec, req, tok, logger.NewLogger(50), "sess", "req")

	bearers, arns := backend.seen()
	if len(bearers) != 2 || bearers[1] != "kiro2-token" || arns[1] != "arn:kiro2" {
		t.Fatalf("bearers %v arns %v", bearers, arns)
	}
	if status == http.StatusPaymentRequired {
		t.Fatal("the client must not see the 402")
	}
	if got := profile.Active(); got != "kiro2" {
		t.Fatalf("active identity: %q", got)
	}
}

func TestExtractNoBrowserFlag(t *testing.T) {
	noBrowser = false
	t.Cleanup(func() { noBrowser = false })
	got := extractNoBrowserFlag([]string{"claude2kiro", "login", "--no-browser", "idc", "https://d5.awsapps.com/start"})
	want := []string{"claude2kiro", "login", "idc", "https://d5.awsapps.com/start"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("args: %v", got)
	}
	if !noBrowser {
		t.Fatal("flag not recorded")
	}
	if err := openBrowser("https://example.test/auth"); err != nil {
		t.Fatalf("with --no-browser openBrowser only prints: %v", err)
	}
}

func TestCreditsAllListsIdentitiesThatAreNotLoggedIn(t *testing.T) {
	withIdentities(t, map[string]TokenData{})
	cfg := *config.Get()
	cfg.Auth.FallbackProfiles = []string{"kiro2"}
	withConfig(t, &cfg)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	ok := printAllIdentitiesCredits()
	w.Close()
	os.Stdout = stdout
	out, _ := io.ReadAll(r)

	if !ok {
		t.Fatal("identities that are not logged in are reported, not errors")
	}
	for _, want := range []string{"== default (primary)", "== kiro2 (fallback 1)", "CLAUDE2KIRO_PROFILE=kiro2 claude2kiro login"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("%q missing in:\n%s", want, out)
		}
	}
	if got := profile.Active(); got != "" {
		t.Fatalf("credits --all must leave the identity where it was: %q", got)
	}
}
