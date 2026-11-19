package parser

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"sort"
)

// ErrInvalidFrame reports a frame whose length fields cannot be right.
var ErrInvalidFrame = errors.New("invalid event-stream frame")

// maxFrameLen bounds a single frame so a corrupt length cannot make the proxy
// allocate without limit. Real frames are a few kilobytes.
const maxFrameLen = 16 << 20

// ReadFramePayload reads one CodeWhisperer event-stream frame from r and
// returns its payload, framed exactly as ParseEvents frames it. It returns
// io.EOF at a clean end of stream, io.ErrUnexpectedEOF when the stream ends
// inside a frame, and ErrInvalidFrame for impossible lengths.
func ReadFramePayload(r io.Reader) ([]byte, error) {
	var prelude [8]byte
	if _, err := io.ReadFull(r, prelude[:]); err != nil {
		return nil, err
	}
	totalLen := uint64(binary.BigEndian.Uint32(prelude[:4]))
	headerLen := uint64(binary.BigEndian.Uint32(prelude[4:]))
	if totalLen < headerLen+12 || totalLen > maxFrameLen {
		return nil, ErrInvalidFrame
	}

	rest := make([]byte, totalLen-8)
	if _, err := io.ReadFull(r, rest); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	payload := rest[headerLen : uint64(len(rest))-4] // drop the header and the trailing CRC
	return bytes.TrimPrefix(payload, []byte("vent")), nil
}

// StreamTranslator turns CodeWhisperer frames into Anthropic content-block
// events one frame at a time, so the proxy can forward a response while the
// backend is still producing it. Blocks get their index in arrival order and a
// text block is closed before a tool block starts. A tool block stays open
// until its stop frame or Finish, even when text arrives in between: that text
// gets its own block, so two blocks can be open at once. Clients follow blocks
// by index, and ParseEvents produced the same shape. It is not safe for
// concurrent use.
type StreamTranslator struct {
	nextIndex int
	textIndex int            // index of the open text block, -1 when none is open
	toolIndex map[string]int // toolUseId -> block index, for every tool seen
	openTools map[string]bool
	sawTool   bool
	emitted   bool

	backendStop    string // stopReason as the backend sent it
	reasoningChars int    // characters of reasoning text, never forwarded
}

func NewStreamTranslator() *StreamTranslator {
	return &StreamTranslator{textIndex: -1, toolIndex: map[string]int{}, openTools: map[string]bool{}}
}

// Translate returns the events one frame payload produces. Frames that carry
// no assistant content (reasoning, signature, stop reason, context usage,
// metering, unparseable payloads) produce none; the stop reason and the size
// of the reasoning are recorded.
func (t *StreamTranslator) Translate(payload []byte) []SSEEvent {
	var evt assistantResponseEvent
	if err := json.Unmarshal(payload, &evt); err != nil {
		return nil
	}
	t.reasoningChars += evt.reasoningChars()
	if evt.StopReason != "" {
		t.backendStop = evt.StopReason
	}

	var out []SSEEvent
	switch {
	case evt.Content != "":
		if t.textIndex < 0 {
			// An open tool block stays open: its input may still arrive after
			// this text, and closing it would lose that input.
			t.textIndex = t.nextIndex
			t.nextIndex++
			out = append(out, SSEEvent{Event: "content_block_start", Data: map[string]any{
				"type":          "content_block_start",
				"index":         t.textIndex,
				"content_block": map[string]any{"type": "text", "text": ""},
			}})
		}
		out = append(out, SSEEvent{Event: "content_block_delta", Data: map[string]any{
			"type":  "content_block_delta",
			"index": t.textIndex,
			"delta": map[string]any{"type": "text_delta", "text": evt.Content},
		}})

	case evt.ToolUseId != "" && evt.Name != "" && !evt.Stop:
		index, known := t.toolIndex[evt.ToolUseId]
		if known && !t.openTools[evt.ToolUseId] {
			return nil // input after the tool's own stop frame: its block is closed
		}
		if !known {
			out = append(out, t.closeText()...)
			index = t.nextIndex
			t.nextIndex++
			t.toolIndex[evt.ToolUseId] = index
			t.openTools[evt.ToolUseId] = true
			t.sawTool = true
			out = append(out, SSEEvent{Event: "content_block_start", Data: map[string]any{
				"type":  "content_block_start",
				"index": index,
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    evt.ToolUseId,
					"name":  evt.Name,
					"input": map[string]any{},
				},
			}})
		}
		if evt.Input != nil {
			out = append(out, SSEEvent{Event: "content_block_delta", Data: map[string]any{
				"type":  "content_block_delta",
				"index": index,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": *evt.Input},
			}})
		}

	case evt.Stop && evt.ToolUseId != "":
		if t.openTools[evt.ToolUseId] {
			out = append(out, t.closeTool(evt.ToolUseId))
		}
	}

	if len(out) > 0 {
		t.emitted = true
	}
	return out
}

// Finish closes whatever block the backend left open at the end of the stream.
func (t *StreamTranslator) Finish() []SSEEvent {
	return append(t.closeText(), t.closeTools()...)
}

// StopReason is the Anthropic stop_reason for the response translated so far:
// the backend's stopReason when it sent a known one, else tool_use when a tool
// was used and end_turn otherwise.
func (t *StreamTranslator) StopReason() string {
	return AnthropicStopReason(t.backendStop, t.sawTool)
}

// BackendStopReason is the stopReason the backend sent, "" when none.
func (t *StreamTranslator) BackendStopReason() string { return t.backendStop }

// ReasoningChars is the number of reasoning characters the backend sent.
func (t *StreamTranslator) ReasoningChars() int { return t.reasoningChars }

// Emitted reports whether any frame produced an event.
func (t *StreamTranslator) Emitted() bool { return t.emitted }

func blockStop(index int) SSEEvent {
	return SSEEvent{Event: "content_block_stop", Data: map[string]any{"type": "content_block_stop", "index": index}}
}

func (t *StreamTranslator) closeText() []SSEEvent {
	if t.textIndex < 0 {
		return nil
	}
	stop := blockStop(t.textIndex)
	t.textIndex = -1
	return []SSEEvent{stop}
}

func (t *StreamTranslator) closeTool(id string) SSEEvent {
	delete(t.openTools, id)
	return blockStop(t.toolIndex[id])
}

func (t *StreamTranslator) closeTools() []SSEEvent {
	if len(t.openTools) == 0 {
		return nil
	}
	ids := make([]string, 0, len(t.openTools))
	for id := range t.openTools {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return t.toolIndex[ids[a]] < t.toolIndex[ids[b]] })
	out := make([]SSEEvent, 0, len(ids))
	for _, id := range ids {
		out = append(out, t.closeTool(id))
	}
	return out
}
