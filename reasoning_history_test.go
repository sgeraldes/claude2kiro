package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/reasoning"
	"github.com/sgeraldes/claude2kiro/parser"
)

const reasoningTestModel = "claude-sonnet-4-5"

// withReasoningStore gives the test an empty process-wide reasoning store.
func withReasoningStore(t *testing.T) {
	t.Helper()
	orig := reasoningStore
	reasoningStore = reasoning.NewStore(reasoning.DefaultMaxBytes)
	t.Cleanup(func() { reasoningStore = orig })
}

// withReasoningConfig sets the config the request builder reads, with the
// given reasoning cap and tool and history modes.
func withReasoningConfig(t *testing.T, maxBytes int, toolMode, historyMode string) {
	t.Helper()
	withOfflineCatalog(t)
	cfg := config.Default()
	cfg.Advanced.ReasoningHistoryMaxBytes = maxBytes
	cfg.Advanced.ToolMode = toolMode
	cfg.Advanced.HistoryMode = historyMode
	cfg.Advanced.HistoryRecentTurns = 1
	withConfig(t, cfg)
}

// rememberResponse runs a backend response through the non-stream translator
// and keeps its reasoning, as the handlers do after a successful answer.
func rememberResponse(t *testing.T, kiroModel string, payloads ...string) {
	t.Helper()
	rememberResponseIn(t, kiroModel, reasoningScope{}, payloads...)
}

// rememberResponseIn is rememberResponse for the turn at scope.
func rememberResponseIn(t *testing.T, kiroModel string, scope reasoningScope, payloads ...string) {
	t.Helper()
	var raw bytes.Buffer
	for _, p := range payloads {
		raw.Write(cwFrame(p))
	}
	_, summary := parser.ParseEventsWithSummary(raw.Bytes())
	rememberReasoning(kiroModel, scope, summary.Reasoning, summary.ToolUseIDs, summary.Text)
}

// toolTurnRequest is the request that follows an answer with a tool call.
func toolTurnRequest(model, toolID string) AnthropicRequest {
	return AnthropicRequest{
		Model:     model,
		MaxTokens: 64,
		Messages: []AnthropicRequestMessage{
			{Role: "user", Content: "read the file"},
			{Role: "assistant", Content: []any{map[string]any{"type": "text", "text": "Reading it."}, toolUseBlock(toolID)}},
			{Role: "user", Content: []any{toolResultBlock(toolID, "file body")}},
		},
	}
}

// historyAssistants returns the history's assistant messages, oldest first.
func historyAssistants(cw CodeWhispererRequest) []HistoryAssistantMessage {
	var out []HistoryAssistantMessage
	for _, h := range cw.ConversationState.History {
		if a, ok := h.(HistoryAssistantMessage); ok {
			out = append(out, a)
		}
	}
	return out
}

// reasoningOf decodes the marshalled reasoningContent of every assistant
// history entry that has one, oldest first.
func reasoningOf(t *testing.T, cw CodeWhispererRequest) []map[string]any {
	t.Helper()
	var decoded struct {
		ConversationState struct {
			History []map[string]map[string]any `json:"history"`
		} `json:"conversationState"`
	}
	if err := json.Unmarshal([]byte(marshalCW(t, cw)), &decoded); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, h := range decoded.ConversationState.History {
		if a, ok := h["assistantResponseMessage"]; ok {
			if rc, ok := a["reasoningContent"].(map[string]any); ok {
				out = append(out, rc)
			}
		}
	}
	return out
}

func toolAnswer(text, sig, toolID string) []string {
	return []string{
		`{"text":"` + text + `"}`,
		`{"signature":"` + sig + `"}`,
		`{"content":"Reading it."}`,
		`{"name":"Read","toolUseId":"` + toolID + `"}`,
		`{"name":"Read","toolUseId":"` + toolID + `","input":"{}"}`,
		`{"name":"Read","toolUseId":"` + toolID + `","stop":true}`,
	}
}

