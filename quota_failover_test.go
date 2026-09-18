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
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/profile"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// One Kiro subscription is one monthly credit pool. When the pool is exhausted
// the backend answers 402 MONTHLY_REQUEST_COUNT to every request; the only way
// to keep working is another subscribed identity. These tests drive the real
// handlers against a fake backend that exhausts the primary identity and
// assert the proxy moves to the fallback profile's token transparently and
// that the client receives the fallback's real answer.

const (
	quotaBody       = `{"message":"You have reached the limit.","reason":"MONTHLY_REQUEST_COUNT"}`
	invalidBearer   = `{"message":"The bearer token included in the request is invalid.","reason":null}`
	fallbackContent = "FALLBACK-OK"
)

// fakeQuotaBackend answers 402 to the bearers in exhausted, 403 to the bearers
// in expired, and a content frame to any other, recording every bearer and
// profileArn it sees, in order. hold, when set, blocks each request until the
// channel is closed so the test can interleave responses.
type fakeQuotaBackend struct {
	mu        sync.Mutex
	exhausted map[string]bool
	expired   map[string]bool
	bearers   []string
	arns      []string
	hold      chan struct{}
	server    *httptest.Server
}

func newFakeQuotaBackend(t *testing.T, exhausted ...string) *fakeQuotaBackend {
	t.Helper()
	f := &fakeQuotaBackend{exhausted: map[string]bool{}, expired: map[string]bool{}}
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
		isExpired := f.expired[bearer]
		hold := f.hold
		f.mu.Unlock()
		if hold != nil {
			<-hold
		}
		switch {
		case isExhausted:
			w.WriteHeader(http.StatusPaymentRequired)
			io.WriteString(w, quotaBody)
		case isExpired:
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, invalidBearer)
		default:
			w.WriteHeader(http.StatusOK)
			w.Write(cwFrame(`{"content":"` + fallbackContent + `"}`))
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// rejectBearer makes the backend answer 403 "invalid bearer" to a bearer.
func (f *fakeQuotaBackend) rejectBearer(bearer string) {
	f.mu.Lock()
	f.expired[bearer] = true
	f.mu.Unlock()
}

func (f *fakeQuotaBackend) seen() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bearers...), append([]string(nil), f.arns...)
}

// waitForRequests blocks until the backend has seen at least n requests.
func (f *fakeQuotaBackend) waitForRequests(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := f.seen()
		if len(b) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("backend saw %d requests, waited for %d", len(b), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *fakeQuotaBackend) release() {
	f.mu.Lock()
	hold := f.hold
	f.hold = nil
	f.mu.Unlock()
	if hold != nil {
		close(hold)
	}
}

// withIdentities points the token directory at a temp home holding one token
// file per identity ("" is the default profile) and resets every piece of
// identity state the proxy keeps when the test ends.
func withIdentities(t *testing.T, tokens map[string]TokenData) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".aws", "sso", "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, tok := range tokens {
		writeIdentityToken(t, name, tok)
	}
	resetIdentityState()
	t.Cleanup(resetIdentityState)
	return home
}

// identityFile is the token file of a test identity. "" is the primary: the
// profile the test process was launched as, so the fixtures hold under
// CLAUDE2KIRO_PROFILE=<name> as well as without it.
func identityFile(name string) string {
	if name == "" {
		name = profile.Name()
	}
	return tokenFilePathFor(name)
}

// primaryLabel is how the primary identity is named in messages.
func primaryLabel() string {
	if profile.Name() == "" {
		return profile.DefaultLabel
	}
	return profile.Name()
}

