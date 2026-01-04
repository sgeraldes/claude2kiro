package parser

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"strings"
)

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

	// Track tool indices by tool ID for multi-tool responses
	toolIndices := make(map[string]int)
	nextToolIndex := 0    // Will be adjusted to 1 if text content is present
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

		var evt assistantResponseEvent
		if err := json.Unmarshal([]byte(payloadStr), &evt); err == nil {
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
		}
		// json unmarshal error - silently continue (for metering/context events)
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