func TestReasoningIsGivenBackOnTheTurnThatProducedIt(t *testing.T) {
	withReasoningStore(t)
	withReasoningConfig(t, config.DefaultReasoningHistoryMaxBytes, "full", "full")
	kiroModel := getKiroModelID(reasoningTestModel)
	rememberResponse(t, kiroModel, toolAnswer("I should read the file.", "SIG-READ", "tu_r1")...)

	cw := buildCodeWhispererRequest(toolTurnRequest(reasoningTestModel, "tu_r1"), TokenData{})
	body := marshalCW(t, cw)
	if got := strings.Count(body, `"reasoningContent"`); got != 1 {
		t.Fatalf("reasoningContent appears %d times, want once:\n%s", got, body)
	}
	rcs := reasoningOf(t, cw)
	text, _ := rcs[0]["reasoningText"].(map[string]any)
	if text["text"] != "I should read the file." || text["signature"] != "SIG-READ" || len(rcs[0]) != 1 {
		t.Fatalf("reasoningContent = %v, want exactly reasoningText{text, signature}", rcs[0])
	}
	assistants := historyAssistants(cw)
	if len(assistants) != 1 || assistants[0].AssistantResponseMessage.ReasoningContent == nil {
		t.Fatalf("want the one assistant turn to carry the reasoning: %+v", assistants)
	}
	if cw.reasoning.turns != 1 || cw.reasoning.bytes != len("I should read the file.")+len("SIG-READ") {
		t.Fatalf("attachment = %+v, want 1 turn and the text plus signature bytes", cw.reasoning)
	}
	if line := requestMetricsSummary(cw, len(body), config.Get()); !strings.Contains(line, "reasoningAttached=1 reasoningBytes=31") {
		t.Fatalf("metrics line lacks the reasoning counts: %s", line)
	}
}

func TestRedactedReasoningIsGivenBackAsItArrived(t *testing.T) {
	withReasoningStore(t)
	withReasoningConfig(t, config.DefaultReasoningHistoryMaxBytes, "full", "full")
	kiroModel := getKiroModelID(reasoningTestModel)
	// A turn without a tool call is found by its session, position and text.
	rememberResponseIn(t, kiroModel, reasoningScope{session: scopeSessionA}, `{"redactedContent":"UkVEQUNURUQ="}`, `{"content":"The answer is 42."}`)

	req := inSession(AnthropicRequest{Model: reasoningTestModel, MaxTokens: 64, Messages: []AnthropicRequestMessage{
		{Role: "user", Content: "question"},
		{Role: "assistant", Content: []any{map[string]any{"type": "text", "text": "The answer is 42."}}},
		{Role: "user", Content: "why?"},
	}}, scopeSessionA)
	cw := buildCodeWhispererRequest(req, TokenData{})
	rcs := reasoningOf(t, cw)
	if len(rcs) != 1 || rcs[0]["redactedContent"] != "UkVEQUNURUQ=" || len(rcs[0]) != 1 {
		t.Fatalf("reasoningContent = %v, want exactly redactedContent as it arrived", rcs)
	}
}

func TestReasoningOfAnotherModelIsNotGivenBack(t *testing.T) {
	withReasoningStore(t)
	withReasoningConfig(t, config.DefaultReasoningHistoryMaxBytes, "full", "full")
	rememberResponse(t, "some-other-kiro-model", toolAnswer("plan", "SIG", "tu_m1")...)

	cw := buildCodeWhispererRequest(toolTurnRequest(reasoningTestModel, "tu_m1"), TokenData{})
	if body := marshalCW(t, cw); strings.Contains(body, "reasoningContent") {
		t.Fatalf("reasoning of another model was attached:\n%s", body)
	}
}

func TestNoStoredReasoningMeansNoReasoningContentKey(t *testing.T) {
	withReasoningStore(t)
	withReasoningConfig(t, config.DefaultReasoningHistoryMaxBytes, "full", "full")

	cw := buildCodeWhispererRequest(toolTurnRequest(reasoningTestModel, "tu_none"), TokenData{})
	body := marshalCW(t, cw)
	if strings.Contains(body, "reasoningContent") {
		t.Fatalf("a request without stored reasoning has a reasoningContent key:\n%s", body)
	}
	if line := requestMetricsSummary(cw, len(body), config.Get()); !strings.Contains(line, "reasoningAttached=0 reasoningBytes=0") {
		t.Fatalf("metrics line lacks zero reasoning counts: %s", line)
	}
}