func writeIdentityToken(t *testing.T, name string, tok TokenData) {
	t.Helper()
	data, err := json.Marshal(tok)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityFile(name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readIdentityToken(t *testing.T, name string) TokenData {
	t.Helper()
	data, err := os.ReadFile(identityFile(name))
	if err != nil {
		t.Fatal(err)
	}
	var tok TokenData
	if err := json.Unmarshal(data, &tok); err != nil {
		t.Fatal(err)
	}
	return tok
}

func farFuture() string { return time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339) }

func primaryToken() TokenData {
	return TokenData{AccessToken: "primary-token", AuthMethod: "IdC", ProfileArn: "arn:primary", ExpiresAt: farFuture()}
}

func fallbackToken() TokenData {
	return TokenData{AccessToken: "kiro2-token", AuthMethod: "IdC", ProfileArn: "arn:kiro2", ExpiresAt: farFuture()}
}

func failoverConfig(t *testing.T, backend string, fallbacks ...string) *config.Config {
	t.Helper()
	cfg := *config.Get()
	cfg.Advanced.CodeWhispererEndpoint = backend
	cfg.Auth.FallbackProfiles = fallbacks
	withConfig(t, &cfg)
	return &cfg
}

func testRequest() AnthropicRequest {
	return AnthropicRequest{Model: "claude-sonnet-4-5", Messages: []AnthropicRequestMessage{{Role: "user", Content: "hi"}}, MaxTokens: 10}
}

func streamOnce(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	if _, err := getToken(); err != nil {
		t.Fatal(err)
	}
	handleStreamRequestWithLogger(rec, testRequest(), logger.NewLogger(50), "sess", "req", nil)
	return rec
}

func nonStreamOnce(t *testing.T) (*httptest.ResponseRecorder, int) {
	t.Helper()
	rec := httptest.NewRecorder()
	if _, err := getToken(); err != nil {
		t.Fatal(err)
	}
	status := handleNonStreamRequest(rec, testRequest(), logger.NewLogger(50), "sess", "req")
	return rec, status
}

// assertFallbackAnswer checks the client got the fallback's translated content
// and no error event at all.
func assertFallbackAnswer(t *testing.T, body string) {
	t.Helper()
	if !strings.Contains(body, fallbackContent) {
		t.Fatalf("client did not receive the fallback's content:\n%s", body)
	}
	if strings.Contains(body, `"type":"error"`) || strings.Contains(body, "MONTHLY_REQUEST_COUNT") {
		t.Fatalf("client saw an error:\n%s", body)
	}
}

func TestIsMonthlyQuotaExceeded(t *testing.T) {
	if !isMonthlyQuotaExceeded(402, []byte(quotaBody)) {
		t.Fatal("402 MONTHLY_REQUEST_COUNT must count as quota exhaustion")
	}
	if isMonthlyQuotaExceeded(403, []byte(quotaBody)) {
		t.Fatal("403 is not a quota answer")
	}
	if isMonthlyQuotaExceeded(200, nil) {
		t.Fatal("200 is not a quota answer")
	}
	if got := quotaReason([]byte(quotaBody)); got != "MONTHLY_REQUEST_COUNT" {
		t.Fatalf("reason: %q", got)
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
	assertFallbackAnswer(t, rec.Body.String())
}

func TestFailoverIsStickyForLaterRequests(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": fallbackToken()})
	backend := newFakeQuotaBackend(t, "primary-token")
	failoverConfig(t, backend.server.URL, "kiro2")

	streamOnce(t)
	rec := streamOnce(t)

	bearers, _ := backend.seen()
	if len(bearers) != 3 || bearers[2] != "kiro2-token" {
		t.Fatalf("the second request must go straight to the fallback: %v", bearers)
	}
	assertFallbackAnswer(t, rec.Body.String())
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
	if got := profile.Active(); got != profile.Name() {
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

	rec := streamOnce(t)

	if got := profile.Active(); got != "ops" {
		t.Fatalf("a fallback that was never logged in must be skipped, got %q", got)
	}
	bearers, _ := backend.seen()
	if len(bearers) != 2 || bearers[1] != "kiro2-token" {
		t.Fatalf("bearers: %v", bearers)
	}
	assertFallbackAnswer(t, rec.Body.String())
}

func TestNonStreamSwitchesToFallbackIdentityOn402(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": fallbackToken()})
	backend := newFakeQuotaBackend(t, "primary-token")
	failoverConfig(t, backend.server.URL, "kiro2")

	rec, status := nonStreamOnce(t)

	bearers, arns := backend.seen()
	if len(bearers) != 2 || bearers[1] != "kiro2-token" || arns[1] != "arn:kiro2" {
		t.Fatalf("bearers %v arns %v", bearers, arns)
	}
	if status != http.StatusOK {
		t.Fatalf("status %d, body:\n%s", status, rec.Body.String())
	}
	assertFallbackAnswer(t, rec.Body.String())
	if got := profile.Active(); got != "kiro2" {
		t.Fatalf("active identity: %q", got)
	}
}

// H01: two requests leave with the primary token; the second one's 402 arrives
// after the first already moved the proxy to the fallback. It must adopt the
// fallback, not mark it exhausted.
func TestLate402FromOldIdentityDoesNotExhaustTheFallback(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": fallbackToken()})
	backend := newFakeQuotaBackend(t, "primary-token")
	failoverConfig(t, backend.server.URL, "kiro2")

	backend.hold = make(chan struct{})
	if _, err := getToken(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	bodies := make([]*httptest.ResponseRecorder, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			statuses[i] = handleNonStreamRequest(rec, testRequest(), nil, "sess", "req")
			bodies[i] = rec
		}()
	}
	backend.waitForRequests(t, 2)
	backend.release()
	wg.Wait()

	for i := range 2 {
		if statuses[i] != http.StatusOK {
			t.Fatalf("request %d: status %d body %s", i, statuses[i], bodies[i].Body.String())
		}
		assertFallbackAnswer(t, bodies[i].Body.String())
	}
	identityMu.Lock()
	exhaustedFallback := exhaustedIdentities["kiro2"] != ""
	identityMu.Unlock()
	if exhaustedFallback {
		t.Fatal("a late 402 from the primary marked the healthy fallback as exhausted")
	}
	if got := profile.Active(); got != "kiro2" {
		t.Fatalf("active identity: %q", got)
	}
}

// H02: a profileArn discovery for the primary that finishes after the switch
// must write back to the primary's file, never to the fallback's.
func TestDiscoveryFinishingAfterSwitchWritesToItsOwnFile(t *testing.T) {
	primary := primaryToken()
	primary.ProfileArn = "" // forces discovery on read
	withIdentities(t, map[string]TokenData{"": primary, "kiro2": fallbackToken()})

	release := make(chan struct{})
	profiles := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		io.WriteString(w, `{"profiles":[{"arn":"arn:discovered","profileName":"p"}]}`)
	}))
	t.Cleanup(profiles.Close)
	backend := newFakeQuotaBackend(t, "primary-token")
	cfg := failoverConfig(t, backend.server.URL, "kiro2")
	cfg.Advanced.ProfilesEndpoint = profiles.URL
	config.Set(cfg)

	// Reader A: getToken() on the primary, blocked inside discovery.
	ident := currentIdentity()
	done := make(chan TokenData, 1)
	go func() {
		tok, _ := getToken()
		done <- tok
	}()
	time.Sleep(50 * time.Millisecond)

	// Meanwhile the proxy fails over to kiro2 (a request saw a 402). The
	// switch needs the token lock the reader holds, so it completes only once
	// discovery returns; either way the write must land in the primary's file.
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(release)
	}()
	if _, next, err := switchToFallbackIdentity(ident); err != nil || next.Name != "kiro2" {
		t.Fatalf("switch: %v %+v", err, next)
	}
	<-done

	if got := readIdentityToken(t, "kiro2").AccessToken; got != "kiro2-token" {
		t.Fatalf("fallback token file was overwritten: bearer now %q", got)
	}
	if got := readIdentityToken(t, "").ProfileArn; got != "arn:discovered" {
		t.Fatalf("discovery must land in the primary's file, got %q", got)
	}
	if tok, err := getToken(); err != nil || tok.AccessToken != "kiro2-token" {
		t.Fatalf("active token after switch: %+v %v", tok, err)
	}
}

