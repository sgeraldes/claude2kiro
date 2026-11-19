package parser

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func translateAll(t *testing.T, tr *StreamTranslator, jsonPayloads ...string) []SSEEvent {
	t.Helper()
	var out []SSEEvent
	for _, p := range jsonPayloads {
		out = append(out, tr.Translate([]byte(p))...)
	}
	return append(out, tr.Finish()...)
}

func indexes(t *testing.T, events []SSEEvent) []int {
	t.Helper()
	out := make([]int, len(events))
	for i, e := range events {
		idx, ok := dataField(t, e, "index").(int)
		if !ok {
			t.Fatalf("event %d (%s) has no int index", i, e.Event)
		}
		out[i] = idx
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestReadFramePayload_ReadsFramesOneAtATime(t *testing.T) {
	r := bytes.NewReader(frames(`{"content":"a"}`, `{"content":"b"}`))

	for _, want := range []string{`{"content":"a"}`, `{"content":"b"}`} {
		got, err := ReadFramePayload(r)
		if err != nil {
			t.Fatalf("ReadFramePayload: %v", err)
		}
		if string(got) != want {
			t.Fatalf("payload = %q, want %q", got, want)
		}
	}
	if _, err := ReadFramePayload(r); err != io.EOF {
		t.Fatalf("after the last frame err = %v, want io.EOF", err)
	}
}

func TestReadFramePayload_TruncatedFrame(t *testing.T) {
	all := frames(`{"content":"hello"}`)
	_, err := ReadFramePayload(bytes.NewReader(all[:len(all)-3]))
	if err != io.ErrUnexpectedEOF {
		t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestReadFramePayload_InvalidLengths(t *testing.T) {
	// total length smaller than the header it claims to hold
	bad := []byte{0, 0, 0, 4, 0, 0, 0, 200, 0, 0, 0, 0}
	_, err := ReadFramePayload(bytes.NewReader(bad))
	if !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("err = %v, want ErrInvalidFrame", err)
	}
}

func TestStreamTranslator_TextOnly(t *testing.T) {
	tr := NewStreamTranslator()
	events := translateAll(t, tr, `{"content":"Hello"}`, `{"content":" world"}`)

	want := []string{"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop"}
	if got := eventTypes(events); !equalStrings(got, want) {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}
	if got := indexes(t, events); !equalInts(got, []int{0, 0, 0, 0}) {
		t.Fatalf("indexes = %v, want all 0", got)
	}
	if tr.StopReason() != "end_turn" {
		t.Fatalf("stop reason = %q, want end_turn", tr.StopReason())
	}
}

func TestStreamTranslator_TextThenTool(t *testing.T) {
	tr := NewStreamTranslator()
	events := translateAll(t, tr,
		`{"content":"Reading."}`,
		`{"name":"Read","toolUseId":"t1"}`,
		`{"name":"Read","toolUseId":"t1","input":"{\"a\":1}"}`,
		`{"name":"Read","toolUseId":"t1","stop":true}`,
	)

	want := []string{
		"content_block_start", "content_block_delta", "content_block_stop", // text, closed before the tool
		"content_block_start", "content_block_delta", "content_block_stop", // tool
	}
	if got := eventTypes(events); !equalStrings(got, want) {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}
	if got := indexes(t, events); !equalInts(got, []int{0, 0, 0, 1, 1, 1}) {
		t.Fatalf("indexes = %v", got)
	}
	if tr.StopReason() != "tool_use" {
		t.Fatalf("stop reason = %q, want tool_use", tr.StopReason())
	}
}

func TestStreamTranslator_ToolOnlyStartsAtIndexZero(t *testing.T) {
	tr := NewStreamTranslator()
	events := translateAll(t, tr,
		`{"name":"Bash","toolUseId":"t1"}`,
		`{"name":"Bash","toolUseId":"t1","input":"{}"}`,
		`{"name":"Bash","toolUseId":"t1","stop":true}`,
	)
	if got := indexes(t, events); !equalInts(got, []int{0, 0, 0}) {
		t.Fatalf("indexes = %v, want all 0", got)
	}
}

func TestStreamTranslator_TextAfterToolOpensANewBlock(t *testing.T) {
	tr := NewStreamTranslator()
	events := translateAll(t, tr,
		`{"name":"Bash","toolUseId":"t1"}`,
		`{"name":"Bash","toolUseId":"t1","stop":true}`,
		`{"content":"Done."}`,
	)
	want := []string{
		"content_block_start", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop",
	}
	if got := eventTypes(events); !equalStrings(got, want) {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}
	if got := indexes(t, events); !equalInts(got, []int{0, 0, 1, 1, 1}) {
		t.Fatalf("indexes = %v", got)
	}
}

func TestStreamTranslator_FinishClosesAToolTheBackendLeftOpen(t *testing.T) {
	tr := NewStreamTranslator()
	events := translateAll(t, tr,
		`{"name":"Bash","toolUseId":"t1"}`,
		`{"name":"Bash","toolUseId":"t1","input":"{"}`,
	)
	want := []string{"content_block_start", "content_block_delta", "content_block_stop"}
	if got := eventTypes(events); !equalStrings(got, want) {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}
}

func TestStreamTranslator_IgnoresFramesThatCarryNoContent(t *testing.T) {
	tr := NewStreamTranslator()
	events := translateAll(t, tr, `{"unit":"credit","usage":0.5}`, `not json`)
	if len(events) != 0 {
		t.Fatalf("events = %v, want none", eventTypes(events))
	}
	if tr.Emitted() {
		t.Fatal("Emitted() = true with no content frames")
	}
}