func TestUnsignedReasoningIsNeitherKeptNorGivenBack(t *testing.T) {
	withReasoningStore(t)
	withReasoningConfig(t, config.DefaultReasoningHistoryMaxBytes, "full", "full")
	rememberResponse(t, getKiroModelID(reasoningTestModel), `{"text":"unsigned"}`, `{"name":"Read","toolUseId":"tu_u1"}`)
	if reasoningStore.Len() != 0 {
		t.Fatal("unsigned reasoning must not be kept")
	}
}

// manyTurnsRequest has one assistant tool turn per id, oldest first.
func manyTurnsRequest(ids ...string) AnthropicRequest {
	msgs := []AnthropicRequestMessage{{Role: "user", Content: "start"}}
	for _, id := range ids {
		msgs = append(msgs,
			AnthropicRequestMessage{Role: "assistant", Content: []any{toolUseBlock(id)}},
			AnthropicRequestMessage{Role: "user", Content: []any{toolResultBlock(id, "ok")}})
	}
	return AnthropicRequest{Model: reasoningTestModel, MaxTokens: 64, Messages: msgs}
}

func TestReasoningCapKeepsTheNewestTurns(t *testing.T) {
	withReasoningStore(t)
	const turnText, turnSig = "0123456789", "SIGNATURE0"
	turnBytes := len(turnText) + len(turnSig)
	withReasoningConfig(t, 2*turnBytes+turnBytes/2, "full", "full")
	kiroModel := getKiroModelID(reasoningTestModel)
	for _, id := range []string{"tu_c1", "tu_c2", "tu_c3"} {
		rememberResponse(t, kiroModel, toolAnswer(turnText, turnSig, id)...)
	}

	cw := buildCodeWhispererRequest(manyTurnsRequest("tu_c1", "tu_c2", "tu_c3"), TokenData{})
	assistants := historyAssistants(cw)
	if len(assistants) != 3 {
		t.Fatalf("history has %d assistant turns, want 3", len(assistants))
	}
	if assistants[0].AssistantResponseMessage.ReasoningContent != nil {
		t.Fatal("the oldest turn must not fit under the cap")
	}
	for i := 1; i < 3; i++ {
		if assistants[i].AssistantResponseMessage.ReasoningContent == nil {
			t.Fatalf("newer turn %d must carry its reasoning", i)
		}
	}
	if cw.reasoning.turns != 2 || cw.reasoning.bytes != 2*turnBytes {
		t.Fatalf("attachment = %+v, want 2 turns of %d bytes", cw.reasoning, turnBytes)
	}
}

// A turn whose reasoning alone exceeds the room left is skipped; older turns
// that fit still get theirs.
func TestReasoningCapSkipsAnOversizedTurnAndKeepsOlderOnes(t *testing.T) {
	withReasoningStore(t)
	withReasoningConfig(t, 100, "full", "full")
	kiroModel := getKiroModelID(reasoningTestModel)
	rememberResponse(t, kiroModel, toolAnswer("small", "SIG-S", "tu_old")...)
	rememberResponse(t, kiroModel, toolAnswer(strings.Repeat("x", 200), "SIG-BIG", "tu_new")...)

	cw := buildCodeWhispererRequest(manyTurnsRequest("tu_old", "tu_new"), TokenData{})
	assistants := historyAssistants(cw)
	if len(assistants) != 2 {
		t.Fatalf("history has %d assistant turns, want 2", len(assistants))
	}
	if assistants[1].AssistantResponseMessage.ReasoningContent != nil {
		t.Fatal("the newest turn's reasoning is over the cap and must not be attached")
	}
	if assistants[0].AssistantResponseMessage.ReasoningContent == nil {
		t.Fatal("the older turn fits and must carry its reasoning")
	}
	if cw.reasoning.turns != 1 || cw.reasoning.bytes != len("small")+len("SIG-S") {
		t.Fatalf("attachment = %+v, want only the small turn", cw.reasoning)
	}
}

