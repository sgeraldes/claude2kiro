package parser

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DebugInfo holds debug information for a single binary frame
type DebugInfo struct {
	FrameIndex     int    `json:"frame_index"`
	TotalLen       uint32 `json:"total_len"`
	HeaderLen      uint32 `json:"header_len"`
	PayloadLen     int    `json:"payload_len"`
	RawPayloadHex  string `json:"raw_payload_hex"`
	RawPayloadStr  string `json:"raw_payload_str"`
	AfterTrimStr   string `json:"after_trim_str"`
	ParsedEvent    any    `json:"parsed_event,omitempty"`
	ParseError     string `json:"parse_error,omitempty"`
	HasToolInput   bool   `json:"has_tool_input"`
	ToolInputValue string `json:"tool_input_value,omitempty"`
}

// ParseDebugInfo holds all debug info for a parse operation
type ParseDebugInfo struct {
	Timestamp   string      `json:"timestamp"`
	TotalBytes  int         `json:"total_bytes"`
	FrameCount  int         `json:"frame_count"`
	EventCount  int         `json:"event_count"`
	Frames      []DebugInfo `json:"frames"`
	FinalEvents []SSEEvent  `json:"final_events"`
}

// Global debug flag - can be set externally
var DebugMode = false

// writeDebugFile writes debug info to a file in the debug directory
func writeDebugFile(info *ParseDebugInfo) {
	debugDir := filepath.Join(os.TempDir(), "claude2kiro-debug")
	os.MkdirAll(debugDir, 0700)

	filename := fmt.Sprintf("parser-debug-%s.json", time.Now().Format("20060102-150405.000"))
	filePath := filepath.Join(debugDir, filename)

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(filePath, data, 0600)
}

type assistantResponseEvent struct {
	Content   string  `json:"content"`
	Input     *string `json:"input,omitempty"`
	Name      string  `json:"name"`
	ToolUseId string  `json:"toolUseId"`
	Stop      bool    `json:"stop"`
}

type SSEEvent struct {
	Event string      `json:"event"`
	Data  interface{} `json:"data"`
}

