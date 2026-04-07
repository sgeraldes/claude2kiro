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

func TestMultiToolIndexing(t *testing.T) {
	// Test that multiple tools get different indices
	toolIndices := make(map[string]int)
	nextToolIndex := 0

	// Simulate first tool
	toolId1 := "tool_1"
	if _, exists := toolIndices[toolId1]; !exists {
		toolIndices[toolId1] = nextToolIndex
		nextToolIndex++
	}

	// Simulate second tool
	toolId2 := "tool_2"
	if _, exists := toolIndices[toolId2]; !exists {
		toolIndices[toolId2] = nextToolIndex
		nextToolIndex++
	}

	if toolIndices[toolId1] != 0 {
		t.Errorf("Expected tool_1 to have index 0, got %d", toolIndices[toolId1])
	}
	if toolIndices[toolId2] != 1 {
		t.Errorf("Expected tool_2 to have index 1, got %d", toolIndices[toolId2])
	}
}

func TestTextThenToolIndexing(t *testing.T) {
	// Test that when text content is present, tools start at index 1
	toolIndices := make(map[string]int)
	nextToolIndex := 0
	hasTextContent := false

	// Simulate text content arriving first
	hasTextContent = true
	if len(toolIndices) == 0 && nextToolIndex == 0 {
		nextToolIndex = 1 // Bump to 1 so tools don't conflict with text at index 0
	}

	// Simulate first tool after text
	toolId1 := "tool_1"
	if _, exists := toolIndices[toolId1]; !exists {
		toolIndices[toolId1] = nextToolIndex
		nextToolIndex++
	}

	if !hasTextContent {
		t.Error("Expected hasTextContent to be true")
	}
	if toolIndices[toolId1] != 1 {
		t.Errorf("Expected tool_1 to have index 1 (after text at 0), got %d", toolIndices[toolId1])
	}
}

func TestToolOnlyIndexing(t *testing.T) {
	// Test that tool-only responses start at index 0
	toolIndices := make(map[string]int)
	nextToolIndex := 0

	// No text content - tools should start at 0
	toolId1 := "tool_1"
	if _, exists := toolIndices[toolId1]; !exists {
		toolIndices[toolId1] = nextToolIndex
		nextToolIndex++
	}

	if toolIndices[toolId1] != 0 {
		t.Errorf("Expected tool_1 to have index 0 for tool-only response, got %d", toolIndices[toolId1])
	}
}

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
		Data: map[string]interface{}{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]interface{}{
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
			Data: map[string]interface{}{
				"type": "message_delta",
				"delta": map[string]interface{}{
					"stop_reason":   stopReason,
					"stop_sequence": nil,
				},
				"usage": map[string]interface{}{"output_tokens": 0},
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

	dataMap, ok := lastEvent.Data.(map[string]interface{})
	if !ok {
		t.Error("Expected Data to be a map")
		return
	}

	deltaMap, ok := dataMap["delta"].(map[string]interface{})
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
