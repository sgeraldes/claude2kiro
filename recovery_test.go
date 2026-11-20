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
	frameText      = `{"content":"Here is the plan. "}`
)

// withRecovery sets the idle cut threshold and the recovery switch on top of
// the current config, so call it after the backend setup.
func withRecovery(t *testing.T, on bool) {
	t.Helper()
	cfg := *config.Get()
	cfg.Network.BackendIdleCutAfter = testIdleCutAfter
	cfg.Network.StreamKeepaliveInterval = 0
	cfg.Advanced.InterruptedToolRecovery = on
	withConfig(t, &cfg)
}

// withRecoveredStore gives the test an empty store of recovered exchanges.
func withRecoveredStore(t *testing.T) {
	t.Helper()
	orig := recoveredTurns
	recoveredTurns = newRecoveredStore(256, recoveredMaxBytes, 6*time.Hour)
	t.Cleanup(func() { recoveredTurns = orig })
}

// silentCut answers 200, sends frames, stays silent and drops the connection:
// Kiro's idle cut.
func silentCut(w http.ResponseWriter, frames ...string) {
	w.WriteHeader(http.StatusOK)
	for _, f := range frames {
		w.Write(cwFrame(f))
	}
	w.(http.Flusher).Flush()
	time.Sleep(testIdleSilence)
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		panic(err)
	}
	conn.Close()
}

type step func(http.ResponseWriter)

func cutAfter(frames ...string) step {
	return func(w http.ResponseWriter) { silentCut(w, frames...) }
}

func answerWith(frames ...string) step {
	return func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusOK)
		for _, f := range frames {
			w.Write(cwFrame(f))
		}
	}
}

var writtenAnswer = answerWith(`{"content":"The file is written."}`, `{"stopReason":"END_TURN"}`)

