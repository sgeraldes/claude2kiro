package parser

import (
	"encoding/json"
	"strings"
	"testing"
)

// translated is what a translator reports about one response, from either
// translator, so every case runs against both.
type translated struct {
	events      []SSEEvent
	reasoning   ReasoningBlock
	toolUseIDs  []string
	text        string
	servedModel string
}

func viaStream(payloads ...string) translated {
	tr := NewStreamTranslator()
	var out translated
	for _, p := range payloads {
		out.events = append(out.events, tr.Translate([]byte(p))...)
	}
	out.events = append(out.events, tr.Finish()...)
	out.reasoning = tr.Reasoning()
	out.toolUseIDs = tr.ToolUseIDs()
	out.text = tr.Text()
	out.servedModel = tr.ServedModel()
	return out
}

func viaWhole(payloads ...string) translated {
	events, summary := ParseEventsWithSummary(frames(payloads...))
	return translated{events, summary.Reasoning, summary.ToolUseIDs, summary.Text, summary.ServedModel}
}

var translators = map[string]func(...string) translated{"stream": viaStream, "whole": viaWhole}

func eachTranslator(t *testing.T, run func(t *testing.T, translate func(...string) translated)) {
	for name, translate := range translators {
		t.Run(name, func(t *testing.T) { run(t, translate) })
	}
}

// assertNoReasoningForwarded fails when any client event carries reasoning.
func assertNoReasoningForwarded(t *testing.T, events []SSEEvent, secrets ...string) {
	t.Helper()
	raw, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range append(secrets, "thinking", "signature", "redacted") {
		if strings.Contains(string(raw), s) {
			t.Fatalf("client events carry %q: %s", s, raw)
		}
	}
}

func TestReasoningTextAndSignatureAreCaptured(t *testing.T) {
	eachTranslator(t, func(t *testing.T, translate func(...string) translated) {
		got := translate(
			`{"text":"First I need to "}`,
			`{"text":"read the file."}`,
			`{"signature":"SIG-1"}`,
			`{"content":"Reading it."}`,
			`{"name":"Read","toolUseId":"tool-a"}`,
			`{"name":"Read","toolUseId":"tool-a","input":"{\"path\":\"x\"}"}`,
			`{"name":"Read","toolUseId":"tool-a","stop":true}`,
			`{"stopReason":"TOOL_USE"}`,
		)
		want := ReasoningBlock{Text: "First I need to read the file.", Signature: "SIG-1"}
		if got.reasoning.Text != want.Text || got.reasoning.Signature != want.Signature || len(got.reasoning.Redacted) != 0 {
			t.Fatalf("reasoning = %+v, want %+v", got.reasoning, want)
		}
		if !got.reasoning.Forwardable() {
			t.Fatal("text with a signature must be forwardable")
		}
		if strings.Join(got.toolUseIDs, ",") != "tool-a" {
			t.Fatalf("tool use ids = %v, want [tool-a]", got.toolUseIDs)
		}
		if got.text != "Reading it." {
			t.Fatalf("text = %q, want the assistant text", got.text)
		}
		assertNoReasoningForwarded(t, got.events, "First I need", "SIG-1")
	})
}

func TestRedactedReasoningIsCapturedAsItArrived(t *testing.T) {
	eachTranslator(t, func(t *testing.T, translate func(...string) translated) {
		got := translate(`{"redactedContent":"QUJDRA=="}`, `{"content":"Done."}`)
		if string(got.reasoning.Redacted) != `"QUJDRA=="` {
			t.Fatalf("redacted = %s, want the JSON value as it arrived", got.reasoning.Redacted)
		}
		if !got.reasoning.Forwardable() {
			t.Fatal("redacted reasoning must be forwardable")
		}
		assertNoReasoningForwarded(t, got.events, "QUJDRA")
	})
}

func TestUnsignedReasoningIsNotForwardable(t *testing.T) {
	eachTranslator(t, func(t *testing.T, translate func(...string) translated) {
		got := translate(`{"text":"Short plan."}`, `{"content":"Answer"}`)
		if got.reasoning.Text != "Short plan." {
			t.Fatalf("reasoning text = %q, want it captured", got.reasoning.Text)
		}
		if got.reasoning.Forwardable() {
			t.Fatal("reasoning without a signature must not be forwardable")
		}
		if (ReasoningBlock{Signature: "SIG"}).Forwardable() {
			t.Fatal("a signature without text must not be forwardable")
		}
	})
}

func TestReasoningAfterContentReplacesTheEarlierBlock(t *testing.T) {
	eachTranslator(t, func(t *testing.T, translate func(...string) translated) {
		got := translate(
			`{"text":"Old plan."}`,
			`{"signature":"OLD"}`,
			`{"content":"Step one."}`,
			`{"text":"New "}`,
			`{"text":"plan."}`,
			`{"signature":"NEW"}`,
			`{"content":" Step two."}`,
		)
		if got.reasoning.Text != "New plan." || got.reasoning.Signature != "NEW" {
			t.Fatalf("reasoning = %+v, want only the last block", got.reasoning)
		}
		if got.text != "Step one. Step two." {
			t.Fatalf("text = %q", got.text)
		}

		// After a tool call too, and a later block without a signature leaves
		// nothing forwardable.
		got = translate(
			`{"text":"Plan."}`,
			`{"signature":"S1"}`,
			`{"name":"Bash","toolUseId":"t1"}`,
			`{"name":"Bash","toolUseId":"t1","stop":true}`,
			`{"text":"Unsigned afterthought."}`,
		)
		if got.reasoning.Text != "Unsigned afterthought." || got.reasoning.Signature != "" || got.reasoning.Forwardable() {
			t.Fatalf("reasoning = %+v, want the unsigned later block only", got.reasoning)
		}
	})
}

func TestSignatureAndRedactedKeepTheLatestValue(t *testing.T) {
	eachTranslator(t, func(t *testing.T, translate func(...string) translated) {
		got := translate(`{"text":"a"}`, `{"signature":"S1"}`, `{"signature":"S2"}`, `{"redactedContent":"R1"}`, `{"redactedContent":"R2"}`, `{"content":"x"}`)
		if got.reasoning.Signature != "S2" || string(got.reasoning.Redacted) != `"R2"` {
			t.Fatalf("reasoning = %+v, want the latest signature and redacted value", got.reasoning)
		}
	})
}

func TestServedModelIsTheLastModelIdSeen(t *testing.T) {
	eachTranslator(t, func(t *testing.T, translate func(...string) translated) {
		got := translate(`{"content":"Hi","modelId":"claude-sonnet-4.6"}`, `{"content":" there","modelId":"claude-opus-4.8"}`)
		if got.servedModel != "claude-opus-4.8" {
			t.Fatalf("served model = %q, want the last modelId", got.servedModel)
		}
		if got := translate(`{"content":"Hi"}`); got.servedModel != "" {
			t.Fatalf("served model = %q, want empty when no frame names one", got.servedModel)
		}
	})
}

func TestReasoningFramesStillCountReasoningChars(t *testing.T) {
	tr := NewStreamTranslator()
	tr.Translate([]byte(`{"text":"abc"}`))
	tr.Translate([]byte(`{"signature":"zzzz"}`))
	if tr.ReasoningChars() != 3 {
		t.Fatalf("reasoning chars = %d, want 3 (the signature is not reasoning text)", tr.ReasoningChars())
	}
}
