package parser

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sgeraldes/claude2kiro/internal/debug"
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

// writeDebugFile writes debug info to the secure debug directory
func writeDebugFile(info *ParseDebugInfo) {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return
	}
	// Use secure debug directory (~/.claude2kiro/debug/) with random filename
	debug.WriteDebugFile("parser-debug", data)
}

type assistantResponseEvent struct {
	Content   string  `json:"content"`
	Input     *string `json:"input,omitempty"`
	Name      string  `json:"name"`
	ToolUseId string  `json:"toolUseId"`
	Stop      bool    `json:"stop"`
	// Reasoning is the text of a reasoningContentEvent ({"text":"..."}), and
	// Signature and RedactedContent are its other two frame kinds. They are
	// kept for the next request, never forwarded to the client.
	Reasoning       string          `json:"text,omitempty"`
	Signature       string          `json:"signature,omitempty"`
	RedactedContent json.RawMessage `json:"redactedContent,omitempty"`
	// ModelId is the model the backend says produced the frame, when it says.
	ModelId string `json:"modelId,omitempty"`
	// StopReason is the backend's reason for ending the response, sent in a
	// frame of its own ({"stopReason":"TOOL_USE"}).
	StopReason string `json:"stopReason,omitempty"`
}

// reasoningChars is the number of reasoning characters a frame carries. A
// frame with assistant content or a tool is not a reasoning frame.
func (e assistantResponseEvent) reasoningChars() int {
	if e.Content != "" || e.ToolUseId != "" {
		return 0
	}
	return utf8.RuneCountInString(e.Reasoning)
}

// backendStopReasons maps the backend's stopReason values to the Anthropic
// stop_reason.
var backendStopReasons = map[string]string{
	"END_TURN":      "end_turn",
	"TOOL_USE":      "tool_use",
	"MAX_TOKENS":    "max_tokens",
	"STOP_SEQUENCE": "stop_sequence",
}

// AnthropicStopReason maps the backend's stopReason to the Anthropic
// stop_reason. An absent or unknown value falls back to tool_use when the
// response used a tool, else end_turn.
func AnthropicStopReason(backend string, sawTool bool) string {
	if mapped, ok := backendStopReasons[strings.ToUpper(strings.TrimSpace(backend))]; ok {
		return mapped
	}
	if sawTool {
		return "tool_use"
	}
	return "end_turn"
}

// ResponseSummary is what a response says besides its content blocks.
type ResponseSummary struct {
	StopReason        string // Anthropic stop_reason for the response
	BackendStopReason string // stopReason as the backend sent it; "" when it sent none
	ReasoningChars    int    // characters of reasoning text the backend sent

	Reasoning   ReasoningBlock // the response's last reasoning block
	ToolUseIDs  []string       // tool use ids in the order the response opened them
	Text        string         // the assistant text, concatenated
	ServedModel string         // the last modelId a frame carried; "" when none did
}

type SSEEvent struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

type MeteringEvent struct {
	Unit       string  `json:"unit"`
	UnitPlural string  `json:"unitPlural,omitempty"`
	Usage      float64 `json:"usage"`
}

