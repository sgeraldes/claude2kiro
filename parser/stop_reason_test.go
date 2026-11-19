package parser

import "testing"

// Frames the live backend sends besides assistant content.
const (
	reasoningFrame  = `{"text":"Let me think about this."}`
	signatureFrame  = `{"signature":"EqQBCkYIBxgCKkDx"}`
	contextFrame    = `{"contextUsagePercentage":12.9}`
	meteringFrame   = `{"unit":"credit","unitPlural":"credits","usage":0.86}`
	reasoningLength = len("Let me think about this.")
)

var stopReasonCases = []struct {
	backend string
	want    string
}{
	{"END_TURN", "end_turn"},
	{"TOOL_USE", "tool_use"},
	{"MAX_TOKENS", "max_tokens"},
	{"STOP_SEQUENCE", "stop_sequence"},
}

func stopFrame(reason string) string { return `{"stopReason":"` + reason + `"}` }

func lastStopReason(t *testing.T, events []SSEEvent) any {
	t.Helper()
	if len(events) == 0 || events[len(events)-1].Event != "message_delta" {
		t.Fatalf("events = %v, want a final message_delta", eventTypes(events))
	}
	return dataField(t, events[len(events)-1], "delta").(map[string]any)["stop_reason"]
}

func TestParseEvents_UsesTheBackendStopReason(t *testing.T) {
	for _, c := range stopReasonCases {
		t.Run(c.backend, func(t *testing.T) {
			events, summary := ParseEventsWithSummary(frames(`{"content":"Hi"}`, stopFrame(c.backend)))
			if got := lastStopReason(t, events); got != c.want {
				t.Fatalf("stop_reason = %v, want %s", got, c.want)
			}
			if summary.StopReason != c.want || summary.BackendStopReason != c.backend {
				t.Fatalf("summary = %+v, want StopReason=%s BackendStopReason=%s", summary, c.want, c.backend)
			}
		})
	}
}

func TestParseEvents_UnknownOrAbsentStopReasonKeepsTheDerivation(t *testing.T) {
	cases := map[string]struct {
		payloads []string
		want     string
	}{
		"absent, text":   {[]string{`{"content":"Hi"}`}, "end_turn"},
		"absent, tool":   {[]string{`{"name":"Bash","toolUseId":"t1"}`, `{"toolUseId":"t1","stop":true}`}, "tool_use"},
		"unknown, text":  {[]string{`{"content":"Hi"}`, stopFrame("GUARDRAIL")}, "end_turn"},
		"unknown, tool":  {[]string{`{"name":"Bash","toolUseId":"t1"}`, stopFrame("GUARDRAIL")}, "tool_use"},
		"empty, tool":    {[]string{`{"name":"Bash","toolUseId":"t1"}`, stopFrame("")}, "tool_use"},
		"lower case too": {[]string{`{"content":"Hi"}`, stopFrame("max_tokens")}, "max_tokens"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			events, summary := ParseEventsWithSummary(frames(c.payloads...))
			if got := lastStopReason(t, events); got != c.want {
				t.Fatalf("stop_reason = %v, want %s", got, c.want)
			}
			if summary.StopReason != c.want {
				t.Fatalf("summary.StopReason = %q, want %s", summary.StopReason, c.want)
			}
		})
	}
}

func TestParseEvents_ReasoningAndBookkeepingFramesProduceNoEvents(t *testing.T) {
	events, summary := ParseEventsWithSummary(frames(
		reasoningFrame, signatureFrame,
		`{"content":"Answer"}`,
		contextFrame, reasoningFrame,
		`{"name":"Bash","toolUseId":"t1"}`,
		meteringFrame, stopFrame("TOOL_USE"),
	))
	want := []string{"content_block_delta", "content_block_start", "message_delta"}
	if got := eventTypes(events); !equalStrings(got, want) {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}
	if idx := dataField(t, events[1], "index"); idx != 1 {
		t.Fatalf("tool index after text = %v, want 1", idx)
	}
	if summary.ReasoningChars != 2*reasoningLength {
		t.Fatalf("reasoning chars = %d, want %d", summary.ReasoningChars, 2*reasoningLength)
	}
}