// H03: a successful refresh publishes the new bearer immediately, even if a
// reader repopulated the cache with the old file while the refresh was out.
func TestRefreshPublishesTheNewBearer(t *testing.T) {
	expiring := TokenData{AccessToken: "old-token", RefreshToken: "r", AuthMethod: "Social", ProfileArn: "arn:p",
		ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}
	withIdentities(t, map[string]TokenData{"": expiring})

	release := make(chan struct{})
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		io.WriteString(w, `{"accessToken":"fresh-token","refreshToken":"r2","expiresAt":"`+farFuture()+`"}`)
	}))
	t.Cleanup(refresh.Close)
	cfg := *config.Get()
	cfg.Advanced.KiroRefreshEndpoint = refresh.URL
	withConfig(t, &cfg)

	done := make(chan error, 1)
	go func() { done <- tryRefreshToken() }()
	time.Sleep(50 * time.Millisecond)
	if tok, _ := getToken(); tok.AccessToken != "old-token" { // repopulates the cache with the old file
		t.Fatalf("during refresh: %q", tok.AccessToken)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if tok, _ := getToken(); tok.AccessToken != "fresh-token" {
		t.Fatalf("after refresh the cache must hand out the new bearer, got %q", tok.AccessToken)
	}
}

// H04: four transient failures followed by a 402 still fail over instead of
// surfacing the quota answer as a retryable overload.
func TestQuotaOnTheLastAttemptStillFailsOver(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": fallbackToken()})
	var mu sync.Mutex
	var bearers []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		bearers = append(bearers, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		n := len(bearers)
		mu.Unlock()
		switch {
		case n <= 4:
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"message":"unavailable"}`)
		case n == 5:
			w.WriteHeader(http.StatusPaymentRequired)
			io.WriteString(w, quotaBody)
		default:
			w.WriteHeader(http.StatusOK)
			w.Write(cwFrame(`{"content":"` + fallbackContent + `"}`))
		}
	}))
	t.Cleanup(backend.Close)
	failoverConfig(t, backend.URL, "kiro2")

	rec := streamOnce(t)

	mu.Lock()
	got := append([]string(nil), bearers...)
	mu.Unlock()
	if len(got) != 6 || got[5] != "kiro2-token" {
		t.Fatalf("expected a sixth attempt on the fallback, got %v", got)
	}
	assertFallbackAnswer(t, rec.Body.String())
}

// H05: the reserve's access token expired days ago; the switch refreshes it
// before the retry, in both handlers.
func TestExpiredFallbackTokenIsRefreshedOnSwitch(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(map[bool]string{true: "stream", false: "non-stream"}[stream], func(t *testing.T) {
			stale := TokenData{AccessToken: "kiro2-stale", RefreshToken: "r", AuthMethod: "Social", ProfileArn: "arn:kiro2",
				ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
			withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": stale})
			refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"accessToken":"kiro2-token","refreshToken":"r2","expiresAt":"`+farFuture()+`"}`)
			}))
			t.Cleanup(refresh.Close)
			backend := newFakeQuotaBackend(t, "primary-token")
			backend.expired["kiro2-stale"] = true
			cfg := failoverConfig(t, backend.server.URL, "kiro2")
			cfg.Advanced.KiroRefreshEndpoint = refresh.URL
			config.Set(cfg)

			var body string
			if stream {
				body = streamOnce(t).Body.String()
			} else {
				rec, status := nonStreamOnce(t)
				if status != http.StatusOK {
					t.Fatalf("status %d: %s", status, rec.Body.String())
				}
				body = rec.Body.String()
			}
			bearers, _ := backend.seen()
			if len(bearers) != 2 || bearers[1] != "kiro2-token" {
				t.Fatalf("the retry must use the refreshed reserve token: %v", bearers)
			}
			assertFallbackAnswer(t, body)
		})
	}
}

