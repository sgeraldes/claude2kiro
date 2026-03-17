// Package sse provides utilities for building Server-Sent Events (SSE) streams
// in the Anthropic API format.
package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// StreamWriter writes SSE events directly to an http.ResponseWriter.
// It supports optional event capture for comparison/debugging modes.
type StreamWriter struct {
	w               http.ResponseWriter
	flusher         http.Flusher
	capturedEvents  *[]CapturedEvent
}

// CapturedEvent represents a captured SSE event for comparison/debugging.
type CapturedEvent struct {
	Event string `json:"event"`
	Data  string `json:"data"`
}

// NewStreamWriter creates a new StreamWriter.
// If capturedEvents is non-nil, events will be captured for later comparison.
func NewStreamWriter(w http.ResponseWriter, flusher http.Flusher, capturedEvents *[]CapturedEvent) *StreamWriter {
	return &StreamWriter{
		w:              w,
		flusher:        flusher,
		capturedEvents: capturedEvents,
	}
}

// WriteEvent writes an SSE event to the response writer.
func (s *StreamWriter) WriteEvent(eventType string, data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal event data: %w", err)
	}

	// Capture event if requested
	if s.capturedEvents != nil {
		*s.capturedEvents = append(*s.capturedEvents, CapturedEvent{
			Event: eventType,
			Data:  string(jsonData),
		})
	}

	// Write SSE format
	fmt.Fprintf(s.w, "event: %s\n", eventType)
	fmt.Fprintf(s.w, "data: %s\n\n", string(jsonData))
	s.flusher.Flush()

	return nil
}

// MessageStart writes the message_start event which initiates a response.
func (s *StreamWriter) MessageStart(messageID, model string, inputTokens, outputTokens int) error {
	return s.WriteEvent("message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            messageID,
			"type":          "message",
			"role":          "assistant",
			"content":       []interface{}{},
			"model":         model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]interface{}{
				"input_tokens":  inputTokens,
				"output_tokens": outputTokens,
			},
		},
	})
}

// Ping writes a ping event for keep-alive purposes.
func (s *StreamWriter) Ping() error {
	return s.WriteEvent("ping", map[string]string{"type": "ping"})
}

// ContentBlockStart writes a content_block_start event.
func (s *StreamWriter) ContentBlockStart(index int, blockType string, extraFields map[string]interface{}) error {
	contentBlock := map[string]interface{}{
		"type": blockType,
	}
	for k, v := range extraFields {
		contentBlock[k] = v
	}

	return s.WriteEvent("content_block_start", map[string]interface{}{
		"type":          "content_block_start",
		"index":         index,
		"content_block": contentBlock,
	})
}

// ContentBlockDelta writes a content_block_delta event.
func (s *StreamWriter) ContentBlockDelta(index int, deltaType string, deltaFields map[string]interface{}) error {
	delta := map[string]interface{}{
		"type": deltaType,
	}
	for k, v := range deltaFields {
		delta[k] = v
	}

	return s.WriteEvent("content_block_delta", map[string]interface{}{
		"type":  "content_block_delta",
		"index": index,
		"delta": delta,
	})
}

// ContentBlockStop writes a content_block_stop event for the given index.
func (s *StreamWriter) ContentBlockStop(index int) error {
	return s.WriteEvent("content_block_stop", map[string]interface{}{
		"type":  "content_block_stop",
		"index": index,
	})
}

// MessageDelta writes a message_delta event with stop reason and usage.
func (s *StreamWriter) MessageDelta(stopReason, stopSequence string, usage map[string]int) error {
	delta := map[string]interface{}{
		"stop_reason": stopReason,
	}
	if stopSequence != "" {
		delta["stop_sequence"] = stopSequence
	} else {
		delta["stop_sequence"] = nil
	}

	// Convert usage to interface{} map for JSON marshaling
	usageMap := map[string]interface{}{}
	for k, v := range usage {
		usageMap[k] = v
	}

	return s.WriteEvent("message_delta", map[string]interface{}{
		"type":  "message_delta",
		"delta": delta,
		"usage": usageMap,
	})
}

// MessageStop writes the final message_stop event.
func (s *StreamWriter) MessageStop() error {
	return s.WriteEvent("message_stop", map[string]interface{}{
		"type": "message_stop",
	})
}

// TextDelta is a convenience method for writing a text content delta.
func (s *StreamWriter) TextDelta(index int, text string) error {
	return s.ContentBlockDelta(index, "text_delta", map[string]interface{}{
		"text": text,
	})
}

// ToolInputDelta is a convenience method for writing a tool input JSON delta.
func (s *StreamWriter) ToolInputDelta(index int, partialJSON string) error {
	return s.ContentBlockDelta(index, "input_json_delta", map[string]interface{}{
		"partial_json": partialJSON,
	})
}
