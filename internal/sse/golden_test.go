package sse_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/sse"
	"github.com/sgeraldes/claude2kiro/parser"
)

var updateGolden = flag.Bool("update-golden", false, "update golden files")

// textOnlyEvents simulates a text-only response from the parser
func textOnlyEvents() []parser.SSEEvent {
	return []parser.SSEEvent{
		{
			Event: "content_block_delta",
			Data: map[string]interface{}{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": "Hello, ",
				},
			},
		},
		{
			Event: "content_block_delta",
			Data: map[string]interface{}{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": "world!",
				},
			},
		},
		{
			Event: "message_delta",
			Data: map[string]interface{}{
				"type": "message_delta",
				"delta": map[string]interface{}{
					"stop_reason":   "end_turn",
					"stop_sequence": nil,
				},
				"usage": map[string]interface{}{"output_tokens": 0},
			},
		},
	}
}

// toolUseEvents simulates a tool-only response from the parser
func toolUseEvents() []parser.SSEEvent {
	return []parser.SSEEvent{
		{
			Event: "content_block_start",
			Data: map[string]interface{}{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]interface{}{
					"type":  "tool_use",
					"id":    "tool_123",
					"name":  "read_file",
					"input": map[string]interface{}{},
				},
			},
		},
		{
			Event: "content_block_delta",
			Data: map[string]interface{}{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]interface{}{
					"type":         "input_json_delta",
					"partial_json": `{"path":"/test.txt"}`,
				},
			},
		},
		{
			Event: "content_block_stop",
			Data: map[string]interface{}{
				"type":  "content_block_stop",
				"index": 0,
			},
		},
		{
			Event: "message_delta",
			Data: map[string]interface{}{
				"type": "message_delta",
				"delta": map[string]interface{}{
					"stop_reason":   "tool_use",
					"stop_sequence": nil,
				},
				"usage": map[string]interface{}{"output_tokens": 0},
			},
		},
	}
}

// mixedEvents simulates a response with both text and tool use
func mixedEvents() []parser.SSEEvent {
	return []parser.SSEEvent{
		{
			Event: "content_block_delta",
			Data: map[string]interface{}{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": "Let me read that file.",
				},
			},
		},
		{
			Event: "content_block_start",
			Data: map[string]interface{}{
				"type":  "content_block_start",
				"index": 1,
				"content_block": map[string]interface{}{
					"type":  "tool_use",
					"id":    "tool_456",
					"name":  "read_file",
					"input": map[string]interface{}{},
				},
			},
		},
		{
			Event: "content_block_delta",
			Data: map[string]interface{}{
				"type":  "content_block_delta",
				"index": 1,
				"delta": map[string]interface{}{
					"type":         "input_json_delta",
					"partial_json": `{"path":"/example.txt"}`,
				},
			},
		},
		{
			Event: "content_block_stop",
			Data: map[string]interface{}{
				"type":  "content_block_stop",
				"index": 1,
			},
		},
		{
			Event: "message_delta",
			Data: map[string]interface{}{
				"type": "message_delta",
				"delta": map[string]interface{}{
					"stop_reason":   "tool_use",
					"stop_sequence": nil,
				},
				"usage": map[string]interface{}{"output_tokens": 0},
			},
		},
	}
}

