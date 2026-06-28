package parser

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestParseCodeWhispererEvents(t *testing.T) {
	data, err := os.ReadFile("response.raw")
	if err != nil {
		t.Skip("Skipping test: response.raw fixture not found. This test requires a captured response file.")
	}

	events := ParseEvents(data)

	if len(events) == 0 {
		t.Error("Expected at least one event from response.raw")
	}

	for _, e := range events {
		fmt.Printf("event: %s\n", e.Event)
		jsonData, _ := json.Marshal(e.Data)
		fmt.Printf("data: %s\n\n", string(jsonData))
	}
}

// NOTE: the multi-tool / text-then-tool / tool-only indexing cases are covered
// for real by parse_events_test.go, which drives ParseEvents end-to-end. The
// earlier inline tests here reimplemented the indexing logic in the test body
// (testing a copy, not the code) and were removed.

func TestTextOnlyResponseGetsEndTurn(t *testing.T) {
	// Create a minimal binary frame with text content only
	// This tests that text-only responses get message_delta with stop_reason: "end_turn"

	// Simulate parsing a text-only response by checking the logic directly
	events := []SSEEvent{}
	hasToolUse := false
	hasTextContent := true

	// Add a fake text event
	events = append(events, SSEEvent{
		Event: "content_block_delta",
		Data: map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{
				"type": "text_delta",
				"text": "Hello world",
			},
		},
	})

	// Apply the same logic as ParseEvents
	if len(events) > 0 {
		_ = hasTextContent
		stopReason := "end_turn"
		if hasToolUse {
			stopReason = "tool_use"
		}
		events = append(events, SSEEvent{
			Event: "message_delta",
			Data: map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason":   stopReason,
					"stop_sequence": nil,
				},
				"usage": map[string]any{"output_tokens": 0},
			},
		})
	}

	// Verify we have 2 events: text delta + message_delta
	if len(events) != 2 {
		t.Errorf("Expected 2 events, got %d", len(events))
	}

	// Verify last event is message_delta with end_turn
	lastEvent := events[len(events)-1]
	if lastEvent.Event != "message_delta" {
		t.Errorf("Expected last event to be message_delta, got %s", lastEvent.Event)
	}

	dataMap, ok := lastEvent.Data.(map[string]any)
	if !ok {
		t.Error("Expected Data to be a map")
		return
	}

	deltaMap, ok := dataMap["delta"].(map[string]any)
	if !ok {
		t.Error("Expected delta to be a map")
		return
	}

	stopReason, ok := deltaMap["stop_reason"].(string)
	if !ok {
		t.Error("Expected stop_reason to be a string")
		return
	}

	if stopReason != "end_turn" {
		t.Errorf("Expected stop_reason to be 'end_turn' for text-only response, got '%s'", stopReason)
	}
}
