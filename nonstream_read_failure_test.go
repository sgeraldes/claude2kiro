package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// cutAfterFirstFrame answers 200, sends one frame and then drops the
// connection, as the backend does when it resets a stream mid-answer.
func cutAfterFirstFrame(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	w.WriteHeader(http.StatusOK)
	w.Write(cwFrame(`{"content":"FIRST"}`))
	w.(http.Flusher).Flush()
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		panic(err)
	}
	conn.Close()
}

// A non-streaming answer the backend cuts mid-response is logged with its
// cause and comes back as an Anthropic error envelope, not as a bare text 500.
func TestNonStreamReadFailureIsLoggedAndAnErrorEnvelope(t *testing.T) {
	streamTestSetup(t, cutAfterFirstFrame)
	lg := logger.NewLogger(50)

	rec := httptest.NewRecorder()
	status := handleNonStreamRequest(rec, testRequest(), lg, "sess", "req")

	if status != rec.Code {
		t.Fatalf("returned status %d, wrote %d", status, rec.Code)
	}
	if rec.Code < 500 {
		t.Fatalf("a cut response is a server-side failure, got status %d", rec.Code)
	}
	var env struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("the body is not an Anthropic error envelope: %v\n%s", err, rec.Body.String())
	}
	if env.Type != "error" || env.Error.Message == "" {
		t.Fatalf("want an error envelope with a message, got %s", rec.Body.String())
	}
	if !strings.Contains(env.Error.Message, "ended before it was complete") {
		t.Fatalf("the message should say the answer was cut: %q", env.Error.Message)
	}

	var logged string
	for _, e := range lg.GetEntries() {
		if e.Type == logger.LogTypeErr {
			logged += e.Body + "\n"
		}
	}
	if !strings.Contains(logged, "read failed") || !strings.Contains(logged, "convId=") {
		t.Fatalf("want an [ERR] line naming the convId and the read failure, got:\n%s", logged)
	}
}

// withBackendIdleCutAfter shortens the silence after which a reset stream is
// taken for Kiro's idle cut.
func withBackendIdleCutAfter(t *testing.T, d time.Duration) {
	t.Helper()
	old := backendIdleCutAfter
	backendIdleCutAfter = d
	t.Cleanup(func() { backendIdleCutAfter = old })
}

// toolStartThenSilentCut sends the start of a Write tool call, stays silent for
// silence, then drops the connection: what Kiro does when the model takes
// longer than its idle limit to write a tool input it holds back.
func toolStartThenSilentCut(silence time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write(cwFrame(`{"name":"Write","toolUseId":"tu_big","input":"{\"file_path\": \"big.html\""}`))
		w.(http.Flusher).Flush()
		time.Sleep(silence)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			panic(err)
		}
		conn.Close()
	}
}

func errorLines(lg *logger.Logger) string {
	var logged string
	for _, e := range lg.GetEntries() {
		if e.Type == logger.LogTypeErr {
			logged += e.Body + "\n"
		}
	}
	return logged
}

// A stream cut by the idle limit stays retryable: one resend (Claude Code falls
// back to a non-streaming request) is allowed, and the log says how long the
// backend was silent.
func TestStreamIdleCutStaysRetryable(t *testing.T) {
	withBackendIdleCutAfter(t, 100*time.Millisecond)
	streamTestSetup(t, toolStartThenSilentCut(200*time.Millisecond))
	withKeepaliveInterval(t, 0)

	lg := logger.NewLogger(50)
	rec := newLiveRecorder()
	handleStreamRequestWithLogger(rec, testRequest(), lg, "sess", "req", nil)

	assertStreamFailed(t, rec.String())
	if logged := errorLines(lg); !strings.Contains(logged, "silent") || !strings.Contains(logged, "convId=") {
		t.Fatalf("want an [ERR] line with the silence and the convId, got: %s", logged)
	}
}

// A non-streaming request cut by the idle limit (Claude Code's resend of a cut
// stream) is answered as final: a 400 invalid_request_error, which Claude Code
// does not resend, that says what happened and what to ask for instead.
func TestNonStreamIdleCutIsNotRetryable(t *testing.T) {
	withBackendIdleCutAfter(t, 100*time.Millisecond)
	streamTestSetup(t, toolStartThenSilentCut(200*time.Millisecond))
	lg := logger.NewLogger(50)

	rec := httptest.NewRecorder()
	handleNonStreamRequest(rec, testRequest(), lg, "sess", "req")

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid_request_error"`) {
		t.Fatalf("want 400 invalid_request_error, got %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Write") || !strings.Contains(rec.Body.String(), "240 s") {
		t.Fatalf("the message should name the tool call and the limit: %s", rec.Body.String())
	}
	if logged := errorLines(lg); !strings.Contains(logged, "silent") {
		t.Fatalf("want an [ERR] line with the silence, got: %s", logged)
	}
}
