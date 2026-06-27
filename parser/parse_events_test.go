package parser

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"
)

// buildFrame assembles one CodeWhisperer event-stream frame exactly as
// ParseEvents reads it back: a big-endian total length, a big-endian header
// length, the (skipped) header bytes, the payload, and a 4-byte trailing CRC.
// Real payloads arrive with a leading "vent" fragment that ParseEvents trims,
// so callers pass the JSON and we prepend it here to mirror the wire format.
func buildFrame(header []byte, jsonPayload string) []byte {
	payload := append([]byte("vent"), []byte(jsonPayload)...)
	headerLen := uint32(len(header))
	totalLen := uint32(12 + len(header) + len(payload))

	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, totalLen)
	binary.Write(&b, binary.BigEndian, headerLen)
	b.Write(header)
	b.Write(payload)
	binary.Write(&b, binary.BigEndian, uint32(0)) // CRC (ignored by parser)
	return b.Bytes()
}

func frames(jsonPayloads ...string) []byte {
	var all []byte
	for _, p := range jsonPayloads {
		all = append(all, buildFrame([]byte(":event-type"), p)...)
	}
	return all
}

// dataField returns data[key] for an SSEEvent whose Data is a map.
func dataField(t *testing.T, e SSEEvent, key string) any {
	t.Helper()
	m, ok := e.Data.(map[string]any)
	if !ok {
		t.Fatalf("event %q Data is %T, want map", e.Event, e.Data)
	}
	return m[key]
}

func eventTypes(events []SSEEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Event
	}
	return out
}

func TestParseEvents_TextOnly(t *testing.T) {
	events := ParseEvents(frames(`{"content":"Hello"}`, `{"content":" world"}`))

	want := []string{"content_block_delta", "content_block_delta", "message_delta"}
	if got := eventTypes(events); !equalStrings(got, want) {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}

	// Text deltas live at index 0.
	if idx := dataField(t, events[0], "index"); idx != 0 {
		t.Errorf("first text delta index = %v, want 0", idx)
	}
	// Terminal message_delta carries end_turn for a text-only response.
	delta := dataField(t, events[2], "delta").(map[string]any)
	if delta["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v, want end_turn", delta["stop_reason"])
	}
}

func TestParseEvents_SingleToolUse(t *testing.T) {
	events := ParseEvents(frames(
		`{"name":"Bash","toolUseId":"tu_1"}`,                       // start
		`{"name":"Bash","toolUseId":"tu_1","input":"{\"cmd\":1}"}`, // input delta
		`{"toolUseId":"tu_1","stop":true}`,                         // stop
	))

	want := []string{"content_block_start", "content_block_delta", "content_block_stop", "message_delta"}
	if got := eventTypes(events); !equalStrings(got, want) {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}

	// Tool-only response: first tool is at index 0.
	start := events[0]
	if idx := dataField(t, start, "index"); idx != 0 {
		t.Errorf("tool start index = %v, want 0", idx)
	}
	cb := dataField(t, start, "content_block").(map[string]any)
	if cb["type"] != "tool_use" || cb["id"] != "tu_1" || cb["name"] != "Bash" {
		t.Errorf("content_block = %v, want tool_use id=tu_1 name=Bash", cb)
	}

	// Input arrives as input_json_delta carrying the raw partial JSON.
	d := dataField(t, events[1], "delta").(map[string]any)
	if d["type"] != "input_json_delta" || d["partial_json"] != `{"cmd":1}` {
		t.Errorf("input delta = %v, want input_json_delta partial_json={\"cmd\":1}", d)
	}

	// Terminal stop_reason is tool_use when any tool was emitted.
	md := dataField(t, events[3], "delta").(map[string]any)
	if md["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v, want tool_use", md["stop_reason"])
	}
}

func TestParseEvents_TextThenToolIndexing(t *testing.T) {
	// When text precedes a tool, the text holds index 0 and the tool must
	// start at index 1 so the two content blocks don't collide.
	events := ParseEvents(frames(
		`{"content":"thinking"}`,
		`{"name":"Read","toolUseId":"tu_a"}`,
	))

	var toolStart *SSEEvent
	for i := range events {
		if events[i].Event == "content_block_start" {
			toolStart = &events[i]
		}
	}
	if toolStart == nil {
		t.Fatal("no content_block_start emitted for the tool")
	}
	if idx := dataField(t, *toolStart, "index"); idx != 1 {
		t.Errorf("tool index after text = %v, want 1", idx)
	}
}

func TestParseEvents_MultipleToolsDistinctIndices(t *testing.T) {
	// Tool-only response with two tools: indices 0 and 1, in first-seen order.
	events := ParseEvents(frames(
		`{"name":"Bash","toolUseId":"tu_1"}`,
		`{"name":"Read","toolUseId":"tu_2"}`,
	))

	indexByID := map[string]any{}
	for _, e := range events {
		if e.Event != "content_block_start" {
			continue
		}
		cb := dataField(t, e, "content_block").(map[string]any)
		indexByID[cb["id"].(string)] = dataField(t, e, "index")
	}
	if indexByID["tu_1"] != 0 || indexByID["tu_2"] != 1 {
		t.Errorf("tool indices = %v, want tu_1=0 tu_2=1", indexByID)
	}
}

func TestParseEvents_EmptyAndGarbageAreSafe(t *testing.T) {
	// Each must return no events and must not panic.
	cases := map[string][]byte{
		"nil":             nil,
		"empty":           {},
		"too short":       {0x00, 0x01, 0x02},
		"garbage":         bytes.Repeat([]byte{0xFF}, 64),
		"valid-but-empty": frames(`{}`), // parses to an event with no content/tool -> dropped
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			events := ParseEvents(in)
			if len(events) != 0 {
				t.Errorf("ParseEvents(%s) = %d events, want 0", name, len(events))
			}
		})
	}
}

func TestParseEvents_TruncatedFrameDoesNotPanic(t *testing.T) {
	full := frames(`{"content":"hi"}`)
	// Cut the frame mid-payload: parser must stop cleanly, not panic.
	truncated := full[:len(full)-6]
	_ = ParseEvents(truncated) // success = no panic
}

func equalStrings(a, b []string) bool {
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

// Sanity: a frame round-trips through encoding/json the way the parser expects.
func TestBuildFrame_PayloadShape(t *testing.T) {
	f := buildFrame([]byte(":event-type"), `{"content":"x"}`)
	if len(f) < 12 {
		t.Fatal("frame too short")
	}
	// The declared total length must equal the actual frame length.
	total := binary.BigEndian.Uint32(f[0:4])
	if int(total) != len(f) {
		t.Errorf("totalLen field = %d, actual frame len = %d", total, len(f))
	}
	var evt assistantResponseEvent
	if err := json.Unmarshal([]byte(`{"content":"x"}`), &evt); err != nil || evt.Content != "x" {
		t.Errorf("payload JSON did not round-trip: %v", err)
	}
}