func ParseEvents(resp []byte) []SSEEvent {
	events := []SSEEvent{}

	// Debug info collection
	var debugInfo *ParseDebugInfo
	if DebugMode {
		debugInfo = &ParseDebugInfo{
			Timestamp:  time.Now().Format(time.RFC3339Nano),
			TotalBytes: len(resp),
			Frames:     []DebugInfo{},
		}
	}
	frameIndex := 0

	// Track tool indices by tool ID for multi-tool responses
	toolIndices := make(map[string]int)
	nextToolIndex := 0 // Will be adjusted to 1 if text content is present
	hasToolUse := false
	hasTextContent := false

	r := bytes.NewReader(resp)
	for {
		if r.Len() < 12 {
			break
		}

		var totalLen, headerLen uint32
		if err := binary.Read(r, binary.BigEndian, &totalLen); err != nil {
			break
		}
		if err := binary.Read(r, binary.BigEndian, &headerLen); err != nil {
			break
		}

		if int(totalLen) > r.Len()+8 {
			// Frame length invalid - silently break
			break
		}

		// Skip header
		header := make([]byte, headerLen)
		if _, err := io.ReadFull(r, header); err != nil {
			break
		}

		payloadLen := int(totalLen) - int(headerLen) - 12
		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(r, payload); err != nil {
			break
		}

		// Skip CRC32
		if _, err := r.Seek(4, io.SeekCurrent); err != nil {
			break
		}

		payloadStr := strings.TrimPrefix(string(payload), "vent")

		// Collect debug info for this frame
		var frameDebug *DebugInfo
		if DebugMode {
			frameDebug = &DebugInfo{
				FrameIndex:    frameIndex,
				TotalLen:      totalLen,
				HeaderLen:     headerLen,
				PayloadLen:    payloadLen,
				RawPayloadHex: hex.EncodeToString(payload),
				RawPayloadStr: string(payload),
				AfterTrimStr:  payloadStr,
			}
			frameIndex++
		}

		var evt assistantResponseEvent
		if err := json.Unmarshal([]byte(payloadStr), &evt); err == nil {
			// Debug: capture parsed event
			if frameDebug != nil {
				frameDebug.ParsedEvent = evt
				if evt.Input != nil {
					frameDebug.HasToolInput = true
					frameDebug.ToolInputValue = *evt.Input
				}
			}

			// Track if we have text content - this affects tool indexing
			if evt.Content != "" {
				hasTextContent = true
				// If we haven't assigned any tool indices yet, bump to 1
				// so tools don't conflict with text at index 0
				if len(toolIndices) == 0 && nextToolIndex == 0 {
					nextToolIndex = 1
				}
			}

			sseEvent := convertAssistantEventToSSEWithIndex(evt, toolIndices, &nextToolIndex)
			if sseEvent.Event != "" {
				events = append(events, sseEvent)
			}

			if evt.ToolUseId != "" && evt.Name != "" {
				hasToolUse = true
			}
		} else {
			// Debug: capture parse error
			if frameDebug != nil {
				frameDebug.ParseError = err.Error()
			}
		}

		// Add frame debug info
		if frameDebug != nil {
			debugInfo.Frames = append(debugInfo.Frames, *frameDebug)
		}
	}

	// Add a single message_delta at the end for ALL responses
	// - Tool responses get stop_reason: "tool_use"
	// - Text-only responses get stop_reason: "end_turn"
	// Note: hasTextContent is used above to adjust tool indices, ensuring tools start at index 1
	// when there's text content at index 0
	if len(events) > 0 {
		_ = hasTextContent // Used for index adjustment above
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

	// Write debug info if enabled
	if debugInfo != nil {
		debugInfo.FrameCount = frameIndex
		debugInfo.EventCount = len(events)
		debugInfo.FinalEvents = events
		writeDebugFile(debugInfo)
	}

	return events
}

func convertAssistantEventToSSEWithIndex(evt assistantResponseEvent, toolIndices map[string]int, nextToolIndex *int) SSEEvent {
	if evt.Content != "" {
		return SSEEvent{
			Event: "content_block_delta",
			Data: map[string]interface{}{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": evt.Content,
				},
			},
		}
	} else if evt.ToolUseId != "" && evt.Name != "" && !evt.Stop {
		// Get or assign index for this tool
		toolIndex, exists := toolIndices[evt.ToolUseId]
		if !exists {
			toolIndex = *nextToolIndex
			toolIndices[evt.ToolUseId] = toolIndex
			*nextToolIndex++
		}

		if evt.Input == nil {
			// First event for this tool - content_block_start
			return SSEEvent{
				Event: "content_block_start",
				Data: map[string]interface{}{
					"type":  "content_block_start",
					"index": toolIndex,
					"content_block": map[string]interface{}{
						"type":  "tool_use",
						"id":    evt.ToolUseId,
						"name":  evt.Name,
						"input": map[string]interface{}{},
					},
				},
			}
		} else {
			// Subsequent events - input_json_delta
			return SSEEvent{
				Event: "content_block_delta",
				Data: map[string]interface{}{
					"type":  "content_block_delta",
					"index": toolIndex,
					"delta": map[string]interface{}{
						"type":         "input_json_delta",
						"partial_json": *evt.Input,
					},
				},
			}
		}
	} else if evt.Stop && evt.ToolUseId != "" {
		// Tool stop event
		toolIndex, exists := toolIndices[evt.ToolUseId]
		if !exists {
			toolIndex = 0 // Fallback, shouldn't happen
		}
		return SSEEvent{
			Event: "content_block_stop",
			Data: map[string]interface{}{
				"type":  "content_block_stop",
				"index": toolIndex,
			},
		}
	}

	return SSEEvent{}
}