// H06: a 403 from the old identity that lands after the switch adopts the
// fallback's bearer together with the fallback's profileArn.
func TestLate403AfterSwitchAdoptsFallbackBearerAndArn(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": fallbackToken()})
	backend := newFakeQuotaBackend(t)
	backend.expired["primary-token"] = true
	failoverConfig(t, backend.server.URL, "kiro2")

	// The proxy moves to kiro2 while this request's first attempt is parked.
	backend.hold = make(chan struct{})
	if _, err := getToken(); err != nil {
		t.Fatal(err)
	}
	ident := currentIdentity()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		handleStreamRequestWithLogger(rec, testRequest(), logger.NewLogger(50), "sess", "req", nil)
		done <- rec
	}()
	backend.waitForRequests(t, 1)
	if _, next, err := switchToFallbackIdentity(ident); err != nil || next.Name != "kiro2" {
		t.Fatalf("switch: %v %+v", err, next)
	}
	backend.release()
	rec := <-done

	bearers, arns := backend.seen()
	if len(bearers) != 2 || bearers[1] != "kiro2-token" || arns[1] != "arn:kiro2" {
		t.Fatalf("bearers %v arns %v", bearers, arns)
	}
	assertFallbackAnswer(t, rec.Body.String())
}

// H07: a fallback without a profileArn asks the API with its own bearer and
// never borrows the ARN Kiro IDE stored for the launched identity.
func TestFallbackDoesNotBorrowTheIDEProfile(t *testing.T) {
	fallback := fallbackToken()
	fallback.ProfileArn = ""
	home := withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": fallback})
	for _, ide := range kiroProfileFilePaths() {
		if !strings.HasPrefix(ide, home) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(ide), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ide, []byte(`{"arn":"arn:primary-ide"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	profiles := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"profiles":[{"arn":"arn:kiro2","profileName":"p"}]}`)
	}))
	t.Cleanup(profiles.Close)
	backend := newFakeQuotaBackend(t, "primary-token")
	cfg := failoverConfig(t, backend.server.URL, "kiro2")
	cfg.Advanced.ProfilesEndpoint = profiles.URL
	config.Set(cfg)

	streamOnce(t)

	_, arns := backend.seen()
	if len(arns) != 2 || arns[1] != "arn:kiro2" {
		t.Fatalf("fallback ARN must come from the API with its own bearer, got %v", arns)
	}
	if got := readIdentityToken(t, "kiro2").ProfileArn; got != "arn:kiro2" {
		t.Fatalf("persisted fallback ARN: %q", got)
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

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = stdout
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestCreditsAllListsIdentitiesThatAreNotLoggedIn(t *testing.T) {
	withIdentities(t, map[string]TokenData{})
	cfg := *config.Get()
	cfg.Auth.FallbackProfiles = []string{"kiro2"}
	withConfig(t, &cfg)

	var ok bool
	out := captureStdout(t, func() { ok = printAllIdentitiesCredits() })

	if !ok {
		t.Fatal("identities that are not logged in are reported, not errors")
	}
	for _, want := range []string{"== default (primary)", "CLAUDE2KIRO_PROFILE= claude2kiro login", "== kiro2 (fallback 1)", "CLAUDE2KIRO_PROFILE=kiro2 claude2kiro login"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q missing in:\n%s", want, out)
		}
	}
	if got := profile.Active(); got != profile.Name() {
		t.Fatalf("credits --all must leave the identity where it was: %q", got)
	}
}

