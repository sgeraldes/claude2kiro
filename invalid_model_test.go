package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

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

// TestProxyEndToEnd_SurfacesInvalidModel reproduces the "claude-fable-5" loop:
// Kiro answers 400 INVALID_MODEL_ID for a model id it does not serve. The proxy
// must not burn its retry budget on that deterministic failure (exactly one
// upstream request) and must surface a non-retryable invalid_request_error —
// the previous overloaded_error made Claude Code re-send the request silently
// forever, showing the user nothing.
func TestProxyEndToEnd_SurfacesInvalidModel(t *testing.T) {
	writeFakeToken(t)
	withStubCatalog(t, stubList())

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
		t.Errorf("INVALID_MODEL_ID should surface invalid_request_error; got:\n%s", body)
	}
	if strings.Contains(body, "overloaded_error") {
		t.Errorf("INVALID_MODEL_ID must not be labeled overloaded_error (the SDK auto-retries it); got:\n%s", body)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("backend hit %d times, want 1 (INVALID_MODEL_ID is deterministic; retrying it can never succeed)", got)
	}
}

// TestNonStreamSurfacesBackendError verifies the non-streaming path returns a
// real Anthropic error envelope on a CodeWhisperer error instead of parsing the
// error body as an event stream and answering 200 with empty content.
func TestNonStreamSurfacesBackendError(t *testing.T) {
	writeFakeToken(t)
	withStubCatalog(t, stubList())

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

	if rec.Code == http.StatusOK {
		t.Fatalf("non-stream backend error must not answer 200; body:\n%s", rec.Body.String())
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