func TestReasoningCapZeroTurnsTheFeatureOff(t *testing.T) {
	withReasoningStore(t)
	withReasoningConfig(t, config.DefaultReasoningHistoryMaxBytes, "full", "full")
	kiroModel := getKiroModelID(reasoningTestModel)
	rememberResponse(t, kiroModel, toolAnswer("plan", "SIG", "tu_z1")...)

	withReasoningConfig(t, 0, "full", "full")
	cw := buildCodeWhispererRequest(toolTurnRequest(reasoningTestModel, "tu_z1"), TokenData{})
	if body := marshalCW(t, cw); strings.Contains(body, "reasoningContent") {
		t.Fatalf("setting 0 still attached reasoning:\n%s", body)
	}
	rememberResponse(t, kiroModel, toolAnswer("plan 2", "SIG2", "tu_z2")...)
	if reasoningStore.Len() != 1 {
		t.Fatalf("setting 0 still kept reasoning: %d entries", reasoningStore.Len())
	}
}

// In none_text mode the history carries tool calls as text, which is not what
// the reasoning was signed with, so nothing is attached.
func TestNoneTextToolModeGivesNoReasoningBack(t *testing.T) {
	withReasoningStore(t)
	withReasoningConfig(t, config.DefaultReasoningHistoryMaxBytes, "none_text", "full")
	rememberResponse(t, getKiroModelID(reasoningTestModel), toolAnswer("plan", "SIG", "tu_n1")...)

	cw := buildCodeWhispererRequest(toolTurnRequest(reasoningTestModel, "tu_n1"), TokenData{})
	if body := marshalCW(t, cw); strings.Contains(body, "reasoningContent") {
		t.Fatalf("none_text attached reasoning:\n%s", body)
	}
}

// A history mode that drops a turn drops its reasoning with it, and the cap
// counts only what is sent.
func TestDroppedHistoryTurnsCarryNoReasoning(t *testing.T) {
	withReasoningStore(t)
	withReasoningConfig(t, config.DefaultReasoningHistoryMaxBytes, "full", "recent")
	kiroModel := getKiroModelID(reasoningTestModel)
	// Text-only turns: tool turns chained by their results are never dropped.
	msgs := []AnthropicRequestMessage{}
	for i, n := range []string{"d1", "d2", "d3"} {
		rememberResponseIn(t, kiroModel, reasoningScope{session: scopeSessionA, turn: i},
			`{"text":"plan `+n+`"}`, `{"signature":"SIG-`+n+`"}`, `{"content":"answer `+n+`"}`)
		msgs = append(msgs,
			AnthropicRequestMessage{Role: "user", Content: "question " + n},
			AnthropicRequestMessage{Role: "assistant", Content: "answer " + n})
	}
	msgs = append(msgs, AnthropicRequestMessage{Role: "user", Content: "next"})

	cw := buildCodeWhispererRequest(inSession(AnthropicRequest{Model: reasoningTestModel, MaxTokens: 64, Messages: msgs}, scopeSessionA), TokenData{})
	body := marshalCW(t, cw)
	if cw.reasoning.turns == 0 || !strings.Contains(body, "plan d3") {
		t.Fatalf("the kept newest turn must carry its reasoning:\n%s", body)
	}
	attached := 0
	for _, a := range historyAssistants(cw) {
		if a.AssistantResponseMessage.ReasoningContent != nil {
			attached++
		}
	}
	if attached != cw.reasoning.turns || attached != strings.Count(body, `"reasoningContent"`) {
		t.Fatalf("attached %d, counted %d, body has %d", attached, cw.reasoning.turns, strings.Count(body, `"reasoningContent"`))
	}
	if len(historyAssistants(cw)) >= 3 {
		t.Fatalf("history mode recent kept every turn; the test needs one dropped")
	}
	if strings.Contains(body, "plan d1") {
		t.Fatalf("reasoning of a dropped turn was sent:\n%s", body)
	}
}
