package sse

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewEventBuilder(t *testing.T) {
	builder := NewEventBuilder()
	if builder == nil {
		t.Fatal("NewEventBuilder returned nil")
	}
	if builder.Len() != 0 {
		t.Errorf("New builder should have length 0, got %d", builder.Len())
	}
}

func TestMessageStart(t *testing.T) {
	builder := NewEventBuilder()
	err := builder.MessageStart("msg_123", "claude-sonnet-4", 100, 1)
	if err != nil {
		t.Fatalf("MessageStart failed: %v", err)
	}

	output := builder.String()

	if !strings.Contains(output, "event: message_start") {
		t.Error("Missing message_start event type")
	}
	if !strings.Contains(output, "msg_123") {
		t.Error("Missing message ID")
	}
	if !strings.Contains(output, "claude-sonnet-4") {
		t.Error("Missing model name")
	}
	if !strings.Contains(output, `"type":"message_start"`) {
		t.Error("Missing type field in data")
	}
}

func TestPing(t *testing.T) {
	builder := NewEventBuilder()
	err := builder.Ping()
	if err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	output := builder.String()
	if !strings.Contains(output, "event: ping") {
		t.Error("Missing ping event type")
	}
	if !strings.Contains(output, `"type":"ping"`) {
		t.Error("Missing type field in ping data")
	}
}

func TestContentBlockStart_Text(t *testing.T) {
	builder := NewEventBuilder()
	err := builder.ContentBlockStart(0, "text", map[string]interface{}{"text": ""})
	if err != nil {
		t.Fatalf("ContentBlockStart failed: %v", err)
	}

	output := builder.String()
	if !strings.Contains(output, "event: content_block_start") {
		t.Error("Missing content_block_start event type")
	}
	if !strings.Contains(output, `"index":0`) {
		t.Error("Missing index field")
	}
	if !strings.Contains(output, `"type":"text"`) {
		t.Error("Missing type:text in content_block")
	}
}

