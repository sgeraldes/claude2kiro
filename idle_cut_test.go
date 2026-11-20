package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// The fake backend's silence before it drops the connection, and the
// threshold the tests give the proxy: short, with the silence well above it.
const (
	testIdleCutAfter = 100 * time.Millisecond
	testIdleSilence  = 250 * time.Millisecond
)

// Frames the fake backend sends before it falls silent.
const (
	frameReasoning = `{"text":"planning the file"}`
	frameToolStart = `{"name":"Write","toolUseId":"tu_big","input":"{\"file_path\": \"big.html\""}`
	frameText      = `{"content":"Here is the plan"}`
)

// withIdleCut sets the idle cut threshold and the note switch on top of the
// current config, so call it after the backend setup.
func withIdleCut(t *testing.T, note bool) {
	t.Helper()
	cfg := *config.Get()
	cfg.Network.BackendIdleCutAfter = testIdleCutAfter
	cfg.Network.StreamKeepaliveInterval = 0
	cfg.Advanced.IdleCutNote = note
	withConfig(t, &cfg)
}

// withIdleCutCalls gives the test an empty store of cut tool calls.
func withIdleCutCalls(t *testing.T) {
	t.Helper()
	orig := idleCutCalls
	idleCutCalls = newCutCallStore(256, 6*time.Hour)
	t.Cleanup(func() { idleCutCalls = orig })
}

// silentCut answers 200, sends frames, stays silent for silence and drops the
// connection: Kiro's idle cut.
func silentCut(w http.ResponseWriter, silence time.Duration, frames ...string) {
	w.WriteHeader(http.StatusOK)
	for _, f := range frames {
		w.Write(cwFrame(f))
	}
	w.(http.Flusher).Flush()
	time.Sleep(silence)
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		panic(err)
	}
	conn.Close()
}

// answer writes a complete text answer.
func answer(w http.ResponseWriter, text string) {
	w.WriteHeader(http.StatusOK)
	w.Write(cwFrame(`{"content":"` + text + `"}`))
	w.Write(cwFrame(`{"stopReason":"END_TURN"}`))
}

// cutScriptBackend answers its n-th request (from 0) with steps[n], and every
// later one with the last step. It records the bodies.
func cutScriptBackend(log *bodyLog, steps ...func(http.ResponseWriter)) (http.HandlerFunc, *int32) {
	var n int32
	return func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		i := int(atomic.AddInt32(&n, 1)) - 1
		if i >= len(steps) {
			i = len(steps) - 1
		}
		steps[i](w)
	}, &n
}

func cutAfter(frames ...string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { silentCut(w, testIdleSilence, frames...) }
}

func answerWith(text string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { answer(w, text) }
}

func linesOf(lg *logger.Logger, typ logger.LogType) string {
	var out strings.Builder
	for _, e := range lg.GetEntries() {
		if e.Type == typ {
			out.WriteString(e.Body + "\n")
		}
	}
	return out.String()
}

// toolInputs assembles the input_json_delta fragments a stream sent per
// content block index.
func toolInputs(t *testing.T, body string) map[float64]string {
	t.Helper()
	inputs := map[float64]string{}
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev map[string]any
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		if delta, ok := ev["delta"].(map[string]any); ok && delta["type"] == "input_json_delta" {
			idx, _ := ev["index"].(float64)
			inputs[idx] += delta["partial_json"].(string)
		}
	}
	return inputs
}

// assertStreamEndedCleanly checks a stream that ends as a normal message with
// the given stop reason and no error.
func assertStreamEndedCleanly(t *testing.T, body, stopReason string) {
	t.Helper()
	if strings.Contains(body, "event: error") {
		t.Fatalf("the stream must not fail; body:\n%s", body)
	}
	for _, want := range []string{"event: message_delta", `"stop_reason":"` + stopReason + `"`, "event: message_stop"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the stream lacks %q; body:\n%s", want, body)
		}
	}
}

func cutToolResultRequest(isError bool) AnthropicRequest {
	result := map[string]any{"type": "tool_result", "tool_use_id": "tu_big", "content": "InputValidationError: The required parameter `content` is missing"}
	if isError {
		result["is_error"] = true
	} else {
		result["content"] = "File created successfully"
	}
	return AnthropicRequest{
		Model:     "claude-sonnet-4-5",
		MaxTokens: 10,
		Messages: []AnthropicRequestMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: []any{map[string]any{"type": "tool_use", "id": "tu_big", "name": "Write", "input": map[string]any{"file_path": "big.html"}}}},
			{Role: "user", Content: []any{result}},
		},
	}
}