// A backend that spends the whole output budget on reasoning sends no content
// at all: no events, but the summary still says why.
func TestParseEvents_ReasoningOnlyResponseReportsMaxTokens(t *testing.T) {
	events, summary := ParseEventsWithSummary(frames(reasoningFrame, signatureFrame, contextFrame, stopFrame("MAX_TOKENS"), meteringFrame))
	if len(events) != 0 {
		t.Fatalf("events = %v, want none", eventTypes(events))
	}
	if summary.StopReason != "max_tokens" || summary.BackendStopReason != "MAX_TOKENS" || summary.ReasoningChars != reasoningLength {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestStreamTranslator_UsesTheBackendStopReason(t *testing.T) {
	for _, c := range stopReasonCases {
		t.Run(c.backend, func(t *testing.T) {
			tr := NewStreamTranslator()
			events := translateAll(t, tr, `{"content":"Hi"}`, stopFrame(c.backend))
			if want := []string{"content_block_start", "content_block_delta", "content_block_stop"}; !equalStrings(eventTypes(events), want) {
				t.Fatalf("event sequence = %v, want %v", eventTypes(events), want)
			}
			if tr.StopReason() != c.want || tr.BackendStopReason() != c.backend {
				t.Fatalf("stop reason = %q (backend %q), want %s (%s)", tr.StopReason(), tr.BackendStopReason(), c.want, c.backend)
			}
		})
	}
}

func TestStreamTranslator_UnknownOrAbsentStopReasonKeepsTheDerivation(t *testing.T) {
	tr := NewStreamTranslator()
	translateAll(t, tr, `{"name":"Bash","toolUseId":"t1"}`, stopFrame("GUARDRAIL"))
	if tr.StopReason() != "tool_use" {
		t.Fatalf("unknown stop reason with a tool = %q, want tool_use", tr.StopReason())
	}
	tr = NewStreamTranslator()
	translateAll(t, tr, `{"content":"Hi"}`)
	if tr.StopReason() != "end_turn" || tr.BackendStopReason() != "" {
		t.Fatalf("absent stop reason = %q (backend %q), want end_turn", tr.StopReason(), tr.BackendStopReason())
	}
}

func TestStreamTranslator_ReasoningAndBookkeepingFramesProduceNoEvents(t *testing.T) {
	tr := NewStreamTranslator()
	for _, p := range []string{reasoningFrame, signatureFrame, contextFrame, meteringFrame, stopFrame("MAX_TOKENS")} {
		if events := tr.Translate([]byte(p)); len(events) != 0 {
			t.Fatalf("frame %s produced %v, want none", p, eventTypes(events))
		}
	}
	if tr.Emitted() {
		t.Fatal("Emitted() = true with only reasoning and bookkeeping frames")
	}
	if tr.StopReason() != "max_tokens" || tr.ReasoningChars() != reasoningLength {
		t.Fatalf("stop reason = %q reasoning chars = %d, want max_tokens %d", tr.StopReason(), tr.ReasoningChars(), reasoningLength)
	}

	// Interleaved with content, block indexes are unaffected.
	tr = NewStreamTranslator()
	events := translateAll(t, tr,
		reasoningFrame, `{"content":"Reading."}`, signatureFrame,
		`{"name":"Read","toolUseId":"t1"}`, reasoningFrame,
		`{"name":"Read","toolUseId":"t1","input":"{}"}`, contextFrame,
		`{"name":"Read","toolUseId":"t1","stop":true}`, stopFrame("TOOL_USE"),
	)
	if got := indexes(t, events); !equalInts(got, []int{0, 0, 0, 1, 1, 1}) {
		t.Fatalf("indexes = %v, want [0 0 0 1 1 1]", got)
	}
	if tr.ReasoningChars() != 2*reasoningLength {
		t.Fatalf("reasoning chars = %d, want %d", tr.ReasoningChars(), 2*reasoningLength)
	}
}