// generateSSEOutput generates SSE output using the EventBuilder from given events
func generateSSEOutput(events []parser.SSEEvent, model string, messageID string) string {
	builder := sse.NewEventBuilder()

	// Analyze events to determine content types
	hasTextContent := false
	hasToolUse := false
	parserSentMessageDelta := false

	for _, e := range events {
		if e.Event == "content_block_delta" {
			if dataMap, ok := e.Data.(map[string]interface{}); ok {
				if delta, ok := dataMap["delta"].(map[string]interface{}); ok {
					if _, ok := delta["text"]; ok {
						hasTextContent = true
					}
					if _, ok := delta["partial_json"]; ok {
						hasToolUse = true
					}
				}
			}
		}
		if e.Event == "content_block_start" {
			if dataMap, ok := e.Data.(map[string]interface{}); ok {
				if cb, ok := dataMap["content_block"].(map[string]interface{}); ok {
					if cbType, ok := cb["type"].(string); ok && cbType == "tool_use" {
						hasToolUse = true
					}
				}
			}
		}
		if e.Event == "message_delta" {
			parserSentMessageDelta = true
		}
	}

	// Build message_start
	builder.MessageStart(messageID, model, 100, 1)

	// Ping event
	builder.Ping()

	// Only send text content_block_start if there's text content (not tool-only)
	if hasTextContent || !hasToolUse {
		builder.ContentBlockStart(0, "text", map[string]interface{}{"text": ""})
	}

	// Process all parser events
	for _, e := range events {
		builder.WriteRawEvent(e.Event, e.Data)
	}

	// Only send text block stop if we sent text block start
	if hasTextContent || !hasToolUse {
		builder.ContentBlockStop(0)
	}

	// Always send message_delta if parser didn't already send one
	if !parserSentMessageDelta {
		stopReason := "end_turn"
		if hasToolUse && !hasTextContent {
			stopReason = "tool_use"
		}
		builder.MessageDelta(stopReason, "", map[string]int{"output_tokens": 0})
	}

	builder.MessageStop()

	return builder.String()
}

func TestGoldenSSEOutput(t *testing.T) {
	testCases := []struct {
		name       string
		events     []parser.SSEEvent
		goldenFile string
	}{
		{"text_only", textOnlyEvents(), "testdata/text_only.golden"},
		{"tool_use", toolUseEvents(), "testdata/tool_use.golden"},
		{"mixed", mixedEvents(), "testdata/mixed.golden"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Generate SSE output using the builder
			output := generateSSEOutput(tc.events, "claude-sonnet-4-20250514", "msg_test123")

			goldenPath := filepath.Join(".", tc.goldenFile)

			if *updateGolden {
				// Create directory if needed
				dir := filepath.Dir(goldenPath)
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatalf("Failed to create golden file directory: %v", err)
				}
				// Update golden files
				if err := os.WriteFile(goldenPath, []byte(output), 0644); err != nil {
					t.Fatalf("Failed to write golden file: %v", err)
				}
				t.Logf("Updated golden file: %s", goldenPath)
				return
			}

			// Compare with golden file
			expected, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("Failed to read golden file %s: %v\nRun with -update-golden to create it", goldenPath, err)
			}

			// Normalize line endings for comparison
			outputNorm := strings.ReplaceAll(output, "\r\n", "\n")
			expectedNorm := strings.ReplaceAll(string(expected), "\r\n", "\n")

			if outputNorm != expectedNorm {
				t.Errorf("Output does not match golden file %s\n\nExpected:\n%s\n\nGot:\n%s",
					tc.goldenFile, expectedNorm, outputNorm)
			}
		})
	}
}

// TestEventOrder verifies that SSE events are generated in the correct order
func TestEventOrder(t *testing.T) {
	events := mixedEvents()
	output := generateSSEOutput(events, "claude-sonnet-4-20250514", "msg_test")

	// Expected order of event types in the output
	expectedOrder := []string{
		"event: message_start",
		"event: ping",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: content_block_stop",
		"event: message_stop",
	}

	lines := strings.Split(output, "\n")
	eventLines := []string{}
	for _, line := range lines {
		if strings.HasPrefix(line, "event: ") {
			eventLines = append(eventLines, line)
		}
	}

	if len(eventLines) != len(expectedOrder) {
		t.Errorf("Expected %d events, got %d\nEvents: %v", len(expectedOrder), len(eventLines), eventLines)
		return
	}

	for i, expected := range expectedOrder {
		if eventLines[i] != expected {
			t.Errorf("Event %d: expected %q, got %q", i, expected, eventLines[i])
		}
	}
}