func TestContentBlockStart_ToolUse(t *testing.T) {
	builder := NewEventBuilder()
	err := builder.ContentBlockStart(1, "tool_use", map[string]interface{}{
		"id":    "tool_abc",
		"name":  "read_file",
		"input": map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("ContentBlockStart failed: %v", err)
	}

	output := builder.String()
	if !strings.Contains(output, `"type":"tool_use"`) {
		t.Error("Missing type:tool_use in content_block")
	}
	if !strings.Contains(output, `"id":"tool_abc"`) {
		t.Error("Missing tool id")
	}
	if !strings.Contains(output, `"name":"read_file"`) {
		t.Error("Missing tool name")
	}
}

func TestContentBlockDelta_Text(t *testing.T) {
	builder := NewEventBuilder()
	err := builder.TextDelta(0, "Hello, world!")
	if err != nil {
		t.Fatalf("TextDelta failed: %v", err)
	}

	output := builder.String()
	if !strings.Contains(output, "event: content_block_delta") {
		t.Error("Missing content_block_delta event type")
	}
	if !strings.Contains(output, `"type":"text_delta"`) {
		t.Error("Missing type:text_delta in delta")
	}
	if !strings.Contains(output, `"text":"Hello, world!"`) {
		t.Error("Missing text content")
	}
}

func TestContentBlockDelta_ToolInput(t *testing.T) {
	builder := NewEventBuilder()
	err := builder.ToolInputDelta(1, `{"path":"/test.txt"}`)
	if err != nil {
		t.Fatalf("ToolInputDelta failed: %v", err)
	}

	output := builder.String()
	if !strings.Contains(output, "event: content_block_delta") {
		t.Error("Missing content_block_delta event type")
	}
	if !strings.Contains(output, `"type":"input_json_delta"`) {
		t.Error("Missing type:input_json_delta in delta")
	}
	if !strings.Contains(output, `"partial_json"`) {
		t.Error("Missing partial_json field")
	}
}

func TestContentBlockStop(t *testing.T) {
	builder := NewEventBuilder()
	err := builder.ContentBlockStop(0)
	if err != nil {
		t.Fatalf("ContentBlockStop failed: %v", err)
	}

	output := builder.String()
	if !strings.Contains(output, "event: content_block_stop") {
		t.Error("Missing content_block_stop event type")
	}
	if !strings.Contains(output, `"index":0`) {
		t.Error("Missing index field")
	}
	if !strings.Contains(output, `"type":"content_block_stop"`) {
		t.Error("Missing type field")
	}
}

func TestMessageDelta_EndTurn(t *testing.T) {
	builder := NewEventBuilder()
	err := builder.MessageDelta("end_turn", "", map[string]int{"output_tokens": 50})
	if err != nil {
		t.Fatalf("MessageDelta failed: %v", err)
	}

	output := builder.String()
	if !strings.Contains(output, "event: message_delta") {
		t.Error("Missing message_delta event type")
	}
	if !strings.Contains(output, `"stop_reason":"end_turn"`) {
		t.Error("Missing stop_reason:end_turn")
	}
	if !strings.Contains(output, `"output_tokens":50`) {
		t.Error("Missing output_tokens in usage")
	}
}

func TestMessageDelta_ToolUse(t *testing.T) {
	builder := NewEventBuilder()
	err := builder.MessageDelta("tool_use", "", map[string]int{"output_tokens": 25})
	if err != nil {
		t.Fatalf("MessageDelta failed: %v", err)
	}

	output := builder.String()
	if !strings.Contains(output, `"stop_reason":"tool_use"`) {
		t.Error("Missing stop_reason:tool_use")
	}
}

func TestMessageStop(t *testing.T) {
	builder := NewEventBuilder()
	err := builder.MessageStop()
	if err != nil {
		t.Fatalf("MessageStop failed: %v", err)
	}

	output := builder.String()
	if !strings.Contains(output, "event: message_stop") {
		t.Error("Missing message_stop event type")
	}
	if !strings.Contains(output, `"type":"message_stop"`) {
		t.Error("Missing type field")
	}
}

func TestContentBlockSequence(t *testing.T) {
	builder := NewEventBuilder()
	builder.ContentBlockStart(0, "text", map[string]interface{}{"text": ""})
	builder.TextDelta(0, "Hello")
	builder.ContentBlockStop(0)

	output := builder.String()
	events := strings.Split(output, "event:")
	// First split element is empty, so we have 4 elements for 3 events
	if len(events) != 4 {
		t.Errorf("Expected 3 events, got %d", len(events)-1)
	}
}

func TestCompleteTextResponse(t *testing.T) {
	builder := NewEventBuilder()
	builder.MessageStart("msg_test", "claude-sonnet-4", 100, 1)
	builder.Ping()
	builder.ContentBlockStart(0, "text", map[string]interface{}{"text": ""})
	builder.TextDelta(0, "Hello, world!")
	builder.ContentBlockStop(0)
	builder.MessageDelta("end_turn", "", map[string]int{"output_tokens": 3})
	builder.MessageStop()

	output := builder.String()

	// Count events
	eventCount := strings.Count(output, "event:")
	if eventCount != 7 {
		t.Errorf("Expected 7 events, got %d", eventCount)
	}

	// Verify event order by finding positions
	events := []string{
		"event: message_start",
		"event: ping",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
	}

	lastPos := -1
	for _, event := range events {
		pos := strings.Index(output, event)
		if pos == -1 {
			t.Errorf("Missing event: %s", event)
			continue
		}
		if pos <= lastPos {
			t.Errorf("Event %s is out of order", event)
		}
		lastPos = pos
	}
}

func TestCompleteToolUseResponse(t *testing.T) {
	builder := NewEventBuilder()
	builder.MessageStart("msg_test", "claude-sonnet-4", 100, 1)
	builder.Ping()
	builder.ContentBlockStart(0, "tool_use", map[string]interface{}{
		"id":    "tool_123",
		"name":  "read_file",
		"input": map[string]interface{}{},
	})
	builder.ToolInputDelta(0, `{"path":"/test.txt"}`)
	builder.ContentBlockStop(0)
	builder.MessageDelta("tool_use", "", map[string]int{"output_tokens": 5})
	builder.MessageStop()

	output := builder.String()

	// Verify tool_use response has correct stop_reason
	if !strings.Contains(output, `"stop_reason":"tool_use"`) {
		t.Error("Tool use response should have stop_reason:tool_use")
	}
}

func TestReset(t *testing.T) {
	builder := NewEventBuilder()
	builder.MessageStart("msg_test", "claude-sonnet-4", 100, 1)

	if builder.Len() == 0 {
		t.Error("Builder should have content after MessageStart")
	}

	builder.Reset()

	if builder.Len() != 0 {
		t.Errorf("Builder should be empty after Reset, got length %d", builder.Len())
	}
}

func TestBytesAndString(t *testing.T) {
	builder := NewEventBuilder()
	builder.Ping()

	bytesOutput := builder.Bytes()
	stringOutput := builder.String()

	if string(bytesOutput) != stringOutput {
		t.Error("Bytes and String output should match")
	}
}

func TestSSEFormat(t *testing.T) {
	builder := NewEventBuilder()
	builder.Ping()

	output := builder.String()

	// SSE format should be: event: TYPE\ndata: JSON\n\n
	lines := strings.Split(output, "\n")
	if len(lines) < 3 {
		t.Error("SSE output should have at least 3 lines (event, data, blank)")
	}

	if !strings.HasPrefix(lines[0], "event: ") {
		t.Error("First line should start with 'event: '")
	}
	if !strings.HasPrefix(lines[1], "data: ") {
		t.Error("Second line should start with 'data: '")
	}
	if lines[2] != "" {
		t.Error("Third line should be empty (blank line between events)")
	}
}

func TestWriteRawEvent(t *testing.T) {
	builder := NewEventBuilder()

	// Test with a map[string]interface{} data structure (like from parser)
	data := map[string]interface{}{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]interface{}{
			"type": "text_delta",
			"text": "Hello",
		},
	}

	err := builder.WriteRawEvent("content_block_delta", data)
	if err != nil {
		t.Fatalf("WriteRawEvent failed: %v", err)
	}

	output := builder.String()
	if !strings.Contains(output, "event: content_block_delta") {
		t.Error("Missing event type")
	}
	if !strings.Contains(output, `"text":"Hello"`) {
		t.Error("Missing text content in raw event")
	}
}

func TestJSONValidity(t *testing.T) {
	builder := NewEventBuilder()
	builder.MessageStart("msg_test", "claude-sonnet-4", 100, 1)
	builder.Ping()
	builder.ContentBlockStart(0, "text", map[string]interface{}{"text": ""})
	builder.TextDelta(0, "Hello, world!")
	builder.ContentBlockStop(0)
	builder.MessageDelta("end_turn", "", map[string]int{"output_tokens": 3})
	builder.MessageStop()

	output := builder.String()

	// Extract and validate each JSON data line
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			jsonStr := strings.TrimPrefix(line, "data: ")
			var parsed interface{}
			if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
				t.Errorf("Invalid JSON in SSE data: %s\nError: %v", jsonStr, err)
			}
		}
	}
}