// stepBackend answers its n-th request (from 0) with steps[n] and every later
// one with the last step, recording the bodies.
func stepBackend(log *bodyLog, steps ...step) (http.HandlerFunc, *int32) {
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

func linesOf(lg *logger.Logger, typ logger.LogType) string {
	var out strings.Builder
	for _, e := range lg.GetEntries() {
		if e.Type == typ {
			out.WriteString(e.Body + "\n")
		}
	}
	return out.String()
}

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

// cwHistory decodes the parts of a CodeWhisperer request body the history
// checks need.
type cwHistory struct {
	ConversationState struct {
		ConversationID string `json:"conversationId"`
		CurrentMessage struct {
			UserInputMessage struct {
				Content                 string `json:"content"`
				UserInputMessageContext struct {
					ToolResults []struct {
						ToolUseID string `json:"toolUseId"`
						Status    string `json:"status"`
						Content   []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"toolResults"`
				} `json:"userInputMessageContext"`
			} `json:"userInputMessage"`
		} `json:"currentMessage"`
		History []struct {
			UserInputMessage *struct {
				Content                 string `json:"content"`
				UserInputMessageContext *struct {
					ToolResults []struct {
						ToolUseID string `json:"toolUseId"`
					} `json:"toolResults"`
				} `json:"userInputMessageContext"`
			} `json:"userInputMessage"`
			AssistantResponseMessage *struct {
				Content  string `json:"content"`
				ToolUses []struct {
					ToolUseID string         `json:"toolUseId"`
					Name      string         `json:"name"`
					Input     map[string]any `json:"input"`
				} `json:"toolUses"`
			} `json:"assistantResponseMessage"`
		} `json:"history"`
	} `json:"conversationState"`
}

func decodeCW(t *testing.T, body string) cwHistory {
	t.Helper()
	var h cwHistory
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		t.Fatalf("backend body is not JSON: %v", err)
	}
	return h
}

// assertWellFormed checks that every tool result, in the history and in the
// current message, answers a tool use an earlier assistant turn made: Kiro
// rejects anything else as "Improperly formed request".
func assertWellFormed(t *testing.T, h cwHistory) {
	t.Helper()
	used := map[string]bool{}
	for i, entry := range h.ConversationState.History {
		if a := entry.AssistantResponseMessage; a != nil {
			for _, tu := range a.ToolUses {
				used[tu.ToolUseID] = true
			}
		}
		if u := entry.UserInputMessage; u != nil && u.UserInputMessageContext != nil {
			for _, tr := range u.UserInputMessageContext.ToolResults {
				if !used[tr.ToolUseID] {
					t.Fatalf("history entry %d answers %s, which no earlier assistant turn used", i, tr.ToolUseID)
				}
			}
		}
	}
	for _, tr := range h.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.ToolResults {
		if !used[tr.ToolUseID] {
			t.Fatalf("the current message answers %s, which no assistant turn used", tr.ToolUseID)
		}
	}
}

// Path 1: a cut before any tool frame and before any text sends the same
// request again; the client only sees the answer of the resend.
func TestStreamCutBeforeContentIsResentUnchanged(t *testing.T) {
	withRecoveredStore(t)
	log := &bodyLog{}
	backend, n := stepBackend(log, cutAfter(frameReasoning), writtenAnswer)
	streamTestSetup(t, backend)
	withRecovery(t, true)
	lg := logger.NewLogger(100)

	body := runStream(t, testRequest(), lg)

	if *n != 2 {
		t.Fatalf("backend saw %d requests, want 2", *n)
	}
	bodies := log.all()
	if bodies[0] != bodies[1] {
		t.Fatal("path 1 must resend the same request")
	}
	assertStreamEndedCleanly(t, body, "end_turn")
	if !strings.Contains(body, "The file is written.") {
		t.Fatalf("the client must get the resend's answer; body:\n%s", body)
	}
	if w := linesOf(lg, logger.LogTypeWrn); !strings.Contains(w, "path 1") || !strings.Contains(w, "recovery 1 of 3") || !strings.Contains(w, "convId=") || !strings.Contains(w, "silent") {
		t.Fatalf("want a WRN line for path 1; got: %s", w)
	}
}

// Text already reached the client, no tool frame: the request goes again with
// that text as an assistant turn and Kiro's request to continue; the client
// gets one message with both parts.
func TestStreamCutAfterTextContinuesFromIt(t *testing.T) {
	withRecoveredStore(t)
	log := &bodyLog{}
	backend, n := stepBackend(log, cutAfter(frameText), writtenAnswer)
	streamTestSetup(t, backend)
	withRecovery(t, true)
	lg := logger.NewLogger(100)

	body := runStream(t, testRequest(), lg)

	if *n != 2 {
		t.Fatalf("backend saw %d requests, want 2", *n)
	}
	assertStreamEndedCleanly(t, body, "end_turn")
	if !strings.Contains(body, "Here is the plan.") || !strings.Contains(body, "The file is written.") || strings.Count(body, "event: message_start") != 1 {
		t.Fatalf("one message with both parts; body:\n%s", body)
	}
	second := decodeCW(t, log.all()[1])
	if second.ConversationState.CurrentMessage.UserInputMessage.Content != continueAfterInterruption {
		t.Fatalf("the resend must ask to continue, got %q", second.ConversationState.CurrentMessage.UserInputMessage.Content)
	}
	if first := decodeCW(t, log.all()[0]); first.ConversationState.ConversationID != second.ConversationState.ConversationID {
		t.Fatal("the recovery must stay in the same conversation")
	}
	if w := linesOf(lg, logger.LogTypeWrn); !strings.Contains(w, "path 2") || !strings.Contains(w, "continuing") {
		t.Fatalf("want a WRN line for path 2; got: %s", w)
	}
}

// A cut after a tool frame: the client never sees the incomplete tool call.
// Kiro gets the attempt back with a failed tool result and asks again; the
// client gets the new answer as the rest of the same message.
func TestStreamCutInAToolCallIsAnsweredAndAskedAgain(t *testing.T) {
	withRecoveredStore(t)
	log := &bodyLog{}
	next := answerWith(`{"content":"Writing it in parts."}`,
		`{"name":"Write","toolUseId":"tu_part1","input":"{\"file_path\": \"big.html\", \"content\": \"<html>\"}"}`,
		`{"name":"Write","toolUseId":"tu_part1","stop":true}`,
		`{"stopReason":"TOOL_USE"}`)
	backend, n := stepBackend(log, cutAfter(frameText, frameToolStart), next)
	streamTestSetup(t, backend)
	withRecovery(t, true)
	lg := logger.NewLogger(100)

	body := runStream(t, testRequest(), lg)

	if *n != 2 {
		t.Fatalf("backend saw %d requests, want 2", *n)
	}
	if strings.Contains(body, "tu_big") {
		t.Fatalf("the interrupted tool call must never reach the client; body:\n%s", body)
	}
	assertStreamEndedCleanly(t, body, "tool_use")
	if !strings.Contains(body, "tu_part1") || !strings.Contains(body, "Here is the plan.") {
		t.Fatalf("the client must get the first text and the new tool call; body:\n%s", body)
	}
	second := decodeCW(t, log.all()[1])
	assertWellFormed(t, second)
	results := second.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.ToolResults
	if len(results) != 1 || results[0].ToolUseID != "tu_big" || results[0].Status != "error" || !strings.Contains(results[0].Content[0].Text, "did not write anything") {
		t.Fatalf("want one failed result for tu_big, got %+v", results)
	}
	if w := linesOf(lg, logger.LogTypeWrn); !strings.Contains(w, "path 2") || !strings.Contains(w, "tu_big") {
		t.Fatalf("want a WRN line for path 2 naming the call; got: %s", w)
	}

	// The client runs tu_part1 and comes back. Kiro must see the hidden
	// exchange in the history, well formed, before the client's message.
	followUp := testRequest()
	followUp.Messages = append(followUp.Messages,
		AnthropicRequestMessage{Role: "assistant", Content: []any{
			map[string]any{"type": "text", "text": "Here is the plan. "},
			map[string]any{"type": "text", "text": "Writing it in parts."},
			map[string]any{"type": "tool_use", "id": "tu_part1", "name": "Write", "input": map[string]any{"file_path": "big.html", "content": "<html>"}},
		}},
		AnthropicRequestMessage{Role: "user", Content: []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_part1", "content": "File created successfully"},
		}})
	runStream(t, followUp, lg)
	third := decodeCW(t, log.all()[2])
	assertWellFormed(t, third)
	raw, _ := json.Marshal(third.ConversationState.History)
	if !strings.Contains(string(raw), "tu_big") || !strings.Contains(log.all()[2], "did not write anything") {
		t.Fatalf("the hidden exchange is missing from the next request's history:\n%s", raw)
	}
	if strings.Count(string(raw), "Here is the plan.") != 1 {
		t.Fatalf("the first text must appear once, in the hidden turn:\n%s", raw)
	}
	if !loggedLine(lg, logger.LogTypeInf, "Request convId=", "recoveredTurns=1") {
		t.Fatalf("the request line must count the recovered exchange; entries: %+v", lg.GetEntries())
	}
}

// A large Write cut midway gets Kiro's split guidance with its path.
func TestInterruptedLargeWriteGetsTheSplitGuidance(t *testing.T) {
	tool := &heldTool{id: "tu_big", name: "Write"}
	tool.input.WriteString(`{"file_path": "G:\\out\\big.html", "content": "` + strings.Repeat("x", 2500))
	text := interruptedResultText(tool, false, false)
	for _, want := range []string{`G:\out\big.html`, "did not write anything", "at most about 50 lines", "Edit"} {
		if !strings.Contains(text, want) {
			t.Fatalf("result text lacks %q: %s", want, text)
		}
	}
	if small := interruptedResultText(&heldTool{name: "Write"}, false, false); small != interruptedGenericResult {
		t.Fatalf("a small cut call gets the generic text, got %s", small)
	}
	if trunc := interruptedResultText(&heldTool{name: "Bash"}, true, false); !strings.Contains(trunc, "output token limit") {
		t.Fatalf("a truncated call gets the truncation text, got %s", trunc)
	}
}

// MAX_TOKENS with a tool call still open: the truncated call never reaches the
// client; Kiro is told why and asked again.
func TestStreamMaxTokensInAToolCallIsAnsweredAndAskedAgain(t *testing.T) {
	withRecoveredStore(t)
	log := &bodyLog{}
	backend, n := stepBackend(log, answerWith(frameToolStart, `{"stopReason":"MAX_TOKENS"}`), writtenAnswer)
	streamTestSetup(t, backend)
	withRecovery(t, true)
	lg := logger.NewLogger(100)

	body := runStream(t, testRequest(), lg)

	if *n != 2 || strings.Contains(body, "tu_big") {
		t.Fatalf("want 2 requests and no truncated call; got %d; body:\n%s", *n, body)
	}
	assertStreamEndedCleanly(t, body, "end_turn")
	results := decodeCW(t, log.all()[1]).ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.ToolResults
	if len(results) != 1 || !strings.Contains(results[0].Content[0].Text, "output token limit") {
		t.Fatalf("want the truncation result, got %+v", results)
	}
	if w := linesOf(lg, logger.LogTypeWrn); !strings.Contains(w, "MAX_TOKENS") {
		t.Fatalf("want a WRN line naming MAX_TOKENS; got: %s", w)
	}
}

// At most three recoveries per response, then a non-retryable error.
func TestStreamRecoveryStopsAfterThree(t *testing.T) {
	withRecoveredStore(t)
	log := &bodyLog{}
	backend, n := stepBackend(log, cutAfter(frameToolStart))
	streamTestSetup(t, backend)
	withRecovery(t, true)
	lg := logger.NewLogger(100)

	body := runStream(t, testRequest(), lg)

	if *n != 4 {
		t.Fatalf("backend saw %d requests, want 4 (the first and three recoveries)", *n)
	}
	if !strings.Contains(body, "event: error") || !strings.Contains(body, `"invalid_request_error"`) || strings.Contains(body, "tu_big") {
		t.Fatalf("want a non-retryable error and no tool call; body:\n%s", body)
	}
	if e := linesOf(lg, logger.LogTypeErr); !strings.Contains(e, "path 3") || !strings.Contains(e, "3 recoveries") {
		t.Fatalf("want an ERR line for path 3; got: %s", e)
	}
}

// With the switch off an idle-cut response fails at once, as an error the
// client does not resend, and no recovery request goes out.
func TestStreamRecoveryOffFails(t *testing.T) {
	withRecoveredStore(t)
	log := &bodyLog{}
	backend, n := stepBackend(log, cutAfter(frameToolStart))
	streamTestSetup(t, backend)
	withRecovery(t, false)
	lg := logger.NewLogger(100)

	body := runStream(t, testRequest(), lg)

	if *n != 1 {
		t.Fatalf("backend saw %d requests, want 1", *n)
	}
	// An idle cut would only be cut again if resent: not retryable, with the
	// same message as the non-streaming path.
	if !strings.Contains(body, "event: error") || !strings.Contains(body, `"invalid_request_error"`) || !strings.Contains(body, "240 s") {
		t.Fatalf("want a non-retryable idle cut error; body: %s", body)
	}
	if e := linesOf(lg, logger.LogTypeErr); !strings.Contains(e, "path 3") || !strings.Contains(e, "recovery is off") {
		t.Fatalf("want an ERR line for path 3; got: %s", e)
	}
}

// Tool calls are held until the response ends; text streams at once.
func TestStreamHoldsToolCallsUntilTheResponseEnds(t *testing.T) {
	release := make(chan struct{})
	streamTestSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(cwFrame(`{"content":"FIRST"}`))
		w.Write(cwFrame(`{"name":"Bash","toolUseId":"tu_held","input":"{\"command\": \"ls\"}"}`))
		w.Write(cwFrame(`{"name":"Bash","toolUseId":"tu_held","stop":true}`))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		w.Write(cwFrame(`{"stopReason":"TOOL_USE"}`))
	})
	withRecovery(t, true)

	rec := newLiveRecorder()
	done := make(chan struct{})
	go func() {
		handleStreamRequestWithLogger(rec, testRequest(), logger.NewLogger(50), "sess", "req", nil)
		close(done)
	}()
	waitForBody(t, rec, "FIRST")
	if strings.Contains(rec.String(), "tu_held") {
		t.Fatalf("the tool call went out before the response ended:\n%s", rec.String())
	}
	close(release)
	<-done
	body := rec.String()
	assertStreamEndedCleanly(t, body, "tool_use")
	if !strings.Contains(body, "tu_held") || !strings.Contains(body, `"index":1`) {
		t.Fatalf("the tool call must follow the text as block 1; body:\n%s", body)
	}
}

// Non-streaming: the same recovery; the client gets only the new answer.
func TestNonStreamCutInAToolCallIsAnsweredAndAskedAgain(t *testing.T) {
	withRecoveredStore(t)
	log := &bodyLog{}
	backend, n := stepBackend(log, cutAfter(frameToolStart), writtenAnswer)
	streamTestSetup(t, backend)
	withRecovery(t, true)
	lg := logger.NewLogger(100)

	rec := httptest.NewRecorder()
	handleNonStreamRequest(rec, testRequest(), lg, "sess", "req")

	if *n != 2 || rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "The file is written.") || strings.Contains(rec.Body.String(), "tu_big") {
		t.Fatalf("want the new answer only, after 2 requests; got %d requests, %d %s", *n, rec.Code, rec.Body.String())
	}
	assertWellFormed(t, decodeCW(t, log.all()[1]))
	if w := linesOf(lg, logger.LogTypeWrn); !strings.Contains(w, "path 2") {
		t.Fatalf("want a WRN line for path 2; got: %s", w)
	}
}

// Non-streaming, recoveries used up: a 400, which Claude Code does not resend.
func TestNonStreamRecoveryStopsAfterThree(t *testing.T) {
	withRecoveredStore(t)
	log := &bodyLog{}
	backend, n := stepBackend(log, cutAfter(frameToolStart))
	streamTestSetup(t, backend)
	withRecovery(t, true)
	lg := logger.NewLogger(100)

	rec := httptest.NewRecorder()
	handleNonStreamRequest(rec, testRequest(), lg, "sess", "req")

	if *n != 4 || rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid_request_error"`) {
		t.Fatalf("want 400 after 4 requests; got %d requests, %d %s", *n, rec.Code, rec.Body.String())
	}
	if e := linesOf(lg, logger.LogTypeErr); !strings.Contains(e, "path 3") {
		t.Fatalf("want an ERR line for path 3; got: %s", e)
	}
}

// The store forgets entries after their expiry and keeps at most max.
func TestRecoveredStoreExpiresAndIsBounded(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := newRecoveredStore(2, 1<<20, time.Hour)
	s.now = func() time.Time { return now }
	ex := recoveredExchange{LeadTexts: []string{"x"}}
	s.put([]string{"a"}, ex)
	now = now.Add(time.Minute)
	s.put([]string{"b"}, ex)
	now = now.Add(time.Minute)
	s.put([]string{"c"}, ex)
	if _, ok := s.get([]string{"a"}); ok || s.len() != 2 {
		t.Fatalf("want the two newest, len=%d", s.len())
	}
	now = now.Add(2 * time.Hour)
	if _, ok := s.get([]string{"c"}); ok {
		t.Fatal("an expired entry must be forgotten")
	}
}

// Measured live on 2026-11-20: Kiro holds a Write's content back, so an idle
// cut leaves only its file_path (128 characters) and Kiro's 2,000-character
// rule gave the generic text; the model sent the same whole-file Write again.
// A file write cut by the idle limit gets the split guidance whatever arrived
// of it.
func TestIdleCutFileWriteGetsTheSplitGuidanceWithLittleInput(t *testing.T) {
	tool := &heldTool{id: "tu_big", name: "Write"}
	tool.input.WriteString(`{"file_path": "G:\\out\\big.html"`)
	text := interruptedResultText(tool, false, true)
	for _, want := range []string{`G:\out\big.html`, "240 seconds", "did not write anything", "at most about 50 lines", "Edit"} {
		if !strings.Contains(text, want) {
			t.Fatalf("result text lacks %q: %s", want, text)
		}
	}
	if other := interruptedResultText(&heldTool{name: "Bash"}, false, true); other != interruptedGenericResult {
		t.Fatalf("a tool that is not a file write keeps the generic text, got %s", other)
	}
}

// Entries are bounded by total bytes too: the oldest go first, and an entry
// larger than the whole bound is not kept.
func TestRecoveredStoreIsBoundedByBytes(t *testing.T) {
	big := func(n int) recoveredExchange {
		return recoveredExchange{LeadTexts: []string{strings.Repeat("x", n)}}
	}
	s := newRecoveredStore(100, 1000, time.Hour)
	s.put([]string{"a"}, big(400))
	s.put([]string{"b"}, big(400))
	s.put([]string{"c"}, big(400))
	if _, ok := s.get([]string{"a"}); ok {
		t.Fatal("the oldest entry must go when the bytes are over the bound")
	}
	if _, ok := s.get([]string{"c"}); !ok {
		t.Fatal("the newest entry must stay")
	}
	if s.bytes() > 1000 {
		t.Fatalf("store holds %d bytes, bound 1000", s.bytes())
	}
	s.put([]string{"d"}, big(5000))
	if _, ok := s.get([]string{"d"}); ok {
		t.Fatal("an entry larger than the bound must not be kept")
	}
}

// Short conversation ids are logged whole instead of panicking.
func TestShortIDNeverPanics(t *testing.T) {
	for in, want := range map[string]string{"": "", "abc": "abc", "1234567890": "12345678"} {
		if got := shortID(in); got != want {
			t.Fatalf("shortID(%q) = %q, want %q", in, got, want)
		}
	}
}

// Non-streaming: the text of an interrupted attempt stays its own block, so
// the next request's history carries it once (in the hidden turn), not twice.
func TestNonStreamRecoveredTextAppearsOnceInTheNextHistory(t *testing.T) {
	withRecoveredStore(t)
	log := &bodyLog{}
	next := answerWith(`{"content":"Writing it in parts."}`,
		`{"name":"Write","toolUseId":"tu_part1","input":"{\"file_path\": \"big.html\", \"content\": \"<html>\"}"}`,
		`{"name":"Write","toolUseId":"tu_part1","stop":true}`,
		`{"stopReason":"TOOL_USE"}`)
	backend, _ := stepBackend(log, cutAfter(frameText, frameToolStart), next, writtenAnswer)
	streamTestSetup(t, backend)
	withRecovery(t, true)
	lg := logger.NewLogger(100)

	rec := httptest.NewRecorder()
	handleNonStreamRequest(rec, testRequest(), lg, "sess", "req")
	var answer struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("answer is not JSON: %v %s", err, rec.Body.String())
	}
	var texts []string
	for _, b := range answer.Content {
		if b["type"] == "text" {
			texts = append(texts, b["text"].(string))
		}
	}
	if len(texts) != 2 || texts[0] != "Here is the plan. " || texts[1] != "Writing it in parts." {
		t.Fatalf("each attempt's text must be its own block, got %q", texts)
	}

	// Claude Code records the answer as it came and sends it back.
	followUp := testRequest()
	followUp.Messages = append(followUp.Messages,
		AnthropicRequestMessage{Role: "assistant", Content: toAnySlice(answer.Content)},
		AnthropicRequestMessage{Role: "user", Content: []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_part1", "content": "File created successfully"},
		}})
	runNonStream(t, followUp, lg)
	third := log.all()[2]
	assertWellFormed(t, decodeCW(t, third))
	if strings.Count(third, "Here is the plan.") != 1 {
		t.Fatalf("the first text must appear once in the next request:\n%s", third)
	}
}

func toAnySlice(blocks []map[string]any) []any {
	out := make([]any, len(blocks))
	for i, b := range blocks {
		out[i] = b
	}
	return out
}

// A text-only recovery (continue after the text already sent) keeps the
// model's reasoning: the turn the reasoning was stored under at the end of the
// recovered response is the turn the next request gives it back on.
func TestReasoningOfATextOnlyRecoveryIsGivenBack(t *testing.T) {
	withRecoveredStore(t)
	withReasoningStore(t)
	log := &bodyLog{}
	finalAnswer := answerWith(`{"text":"I will finish the summary."}`, `{"signature":"SIG-TEXT"}`,
		`{"content":"Summary done."}`, `{"stopReason":"END_TURN","modelId":"claude-sonnet-4.6"}`)
	backend, _ := stepBackend(log, cutAfter(frameText), finalAnswer, writtenAnswer)
	reasoningHandlerSetup(t, backend)
	withRecovery(t, true)
	lg := logger.NewLogger(100)
	session := map[string]any{"user_id": `{"session_id":"sess-text-recovery"}`}

	first := AnthropicRequest{Model: reasoningTestModel, MaxTokens: 64, Metadata: session,
		Messages: []AnthropicRequestMessage{{Role: "user", Content: "summarize the file"}}}
	body := runStream(t, first, lg)
	assertStreamEndedCleanly(t, body, "end_turn")

	next := first
	next.Messages = append(append([]AnthropicRequestMessage(nil), first.Messages...),
		AnthropicRequestMessage{Role: "assistant", Content: []any{
			map[string]any{"type": "text", "text": "Here is the plan. "},
			map[string]any{"type": "text", "text": "Summary done."},
		}},
		AnthropicRequestMessage{Role: "user", Content: "thanks, go on"})
	runStream(t, next, lg)
	third := log.all()[2]
	if !strings.Contains(third, "SIG-TEXT") {
		t.Fatalf("the reasoning of the recovered turn must be given back:\n%s", third)
	}
	if strings.Count(third, "Here is the plan.") != 1 {
		t.Fatalf("the first text must appear once:\n%s", third)
	}
}

// The credits of every attempt of a recovered stream are logged, not only the
// last one's.
func TestStreamRecoveryLogsTheCreditsOfEveryAttempt(t *testing.T) {
	withRecoveredStore(t)
	log := &bodyLog{}
	metered := func(credits string) string {
		return `{"unit":"credit","unitPlural":"credits","usage":` + credits + `}`
	}
	backend, _ := stepBackend(log,
		answerWith(frameToolStart, `{"stopReason":"MAX_TOKENS"}`, metered("1.5")),
		answerWith(`{"content":"The file is written."}`, `{"stopReason":"END_TURN"}`, metered("0.25")))
	streamTestSetup(t, backend)
	withRecovery(t, true)
	lg := logger.NewLogger(100)

	runStream(t, testRequest(), lg)

	var lines []string
	for _, e := range lg.GetEntries() {
		if strings.HasPrefix(e.Body, "Kiro metering") {
			lines = append(lines, e.Body)
		}
	}
	if len(lines) != 2 || !strings.Contains(lines[0], "1.5") || !strings.Contains(lines[1], "0.25") {
		t.Fatalf("want one metering line per attempt (1.5, then 0.25), got %q", lines)
	}
}
