package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// The exact text the client must see; outputLimitNote must not drift from it.
const wantOutputLimitNote = "[Kiro stopped at the output limit before producing an answer]"

func TestOutputLimitNoteText(t *testing.T) {
	if outputLimitNote != wantOutputLimitNote {
		t.Fatalf("outputLimitNote = %q, want %q", outputLimitNote, wantOutputLimitNote)
	}
}

// reasoningThenMaxTokens is what the live backend sent when the model spent
// its whole output budget on reasoning: reasoning, a signature, the stop
// reason, bookkeeping, and no content at all.
func reasoningThenMaxTokens(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write(cwFrame(`{"text":"First I need to "}`))
	w.Write(cwFrame(`{"text":"read the whole file."}`))
	w.Write(cwFrame(`{"signature":"EqQBCkYIBxgCKkDx"}`))
	w.Write(cwFrame(`{"contextUsagePercentage":12.9}`))
	w.Write(cwFrame(`{"stopReason":"MAX_TOKENS"}`))
	w.Write(cwFrame(`{"unit":"credit","unitPlural":"credits","usage":0.86}`))
}

const reasoningChars = len("First I need to ") + len("read the whole file.")

func loggedLine(lg *logger.Logger, wantType logger.LogType, parts ...string) bool {
	for _, e := range lg.GetEntries() {
		if e.Type != wantType {
			continue
		}
		all := true
		for _, p := range parts {
			if !strings.Contains(e.Body, p) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func TestStreamReportsMaxTokensWhenTheBackendOnlyReasoned(t *testing.T) {
	streamTestSetup(t, reasoningThenMaxTokens)

	rec := newLiveRecorder()
	lg := logger.NewLogger(50)
	preview := handleStreamRequestWithLogger(rec, testRequest(), lg, "sess", "req", nil)

	body := rec.String()
	for _, want := range []string{"event: message_start", outputLimitNote, `"stop_reason":"max_tokens"`, "event: message_stop"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %q:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"No response received", "First I need", "signature", `"stop_reason":"end_turn"`} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("body has %q:\n%s", unwanted, body)
		}
	}
	if preview != outputLimitNote {
		t.Fatalf("response preview = %q, want the note", preview)
	}
	if !loggedLine(lg, logger.LogTypeErr, "MAX_TOKENS", "reasoningChars="+strconv.Itoa(reasoningChars)) {
		t.Fatalf("no error line with the stop reason and reasoningChars=%d; entries: %+v", reasoningChars, lg.GetEntries())
	}
}

func TestNonStreamReportsMaxTokensWhenTheBackendOnlyReasoned(t *testing.T) {
	streamTestSetup(t, reasoningThenMaxTokens)

	rec := httptest.NewRecorder()
	lg := logger.NewLogger(50)
	handleNonStreamRequest(rec, testRequest(), lg, "sess", "req")

	var resp struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if resp.StopReason != "max_tokens" {
		t.Fatalf("stop_reason = %q, want max_tokens; body: %s", resp.StopReason, rec.Body.String())
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" || resp.Content[0].Text != outputLimitNote {
		t.Fatalf("content = %+v, want one text block with the note", resp.Content)
	}
	if !loggedLine(lg, logger.LogTypeErr, "MAX_TOKENS", "reasoningChars="+strconv.Itoa(reasoningChars)) {
		t.Fatalf("no error line with the stop reason and reasoningChars=%d; entries: %+v", reasoningChars, lg.GetEntries())
	}
}

// The backend's stop reason reaches the client when there is content too, and
// the Parser log line carries it with the reasoning size.
func TestStreamForwardsTheBackendStopReason(t *testing.T) {
	streamTestSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(cwFrame(`{"text":"Short plan."}`))
		w.Write(cwFrame(`{"content":"Partial answer"}`))
		w.Write(cwFrame(`{"stopReason":"MAX_TOKENS"}`))
	})

	rec := newLiveRecorder()
	lg := logger.NewLogger(50)
	handleStreamRequestWithLogger(rec, testRequest(), lg, "sess", "req", nil)

	body := rec.String()
	if !strings.Contains(body, "Partial answer") || !strings.Contains(body, `"stop_reason":"max_tokens"`) {
		t.Fatalf("want the content and stop_reason max_tokens:\n%s", body)
	}
	if strings.Contains(body, outputLimitNote) {
		t.Fatalf("the note is only for a response with no content:\n%s", body)
	}
	if !loggedLine(lg, logger.LogTypeInf, "Parser:", "stopReason=MAX_TOKENS", "reasoningChars=11") {
		t.Fatalf("no Parser line with stopReason and reasoningChars; entries: %+v", lg.GetEntries())
	}
}

func TestNonStreamForwardsTheBackendStopReason(t *testing.T) {
	streamTestSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(cwFrame(`{"content":"Running it."}`))
		w.Write(cwFrame(`{"name":"Bash","toolUseId":"t1"}`))
		w.Write(cwFrame(`{"name":"Bash","toolUseId":"t1","input":"{\"command\":\"ls\"}"}`))
		w.Write(cwFrame(`{"name":"Bash","toolUseId":"t1","stop":true}`))
		w.Write(cwFrame(`{"stopReason":"TOOL_USE"}`))
	})

	rec := httptest.NewRecorder()
	lg := logger.NewLogger(50)
	handleNonStreamRequest(rec, testRequest(), lg, "sess", "req")

	if !strings.Contains(rec.Body.String(), `"stop_reason":"tool_use"`) {
		t.Fatalf("want stop_reason tool_use: %s", rec.Body.String())
	}
	if !loggedLine(lg, logger.LogTypeInf, "Parser", "stopReason=TOOL_USE", "reasoningChars=0") {
		t.Fatalf("no Parser line with stopReason and reasoningChars; entries: %+v", lg.GetEntries())
	}
}
