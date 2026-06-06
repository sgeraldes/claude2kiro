// Package sse provides utilities for building Server-Sent Events (SSE) streams
// in the Anthropic API format.
package sse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
)

// EventBuilder constructs SSE-formatted events in Anthropic API format.
// It buffers events and can return the complete SSE stream as bytes or string.
type EventBuilder struct {
	buf bytes.Buffer
}

// NewEventBuilder creates a new EventBuilder instance.
func NewEventBuilder() *EventBuilder {
	return &EventBuilder{}
}

// WriteEvent writes a generic SSE event with the given type and data.
// The data is marshaled to JSON.
func (b *EventBuilder) WriteEvent(eventType string, data any) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal event data: %w", err)
	}
	b.buf.WriteString(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, jsonData))
	return nil
}

// WriteRawEvent writes an SSE event, useful for forwarding parser events.
// This is a convenience wrapper around WriteEvent.
func (b *EventBuilder) WriteRawEvent(eventType string, data any) error {
	return b.WriteEvent(eventType, data)
}

// MessageStart writes the message_start event which initiates a response.
func (b *EventBuilder) MessageStart(messageID, model string, inputTokens, outputTokens int) error {
	return b.WriteEvent("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            messageID,
			"type":          "message",
			"role":          "assistant",
			"content":       []any{},
			"model":         model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  inputTokens,
				"output_tokens": outputTokens,
			},
		},
	})
}

// Ping writes a ping event for keep-alive purposes.
func (b *EventBuilder) Ping() error {
	return b.WriteEvent("ping", map[string]string{"type": "ping"})
}

// ContentBlockStart writes a content_block_start event.
// For text blocks: blockType="text", extraFields={"text": ""}
// For tool_use blocks: blockType="tool_use", extraFields={"id": "...", "name": "...", "input": {}}
func (b *EventBuilder) ContentBlockStart(index int, blockType string, extraFields map[string]any) error {
	contentBlock := map[string]any{
		"type": blockType,
	}
	maps.Copy(contentBlock, extraFields)

	return b.WriteEvent("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         index,
		"content_block": contentBlock,
	})
}

// ContentBlockDelta writes a content_block_delta event.
// For text deltas: deltaType="text_delta", deltaFields={"text": "..."}
// For tool input: deltaType="input_json_delta", deltaFields={"partial_json": "..."}
func (b *EventBuilder) ContentBlockDelta(index int, deltaType string, deltaFields map[string]any) error {
	delta := map[string]any{
		"type": deltaType,
	}
	maps.Copy(delta, deltaFields)

	return b.WriteEvent("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": delta,
	})
}

// ContentBlockStop writes a content_block_stop event for the given index.
func (b *EventBuilder) ContentBlockStop(index int) error {
	return b.WriteEvent("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": index,
	})
}

// MessageDelta writes a message_delta event with stop reason and usage.
// stopReason is typically "end_turn" or "tool_use".
// stopSequence can be empty string for nil.
func (b *EventBuilder) MessageDelta(stopReason, stopSequence string, usage map[string]int) error {
	delta := map[string]any{
		"stop_reason": stopReason,
	}
	if stopSequence != "" {
		delta["stop_sequence"] = stopSequence
	} else {
		delta["stop_sequence"] = nil
	}

	// Convert usage to interface{} map for JSON marshaling
	usageMap := map[string]any{}
	for k, v := range usage {
		usageMap[k] = v
	}

	return b.WriteEvent("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": delta,
		"usage": usageMap,
	})
}

// MessageStop writes the final message_stop event.
func (b *EventBuilder) MessageStop() error {
	return b.WriteEvent("message_stop", map[string]any{
		"type": "message_stop",
	})
}

// TextDelta is a convenience method for writing a text content delta.
func (b *EventBuilder) TextDelta(index int, text string) error {
	return b.ContentBlockDelta(index, "text_delta", map[string]any{
		"text": text,
	})
}

// ToolInputDelta is a convenience method for writing a tool input JSON delta.
func (b *EventBuilder) ToolInputDelta(index int, partialJSON string) error {
	return b.ContentBlockDelta(index, "input_json_delta", map[string]any{
		"partial_json": partialJSON,
	})
}

// Bytes returns the built SSE stream as bytes.
func (b *EventBuilder) Bytes() []byte {
	return b.buf.Bytes()
}

// String returns the built SSE stream as a string.
func (b *EventBuilder) String() string {
	return b.buf.String()
}

// Reset clears the buffer to reuse the builder.
func (b *EventBuilder) Reset() {
	b.buf.Reset()
}

// Len returns the current length of the buffer.
func (b *EventBuilder) Len() int {
	return b.buf.Len()
}
