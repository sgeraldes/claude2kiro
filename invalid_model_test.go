package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/models"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

func TestIsInvalidModelID(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			// Exact body CodeWhisperer returned for "claude-fable-5" (2026-07-14 incident).
			name: "codewhisperer invalid model reason",
			body: `{"message":"Invalid model. Please select a different model to continue.","reason":"INVALID_MODEL_ID"}`,
			want: true,
		},
		{
			name: "reason only",
			body: `{"reason":"INVALID_MODEL_ID"}`,
			want: true,
		},
		{
			name: "case insensitive reason",
			body: `{"reason":"invalid_model_id"}`,
			want: true,
		},
		{
			name: "message only",
			body: `{"message":"Invalid model. Please select a different model to continue."}`,
			want: true,
		},
		{
			name: "phrase echoed in unrelated error body",
			body: `{"message":"Improperly formed request.","detail":"tool said: invalid model. please select a different model to continue"}`,
			want: false,
		},
		{
			name: "unrelated 400 improperly formed",
			body: `{"message":"Improperly formed request."}`,
			want: false,
		},
		{
			name: "context length is not invalid model",
			body: `{"message":"Input is too long.","reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`,
			want: false,
		},
		{
			name: "non-json body with enum",
			body: `ValidationException: INVALID_MODEL_ID`,
			want: true,
		},
		{
			name: "empty body",
			body: ``,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isInvalidModelID([]byte(tc.body)); got != tc.want {
				t.Errorf("isInvalidModelID(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestProxyEndToEnd_CatchesUnservableModelPreflight is the core of the fix:
// when the live catalog shows the requested model is not servable (e.g.
// claude-fable-5, which Kiro has no equivalent for), the proxy must catch it
// BEFORE contacting the backend — no request, no silent substitution — and
// surface a non-retryable error that lists the account's real models and how
// to switch.
func TestProxyEndToEnd_CatchesUnservableModelPreflight(t *testing.T) {
	writeFakeToken(t)
	withStubCatalog(t, stubList()) // has opus/sonnet/haiku/glm..., NOT fable

	var hits int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write(cwFrame(`{"content":"should-not-be-called"}`))
	}))
	defer backend.Close()
	stubEndpoint(t, backend.URL+"/generateAssistantResponse")

	mux := buildServerMux(logger.NewLogger(10))

	for _, stream := range []bool{true, false} {
		reqBody, _ := json.Marshal(AnthropicRequest{
			Model:     "claude-fable-5",
			MaxTokens: 64,
			Stream:    stream,
			Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
		})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(reqBody)))

		body := rec.Body.String()
		if !strings.Contains(body, "invalid_request_error") {
			t.Errorf("stream=%v: unservable model should surface invalid_request_error; got:\n%s", stream, body)
		}
		if strings.Contains(body, "overloaded_error") {
			t.Errorf("stream=%v: must not be overloaded_error (the SDK auto-retries it); got:\n%s", stream, body)
		}
		if !strings.Contains(body, "claude-fable-5") {
			t.Errorf("stream=%v: error should name the requested model; got:\n%s", stream, body)
		}
		// The message must list the account's real models and point at the fix.
		if !strings.Contains(body, "claude-opus-4.8") || !strings.Contains(body, "/model auto") {
			t.Errorf("stream=%v: error should list available models and mention /model auto; got:\n%s", stream, body)
		}
		if !stream && rec.Code != http.StatusBadRequest {
			t.Errorf("non-stream unservable model status = %d, want 400", rec.Code)
		}
	}

	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("backend hit %d times, want 0 (an unservable model must be caught pre-flight, not sent)", got)
	}
}

// TestProxyEndToEnd_ReactiveInvalidModelNoRetry covers the backstop: when the
// catalog is unavailable, the proxy can't check pre-flight and forwards the
// request; if the backend answers 400 INVALID_MODEL_ID, it must not retry that
// deterministic failure and must surface a non-retryable error (not the
// overloaded_error the SDK auto-retries, which caused the silent loop).
func TestProxyEndToEnd_ReactiveInvalidModelNoRetry(t *testing.T) {
	writeFakeToken(t)
	// Cold catalog: fetch always fails, so Available() is false and the
	// pre-flight servability check lets the request through to the backend.
	orig := modelCatalog
	modelCatalog = models.NewCatalog(time.Minute, func() ([]models.KiroModel, error) {
		return nil, errStub
	})
	t.Cleanup(func() { modelCatalog = orig })

	var hits int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message":"Invalid model. Please select a different model to continue.","reason":"INVALID_MODEL_ID"}`))
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
	if !strings.Contains(body, "invalid_request_error") {
		t.Errorf("reactive INVALID_MODEL_ID should surface invalid_request_error; got:\n%s", body)
	}
	if strings.Contains(body, "overloaded_error") {
		t.Errorf("reactive INVALID_MODEL_ID must not be labeled overloaded_error; got:\n%s", body)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("backend hit %d times, want 1 (INVALID_MODEL_ID is deterministic; retrying it can never succeed)", got)
	}
}

// TestNonStreamSurfacesBackendError verifies the non-streaming path returns a
// real Anthropic error envelope on a CodeWhisperer error instead of parsing the
// error body as an event stream and answering 200 with empty content. Uses a
// cold catalog so the request reaches the backend (pre-flight is skipped).
func TestNonStreamSurfacesBackendError(t *testing.T) {
	writeFakeToken(t)
	orig := modelCatalog
	modelCatalog = models.NewCatalog(time.Minute, func() ([]models.KiroModel, error) {
		return nil, errStub
	})
	t.Cleanup(func() { modelCatalog = orig })

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message":"Invalid model. Please select a different model to continue.","reason":"INVALID_MODEL_ID"}`))
	}))
	defer backend.Close()
	stubEndpoint(t, backend.URL+"/generateAssistantResponse")

	mux := buildServerMux(logger.NewLogger(10))
	reqBody, _ := json.Marshal(AnthropicRequest{
		Model:     "glm-5",
		MaxTokens: 64,
		Stream:    false,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(reqBody)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-stream backend error status = %d, want 400; body:\n%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("non-stream error body is not an Anthropic error envelope: %v\n%s", err, rec.Body.String())
	}
	if envelope.Type != "error" || envelope.Error.Type != "invalid_request_error" {
		t.Errorf("want type=error error.type=invalid_request_error, got %q/%q", envelope.Type, envelope.Error.Type)
	}
	if !strings.Contains(envelope.Error.Message, "glm-5") {
		t.Errorf("error message should name the rejected model; got %q", envelope.Error.Message)
	}
}
