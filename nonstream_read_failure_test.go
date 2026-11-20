package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