// Path 1, streaming: a cut before any content reached the client is resent
// once inside the proxy; the client only sees the answer of the resend.
func TestStreamIdleCutBeforeContentIsResentOnce(t *testing.T) {
	log := &bodyLog{}
	backend, n := cutScriptBackend(log, cutAfter(frameReasoning), answerWith("The file says hello."))
	streamTestSetup(t, backend)
	withIdleCut(t, true)
	lg := logger.NewLogger(100)

	body := runStream(t, testRequest(), lg)

	if *n != 2 {
		t.Fatalf("backend saw %d requests, want 2 (cut, resent)", *n)
	}
	if !strings.Contains(body, "The file says hello.") {
		t.Fatalf("the client must get the resend's answer; body:\n%s", body)
	}
	assertStreamEndedCleanly(t, body, "end_turn")
	if strings.Count(body, "event: message_start") != 1 {
		t.Fatalf("one message_start, from the resend; body:\n%s", body)
	}
	if w := linesOf(lg, logger.LogTypeWrn); !strings.Contains(w, "path 1") || !strings.Contains(w, "attempt 1") || !strings.Contains(w, "convId=") || !strings.Contains(w, "silent") {
		t.Fatalf("want a WRN line for path 1 with convId, silence and attempt; got: %s", w)
	}
}

// Path 2, streaming: a cut while a tool call was open closes the call with
// valid JSON from what arrived and ends the message as tool_use; the next
// request carries the note on that call's tool result, to Kiro only.
func TestStreamIdleCutInAToolCallClosesItAndNotesTheResult(t *testing.T) {
	withIdleCutCalls(t)
	log := &bodyLog{}
	backend, n := cutScriptBackend(log, cutAfter(frameToolStart), answerWith("Splitting it."))
	streamTestSetup(t, backend)
	withIdleCut(t, true)
	lg := logger.NewLogger(100)

	body := runStream(t, testRequest(), lg)

	if *n != 1 {
		t.Fatalf("backend saw %d requests, want 1: content already went out, nothing is resent", *n)
	}
	assertStreamEndedCleanly(t, body, "tool_use")
	if !strings.Contains(body, "event: content_block_stop") {
		t.Fatalf("the tool block must be closed; body:\n%s", body)
	}
	input := toolInputs(t, body)[0]
	var parsed map[string]any
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		t.Fatalf("the tool input the client assembled is not JSON: %q: %v", input, err)
	}
	if parsed["file_path"] != "big.html" || len(parsed) != 1 {
		t.Fatalf("the input must be what arrived, got %v", parsed)
	}
	if w := linesOf(lg, logger.LogTypeWrn); !strings.Contains(w, "path 2") || !strings.Contains(w, "tu_big") {
		t.Fatalf("want a WRN line for path 2 naming the call; got: %s", w)
	}

	// The client runs the call, it fails, and the tool result comes back.
	second := runStream(t, cutToolResultRequest(true), lg)
	if !strings.Contains(second, "Splitting it.") {
		t.Fatalf("second answer = %s", second)
	}
	sent := log.all()[1]
	if !strings.Contains(sent, "Note from the proxy") || !strings.Contains(sent, "nothing was executed") {
		t.Fatalf("the tool result sent to Kiro lacks the note:\n%s", sent)
	}
	if strings.Contains(second, "Note from the proxy") {
		t.Fatalf("the client must never see the note: %s", second)
	}
	if !loggedLine(lg, logger.LogTypeInf, "Request convId=", "idleCutNotes=1") {
		t.Fatalf("the request line must count the note; entries: %+v", lg.GetEntries())
	}
}

// A cut call whose input happened to run gets the note worded for that.
func TestIdleCutNoteForASuccessfulToolResult(t *testing.T) {
	withIdleCutCalls(t)
	withOfflineCatalog(t)
	cfg := *config.Get()
	cfg.Advanced.IdleCutNote = true
	withConfig(t, &cfg)
	idleCutCalls.Add("", "tu_big")

	cw := buildCodeWhispererRequest(cutToolResultRequest(false), TokenData{})

	results := cw.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.ToolResults
	if len(results) != 1 || len(results[0].Content) != 2 || results[0].Content[1].Text != idleCutNoteSuccess {
		t.Fatalf("want the success note after the result, got %+v", results)
	}
	if cw.idleCutNotes != 1 {
		t.Fatalf("idleCutNotes = %d, want 1", cw.idleCutNotes)
	}
}

