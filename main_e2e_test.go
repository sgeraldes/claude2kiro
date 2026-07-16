package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// cwFrame builds one CodeWhisperer event-stream frame in the exact wire format
// the parser consumes: big-endian total length, big-endian header length, the
// (skipped) header bytes, a "vent"-prefixed JSON payload, and a 4-byte CRC.
func cwFrame(jsonPayload string) []byte {
	payload := append([]byte("vent"), []byte(jsonPayload)...)
	header := []byte(":event-type")
	total := uint32(12 + len(header) + len(payload))
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, total)
	binary.Write(&b, binary.BigEndian, uint32(len(header)))
	b.Write(header)
	b.Write(payload)
	binary.Write(&b, binary.BigEndian, uint32(0)) // CRC (parser ignores it)
	return b.Bytes()
}

// writeFakeToken redirects HOME/USERPROFILE to a temp dir and writes a token
// file with a far-future expiry, so the request handler's getToken() succeeds
// and its proactive-refresh branch is skipped (no network).
func writeFakeToken(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	// getToken() memoizes for 1 minute in a package global, so a real token
	// cached by an earlier test would leak into this one (tests run well within
	// the TTL). Reset it before and after so this test reads the fake token.
	cachedToken = nil
	t.Cleanup(func() { cachedToken = nil })
	cacheDir := filepath.Join(tmp, ".aws", "sso", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tok := TokenData{
		AccessToken: "fake-access-token",
		ExpiresAt:   time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		ProfileArn:  "arn:aws:codewhisperer:us-east-1:000000000000:profile/TEST",
	}
	data, _ := json.Marshal(tok)
	if err := os.WriteFile(filepath.Join(cacheDir, "kiro-auth-token.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// stubEndpoint points the proxy's CodeWhisperer endpoint at a test server for
// the duration of the test, restoring the previous config afterwards.
func stubEndpoint(t *testing.T, url string) {
	t.Helper()
	orig := config.Get()
	cp := *orig
	cp.Advanced.CodeWhispererEndpoint = url
	config.Set(&cp)
	t.Cleanup(func() { config.Set(orig) })
}

// TestProxyEndToEnd_TranslatesKiroResponse drives the real /v1/messages mux
// with a stubbed Kiro backend and asserts the full request path: token is read,
// the model id is resolved, the Kiro bearer (not the client's) is sent upstream,
// and the CodeWhisperer binary frame is translated into Anthropic SSE.
func TestProxyEndToEnd_TranslatesKiroResponse(t *testing.T) {
	writeFakeToken(t)
	withStubCatalog(t, stubList()) // resolve "claude-opus-4-8" without network

	var gotAuth string
	var gotBody []byte
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write(cwFrame(`{"content":"E2E-OK"}`))
	}))
	defer backend.Close()
	stubEndpoint(t, backend.URL+"/generateAssistantResponse")

	mux := buildServerMux(logger.NewLogger(10))

	reqBody, _ := json.Marshal(AnthropicRequest{
		Model:     "claude-opus-4-8",
		MaxTokens: 64,
		Stream:    true,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(reqBody))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body:\n%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "E2E-OK") {
		t.Errorf("translated SSE is missing the backend text 'E2E-OK'; got:\n%s", body)
	}
	if !strings.Contains(body, "content_block_delta") {
		t.Errorf("expected Anthropic content_block_delta events; got:\n%s", body)
	}
	if gotAuth != "Bearer fake-access-token" {
		t.Errorf("upstream Authorization = %q, want Bearer fake-access-token", gotAuth)
	}
	if len(gotBody) == 0 {
		t.Error("proxy sent an empty body to the Kiro backend")
	}
}

// TestProxyEndToEnd_SurfacesAccessDenied verifies the v1.1.2 fix: a 403 that is
// NOT a token problem (e.g. a model the account can't use) is surfaced as a
// non-retryable error rather than triggering an endless "refresh and retry".
func TestProxyEndToEnd_SurfacesAccessDenied(t *testing.T) {
	writeFakeToken(t)
	withStubCatalog(t, stubList())

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"User is not authorized to access this model","__type":"AccessDeniedException"}`))
	}))
	defer backend.Close()
	stubEndpoint(t, backend.URL+"/generateAssistantResponse")

	mux := buildServerMux(logger.NewLogger(10))
	reqBody, _ := json.Marshal(AnthropicRequest{
		Model:     "glm-5",
		MaxTokens: 64,
		Stream:    true,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(reqBody)))

	body := rec.Body.String()
	// The error event must be the non-retryable kind and must not claim the
	// token was refreshed (that was the misdiagnosis the fix removed).
	if !strings.Contains(body, "invalid_request_error") {
		t.Errorf("access-denied 403 should surface invalid_request_error; got:\n%s", body)
	}
	if strings.Contains(body, "Token refreshed") {
		t.Errorf("access-denied 403 must not be treated as token expiry; got:\n%s", body)
	}
}

// TestProxyEndToEnd_DoesNotRetryInvalidRequest verifies that a structurally
// invalid request is attempted once. Retrying the identical body cannot repair
// it and previously multiplied each native web-search failure fivefold.
func TestProxyEndToEnd_DoesNotRetryInvalidRequest(t *testing.T) {
	writeFakeToken(t)
	withStubCatalog(t, stubList())

	attempts := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message":"Invalid tool use format.","reason":"REQUEST_BODY_INVALID"}`))
	}))
	defer backend.Close()
	stubEndpoint(t, backend.URL+"/generateAssistantResponse")

	mux := buildServerMux(logger.NewLogger(10))
	reqBody, _ := json.Marshal(AnthropicRequest{
		Model:     "claude-opus-4-8",
		MaxTokens: 64,
		Stream:    true,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "search"}},
	})
	requestBody := bytes.NewReader(reqBody)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", requestBody)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, request)

	if attempts != 1 {
		t.Errorf("invalid request attempts = %d, want 1", attempts)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "invalid_request_error") {
		t.Errorf("invalid request should surface invalid_request_error; got:\n%s", body)
	}
	if !strings.Contains(body, "Invalid tool use format") {
		t.Errorf("invalid request should preserve the backend explanation; got:\n%s", body)
	}
}