// H10: credits --all queries each identity with its own bearer, refreshing a
// stale one first, and prints distinct balances.
func TestCreditsAllUsesEachIdentityAndRefreshesStaleOnes(t *testing.T) {
	stale := TokenData{AccessToken: "kiro2-stale", RefreshToken: "r", AuthMethod: "Social",
		ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": stale})
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"accessToken":"kiro2-token","refreshToken":"r2","expiresAt":"`+farFuture()+`"}`)
	}))
	t.Cleanup(refresh.Close)
	credits := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		used := map[string]string{"primary-token": "10000", "kiro2-token": "12"}[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if used == "" {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, invalidBearer)
			return
		}
		io.WriteString(w, `{"daysUntilReset":12,"subscriptionInfo":{"subscriptionTitle":"KIRO POWER"},"usageBreakdownList":[{"resourceType":"CREDIT","currentUsageWithPrecision":`+used+`,"usageLimitWithPrecision":10000}]}`)
	}))
	t.Cleanup(credits.Close)
	cfg := *config.Get()
	cfg.Auth.FallbackProfiles = []string{"kiro2"}
	cfg.Advanced.CreditsEndpoint = credits.URL
	cfg.Advanced.KiroRefreshEndpoint = refresh.URL
	withConfig(t, &cfg)

	var ok bool
	out := captureStdout(t, func() { ok = printAllIdentitiesCredits() })

	if !ok {
		t.Fatalf("expected success:\n%s", out)
	}
	if !strings.Contains(out, "Used:      10000.0 / 10000") || !strings.Contains(out, "Used:      12.0 / 10000") {
		t.Fatalf("both balances must be printed:\n%s", out)
	}
	if got := profile.Active(); got != profile.Name() {
		t.Fatalf("identity after credits --all: %q", got)
	}
}