// Later requests carry the cut call's tool result in their history; it keeps
// the note there, so the history Kiro sees does not change between requests.
func TestIdleCutNoteStaysOnTheToolResultInHistory(t *testing.T) {
	withIdleCutCalls(t)
	withOfflineCatalog(t)
	cfg := *config.Get()
	cfg.Advanced.IdleCutNote = true
	cfg.Advanced.HistoryMode = "full"
	cfg.Advanced.ToolMode = "full"
	withConfig(t, &cfg)
	idleCutCalls.Add("", "tu_big")
	req := cutToolResultRequest(true)
	req.Messages = append(req.Messages,
		AnthropicRequestMessage{Role: "assistant", Content: "I will split it."},
		AnthropicRequestMessage{Role: "user", Content: "go on"})

	cw := buildCodeWhispererRequest(req, TokenData{})

	raw, _ := json.Marshal(cw.ConversationState.History)
	if !strings.Contains(string(raw), "Note from the proxy") {
		t.Fatalf("the history's tool result lacks the note:\n%s", raw)
	}
}

// With idle_cut_note off nothing is added to any tool result.
func TestIdleCutNoteOffAddsNothing(t *testing.T) {
	withIdleCutCalls(t)
	withOfflineCatalog(t)
	cfg := *config.Get()
	cfg.Advanced.IdleCutNote = false
	withConfig(t, &cfg)
	idleCutCalls.Add("", "tu_big")

	cw := buildCodeWhispererRequest(cutToolResultRequest(true), TokenData{})

	raw, _ := json.Marshal(cw.ConversationState)
	if strings.Contains(string(raw), "Note from the proxy") || cw.idleCutNotes != 0 {
		t.Fatalf("no note with the switch off:\n%s", raw)
	}
}

// Path 2 with the switch off falls back to path 3: a retryable error, and no
// call is remembered.
func TestStreamIdleCutInAToolCallWithTheNoteOffFails(t *testing.T) {
	withIdleCutCalls(t)
	log := &bodyLog{}
	backend, _ := cutScriptBackend(log, cutAfter(frameToolStart))
	streamTestSetup(t, backend)
	withIdleCut(t, false)
	lg := logger.NewLogger(100)

	body := runStream(t, testRequest(), lg)

	assertStreamFailed(t, body)
	if e := linesOf(lg, logger.LogTypeErr); !strings.Contains(e, "path 3") || !strings.Contains(e, "silent") {
		t.Fatalf("want an ERR line for path 3 with the silence; got: %s", e)
	}
	if idleCutCalls.Len() != 0 {
		t.Fatal("no call may be remembered with the note off")
	}
}

// Path 3, streaming: a cut in plain text (no tool call open) after content
// went out is a retryable error.
func TestStreamIdleCutInTextIsRetryable(t *testing.T) {
	log := &bodyLog{}
	backend, n := cutScriptBackend(log, cutAfter(frameText))
	streamTestSetup(t, backend)
	withIdleCut(t, true)
	lg := logger.NewLogger(100)

	body := runStream(t, testRequest(), lg)

	if *n != 1 {
		t.Fatalf("backend saw %d requests, want 1", *n)
	}
	assertStreamFailed(t, body)
	if e := linesOf(lg, logger.LogTypeErr); !strings.Contains(e, "path 3") {
		t.Fatalf("want an ERR line for path 3; got: %s", e)
	}
}

// A resend that is cut too, with a tool call open, ends as path 2.
func TestStreamIdleCutTwiceEndsInPath2(t *testing.T) {
	withIdleCutCalls(t)
	log := &bodyLog{}
	backend, n := cutScriptBackend(log, cutAfter(frameReasoning), cutAfter(frameToolStart))
	streamTestSetup(t, backend)
	withIdleCut(t, true)
	lg := logger.NewLogger(100)

	body := runStream(t, testRequest(), lg)

	if *n != 2 {
		t.Fatalf("backend saw %d requests, want 2", *n)
	}
	assertStreamEndedCleanly(t, body, "tool_use")
	w := linesOf(lg, logger.LogTypeWrn)
	if !strings.Contains(w, "path 1") || !strings.Contains(w, "path 2") {
		t.Fatalf("want WRN lines for path 1 then path 2; got: %s", w)
	}
}

