// Package sse provides utilities for building Server-Sent Events (SSE) streams
// in the Anthropic API format.
package sse

import (
	"net/http"
	"time"

	"github.com/sgeraldes/claude2kiro/parser"
)

// StreamConfig holds configuration for streaming SSE events.
type StreamConfig struct {
	// MessageID is the unique ID for this message response
	MessageID string
	// Model is the model name (e.g., "claude-sonnet-4-20250514")
	Model string
	// InputTokens is the estimated input token count
	InputTokens int
	// StreamingDelayMax is the maximum random delay between events
	StreamingDelayMax time.Duration
}

// EventAnalysis contains analyzed event information.
type EventAnalysis struct {
	HasTextContent         bool
	HasToolUse             bool
	ParserSentMessageDelta bool
	ToolBlockCount         int
}

// AnalyzeEvents analyzes parser events to determine content types.
func AnalyzeEvents(events []parser.SSEEvent) EventAnalysis {
	analysis := EventAnalysis{}

	for _, e := range events {
		if e.Event == "content_block_delta" {
			if dataMap, ok := e.Data.(map[string]any); ok {
				if delta, ok := dataMap["delta"].(map[string]any); ok {
					if _, ok := delta["text"]; ok {
						analysis.HasTextContent = true
					}
					if _, ok := delta["partial_json"]; ok {
						analysis.HasToolUse = true
					}
				}
			}
		}
		if e.Event == "content_block_start" {
			if dataMap, ok := e.Data.(map[string]any); ok {
				if cb, ok := dataMap["content_block"].(map[string]any); ok {
					if cbType, ok := cb["type"].(string); ok && cbType == "tool_use" {
						analysis.HasToolUse = true
						analysis.ToolBlockCount++
					}
				}
			}
		}
		if e.Event == "message_delta" {
			analysis.ParserSentMessageDelta = true
		}
	}

	return analysis
}

// StreamEvents streams SSE events to the response writer using the new EventBuilder pattern.
// This is the new implementation used when UseNewSSEBuilder is enabled.
func StreamEvents(w http.ResponseWriter, flusher http.Flusher, events []parser.SSEEvent, cfg StreamConfig, capturedEvents *[]CapturedEvent, delayFn func()) string {
	sw := NewStreamWriter(w, flusher, capturedEvents)
	var responseText string

	analysis := AnalyzeEvents(events)

	// Send message_start
	sw.MessageStart(cfg.MessageID, cfg.Model, cfg.InputTokens, 1)

	// Ping event
	sw.Ping()

	// Only send text content_block_start if there's text content (not tool-only)
	if analysis.HasTextContent || !analysis.HasToolUse {
		sw.ContentBlockStart(0, "text", map[string]interface{}{"text": ""})
	}

	outputTokens := 0
	for _, e := range events {
		sw.WriteEvent(e.Event, e.Data)

		if e.Event == "content_block_delta" {
			if dataMap, ok := e.Data.(map[string]any); ok {
				if delta, ok := dataMap["delta"].(map[string]any); ok {
					if text, ok := delta["text"].(string); ok {
						responseText += text
						outputTokens = len(responseText)
					}
				}
			}
		}

		// Call delay function if provided
		if delayFn != nil {
			delayFn()
		}
	}

	// Only send text block stop if we sent text block start
	if analysis.HasTextContent || !analysis.HasToolUse {
		sw.ContentBlockStop(0)
	}

	// Always send message_delta if parser didn't already send one
	if !analysis.ParserSentMessageDelta {
		stopReason := "end_turn"
		if analysis.HasToolUse && !analysis.HasTextContent {
			stopReason = "tool_use"
		}
		sw.MessageDelta(stopReason, "", map[string]int{"output_tokens": outputTokens})
	}

	sw.MessageStop()

	return responseText
}

// StreamEmptyResponse sends an empty/error response when no events are parsed.
func StreamEmptyResponse(w http.ResponseWriter, flusher http.Flusher, cfg StreamConfig, capturedEvents *[]CapturedEvent, errorMessage string) {
	sw := NewStreamWriter(w, flusher, capturedEvents)

	// Send message_start with empty response
	sw.MessageStart(cfg.MessageID, cfg.Model, 0, 0)

	// Text block with error message
	sw.ContentBlockStart(0, "text", map[string]interface{}{"text": errorMessage})
	sw.ContentBlockStop(0)

	sw.MessageDelta("end_turn", "", map[string]int{"output_tokens": 0})
	sw.MessageStop()
}

// EventCapture is an interface for capturing SSE events.
// This allows adapting to different capture types (e.g., main.go's CapturedSSEEvent).
type EventCapture interface {
	Append(event, data string)
}

// StreamEventsWithCapture is like StreamEvents but accepts a generic event capture interface.
// This is useful for integration with existing code that has its own capture type.
func StreamEventsWithCapture(w http.ResponseWriter, flusher http.Flusher, events []parser.SSEEvent, cfg StreamConfig, capture EventCapture, delayFn func()) string {
	// Create a wrapper capture if needed
	var capturedEvents *[]CapturedEvent
	var wrapper *captureWrapper
	if capture != nil {
		wrapper = &captureWrapper{capture: capture}
		events := make([]CapturedEvent, 0)
		capturedEvents = &events
	}

	result := StreamEvents(w, flusher, events, cfg, capturedEvents, delayFn)

	// Forward captured events to the wrapper
	if wrapper != nil && capturedEvents != nil {
		for _, e := range *capturedEvents {
			wrapper.capture.Append(e.Event, e.Data)
		}
	}

	return result
}

// captureWrapper adapts EventCapture interface
type captureWrapper struct {
	capture EventCapture
}

// StreamEmptyResponseWithCapture is like StreamEmptyResponse but accepts a generic event capture interface.
func StreamEmptyResponseWithCapture(w http.ResponseWriter, flusher http.Flusher, cfg StreamConfig, capture EventCapture, errorMessage string) string {
	// Create a wrapper capture if needed
	var capturedEvents *[]CapturedEvent
	if capture != nil {
		events := make([]CapturedEvent, 0)
		capturedEvents = &events
	}

	sw := NewStreamWriter(w, flusher, capturedEvents)

	// Send message_start with empty response
	sw.MessageStart(cfg.MessageID, cfg.Model, 0, 0)

	// Text block with error message
	sw.ContentBlockStart(0, "text", map[string]interface{}{"text": errorMessage})
	sw.ContentBlockStop(0)

	sw.MessageDelta("end_turn", "", map[string]int{"output_tokens": 0})
	sw.MessageStop()

	// Forward captured events
	if capture != nil && capturedEvents != nil {
		for _, e := range *capturedEvents {
			capture.Append(e.Event, e.Data)
		}
	}

	return errorMessage
}