func ParseMeteringEvents(resp []byte) []MeteringEvent {
	var events []MeteringEvent

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
		if totalLen < headerLen+12 || int(totalLen) > r.Len()+8 {
			break
		}

		header := make([]byte, headerLen)
		if _, err := io.ReadFull(r, header); err != nil {
			break
		}

		payloadLen := int(totalLen) - int(headerLen) - 12
		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(r, payload); err != nil {
			break
		}

		if _, err := r.Seek(4, io.SeekCurrent); err != nil {
			break
		}

		payloadStr := strings.TrimPrefix(string(payload), "vent")
		var raw struct {
			Unit       string   `json:"unit"`
			UnitPlural string   `json:"unitPlural"`
			Usage      *float64 `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payloadStr), &raw); err != nil {
			continue
		}
		if raw.Unit != "credit" || raw.Usage == nil {
			continue
		}
		events = append(events, MeteringEvent{
			Unit:       raw.Unit,
			UnitPlural: raw.UnitPlural,
			Usage:      *raw.Usage,
		})
	}

	return events
}

// ParseEvents translates a whole CodeWhisperer response into Anthropic SSE
// events. See ParseEventsWithSummary.
func ParseEvents(resp []byte) []SSEEvent {
	events, _ := ParseEventsWithSummary(resp)
	return events
}

// ParseEventsWithSummary translates a whole CodeWhisperer response into
// Anthropic SSE events and also reports the stop reason and how much reasoning
// the backend produced, and the last reasoning block, tool use ids, text and
// served model (see ResponseSummary). Reasoning, signature, redacted,
// context-usage and metering frames produce no events.
func ParseEventsWithSummary(resp []byte) ([]SSEEvent, ResponseSummary) {
	events := []SSEEvent{}
	var summary ResponseSummary

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

	indices := newBlockIndexer()
	hasToolUse := false
	var collector responseCollector

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

			sseEvent := convertAssistantEventToSSEWithIndex(evt, indices)
			if sseEvent.Event != "" {
				events = append(events, sseEvent)
			}

			if evt.ToolUseId != "" && evt.Name != "" {
				hasToolUse = true
			}
			summary.ReasoningChars += evt.reasoningChars()
			collector.observe(evt)
			if evt.StopReason != "" {
				summary.BackendStopReason = evt.StopReason
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

	// Add a single message_delta at the end for every response with content.
	// It carries the backend's stop reason; without one, tool responses get
	// "tool_use" and text-only responses "end_turn".
	summary.StopReason = AnthropicStopReason(summary.BackendStopReason, hasToolUse)
	summary.Reasoning = collector.reasoning
	summary.ToolUseIDs = collector.toolUseIDs
	summary.Text = collector.text.String()
	summary.ServedModel = collector.servedModel
	if len(events) > 0 {
		events = append(events, SSEEvent{
			Event: "message_delta",
			Data: map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason":   summary.StopReason,
					"stop_sequence": nil,
				},
				"usage": map[string]any{"output_tokens": 0},
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

	return events, summary
}

// blockIndexer gives content blocks their index in arrival order, as
// StreamTranslator does: a run of text gets the next index when it starts,
// and each tool call gets the next index when it is first seen and ends the
// text run, so text after a tool starts a new block.
type blockIndexer struct {
	next  int
	text  int // index of the current text run, -1 when none
	tools map[string]int
}

func newBlockIndexer() *blockIndexer {
	return &blockIndexer{text: -1, tools: map[string]int{}}
}

func (b *blockIndexer) textIndex() int {
	if b.text < 0 {
		b.text = b.next
		b.next++
	}
	return b.text
}

func (b *blockIndexer) toolIndex(id string) int {
	if index, known := b.tools[id]; known {
		return index
	}
	b.text = -1
	index := b.next
	b.next++
	b.tools[id] = index
	return index
}

func convertAssistantEventToSSEWithIndex(evt assistantResponseEvent, indices *blockIndexer) SSEEvent {
	if evt.Content != "" {
		return SSEEvent{
			Event: "content_block_delta",
			Data: map[string]any{
				"type":  "content_block_delta",
				"index": indices.textIndex(),
				"delta": map[string]any{
					"type": "text_delta",
					"text": evt.Content,
				},
			},
		}
	} else if evt.ToolUseId != "" && evt.Name != "" && !evt.Stop {
		toolIndex := indices.toolIndex(evt.ToolUseId)

		if evt.Input == nil {
			// First event for this tool - content_block_start
			return SSEEvent{
				Event: "content_block_start",
				Data: map[string]any{
					"type":  "content_block_start",
					"index": toolIndex,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    evt.ToolUseId,
						"name":  evt.Name,
						"input": map[string]any{},
					},
				},
			}
		} else {
			// Subsequent events - input_json_delta
			return SSEEvent{
				Event: "content_block_delta",
				Data: map[string]any{
					"type":  "content_block_delta",
					"index": toolIndex,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": *evt.Input,
					},
				},
			}
		}
	} else if evt.Stop && evt.ToolUseId != "" {
		// Tool stop event
		toolIndex, exists := indices.tools[evt.ToolUseId]
		if !exists {
			toolIndex = 0 // Fallback, shouldn't happen
		}
		return SSEEvent{
			Event: "content_block_stop",
			Data: map[string]any{
				"type":  "content_block_stop",
				"index": toolIndex,
			},
		}
	}

	return SSEEvent{}
}