// Path 1, non-streaming: a cut is resent once; the client sees the answer.
func TestNonStreamIdleCutIsResentOnce(t *testing.T) {
	log := &bodyLog{}
	backend, n := cutScriptBackend(log, cutAfter(frameToolStart), answerWith("The file says hello."))
	streamTestSetup(t, backend)
	withIdleCut(t, true)
	lg := logger.NewLogger(100)

	rec := httptest.NewRecorder()
	handleNonStreamRequest(rec, testRequest(), lg, "sess", "req")

	if *n != 2 || rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "The file says hello.") {
		t.Fatalf("want the resend's answer after 2 requests; got %d requests, %d %s", *n, rec.Code, rec.Body.String())
	}
	if w := linesOf(lg, logger.LogTypeWrn); !strings.Contains(w, "path 1") || !strings.Contains(w, "attempt 1") {
		t.Fatalf("want a WRN line for path 1; got: %s", w)
	}
}

// Path 2, non-streaming: the resend is cut too with a tool call open; the
// answer closes the call with what arrived and stops as tool_use.
func TestNonStreamIdleCutTwiceInAToolCallClosesIt(t *testing.T) {
	withIdleCutCalls(t)
	log := &bodyLog{}
	backend, n := cutScriptBackend(log, cutAfter(frameToolStart))
	streamTestSetup(t, backend)
	withIdleCut(t, true)
	lg := logger.NewLogger(100)

	rec := httptest.NewRecorder()
	handleNonStreamRequest(rec, testRequest(), lg, "sess", "req")

	if *n != 2 || rec.Code != http.StatusOK {
		t.Fatalf("want 200 after 2 requests; got %d requests, %d %s", *n, rec.Code, rec.Body.String())
	}
	var got struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string         `json:"type"`
			ID    string         `json:"id"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("answer is not JSON: %v %s", err, rec.Body.String())
	}
	if got.StopReason != "tool_use" || len(got.Content) != 1 || got.Content[0].ID != "tu_big" || got.Content[0].Input["file_path"] != "big.html" {
		t.Fatalf("want the cut call with its file_path and stop_reason tool_use; got %s", rec.Body.String())
	}
	if !idleCutCalls.Has("", "tu_big") {
		t.Fatal("the cut call must be remembered for the note")
	}
	if w := linesOf(lg, logger.LogTypeWrn); !strings.Contains(w, "path 2") {
		t.Fatalf("want a WRN line for path 2; got: %s", w)
	}
}

// Path 3, non-streaming: cut twice with no tool call open (or with the note
// off) is a 400 invalid_request_error, which Claude Code does not resend.
func TestNonStreamIdleCutTwiceWithoutAToolCallIsFinal(t *testing.T) {
	for name, tc := range map[string]struct {
		frame string
		note  bool
	}{
		"text":           {frameText, true},
		"tool, note off": {frameToolStart, false},
	} {
		t.Run(name, func(t *testing.T) {
			withIdleCutCalls(t)
			log := &bodyLog{}
			backend, n := cutScriptBackend(log, cutAfter(tc.frame))
			streamTestSetup(t, backend)
			withIdleCut(t, tc.note)
			lg := logger.NewLogger(100)

			rec := httptest.NewRecorder()
			handleNonStreamRequest(rec, testRequest(), lg, "sess", "req")

			if *n != 2 || rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid_request_error"`) {
				t.Fatalf("want 400 invalid_request_error after 2 requests; got %d requests, %d %s", *n, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "240") {
				t.Fatalf("the message should name the limit: %s", rec.Body.String())
			}
			if e := linesOf(lg, logger.LogTypeErr); !strings.Contains(e, "path 3") {
				t.Fatalf("want an ERR line for path 3; got: %s", e)
			}
		})
	}
}

// The store forgets entries after their expiry and keeps at most max.
func TestCutCallStoreExpiresAndIsBounded(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := newCutCallStore(2, time.Hour)
	s.now = func() time.Time { return now }
	s.Add("scope", "a")
	now = now.Add(time.Minute)
	s.Add("scope", "b")
	now = now.Add(time.Minute)
	s.Add("scope", "c")
	if s.Len() != 2 || s.Has("scope", "a") || !s.Has("scope", "c") {
		t.Fatalf("want the two newest; a=%v b=%v c=%v len=%d", s.Has("scope", "a"), s.Has("scope", "b"), s.Has("scope", "c"), s.Len())
	}
	if s.Has("other", "c") {
		t.Fatal("another conversation must not see the entry")
	}
	now = now.Add(2 * time.Hour)
	if s.Has("scope", "c") {
		t.Fatal("an expired entry must be forgotten")
	}
}
