package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/sgeraldes/claude2kiro/parser"
)

// Kiro resets a response stream (HTTP/2 RST_STREAM, INTERNAL_ERROR) once it
// has sent nothing on it for 240 seconds. It also sends a tool call's input
// value only once the model has finished writing it: the name and the first
// key (a Write's file_path) arrive at once, then nothing, then the whole
// content in one burst. So a tool input that takes the model longer than 240 s
// to write (a large file in one Write call) never arrives. Measured on
// 2026-11-20, replaying one request straight against the backend: one send
// burst its content after 97 s of silence and finished; the same body sent ten
// minutes earlier stayed silent and was cut 240.0 s after its last frame. Cut
// requests were not charged.
//
// What the proxy does about a cut (network.backend_idle_cut_after decides what
// counts as one):
//
//   - Path 1: nothing reached the client yet (pings do not count). The same
//     request is sent once more, inside the proxy.
//   - Path 2: a tool call was open (content already went out, or the resend
//     was cut too) and advanced.idle_cut_note is on. The call is closed with
//     valid JSON built from the input that arrived and the response ends as a
//     tool call. The client runs it, it fails (or runs on a cut input), and the
//     note in idle_cut_note.go goes to Kiro with its tool result.
//   - Path 3: anything else. A stream fails as retryable (Claude Code resends
//     it as a non-streaming request); a non-streaming request, already resent
//     once, gets a 400 invalid_request_error that says what happened.

// lastReadReader records when its reader last returned data.
type lastReadReader struct {
	r    io.Reader
	last time.Time
}

func newLastReadReader(r io.Reader, start time.Time) *lastReadReader {
	return &lastReadReader{r: r, last: start}
}

func (l *lastReadReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	if n > 0 {
		l.last = time.Now()
	}
	return n, err
}

// silence is how long the reader has gone without data.
func (l *lastReadReader) silence() time.Duration { return time.Since(l.last) }

// isBackendIdleCut reports a response that broke after the backend had been
// silent for at least threshold (network.backend_idle_cut_after). A clean end
// and a malformed frame are not cuts.
func isBackendIdleCut(err error, silence, threshold time.Duration) bool {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, parser.ErrInvalidFrame) {
		return false
	}
	return silence >= threshold
}

// backendIdleCutMessage is what the client is told when a non-streaming
// request was cut, resent once and cut again. tool is the tool call the
// backend had started, or "" when it had started none.
func backendIdleCutMessage(silence time.Duration, tool string) string {
	secs := int(silence.Round(time.Second) / time.Second)
	head := fmt.Sprintf("Kiro closed the response after it had sent nothing for %d s (Kiro ends a response that stays silent for about 240 s). "+
		"The proxy sent the request once more and it was cut the same way.", secs)
	if tool != "" {
		return head + fmt.Sprintf(" The model had started the %q tool call, and Kiro sends a tool call's input only once it is complete, "+
			"so an input that takes longer than that to write never arrives. "+
			"Ask for the content in smaller pieces (for example, several smaller Write or Edit calls), then retry.", tool)
	}
	return head + " Ask for a smaller step, then retry."
}

// openToolCalls follows the tool_use blocks of a response as their events go
// by: their ids, names and the input fragments, and which are still open.
type openToolCalls struct {
	calls map[int]*openToolCall // by content block index
}

type openToolCall struct {
	id, name string
	input    strings.Builder
	open     bool
}

// blockIndex reads the index of a content block event.
func blockIndex(data map[string]any) (int, bool) {
	switch v := data["index"].(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	}
	return 0, false
}

func (o *openToolCalls) observe(e parser.SSEEvent) {
	data, ok := e.Data.(map[string]any)
	if !ok {
		return
	}
	index, ok := blockIndex(data)
	if !ok {
		return
	}
	switch e.Event {
	case "content_block_start":
		cb, _ := data["content_block"].(map[string]any)
		if cb == nil || cb["type"] != "tool_use" {
			return
		}
		if o.calls == nil {
			o.calls = map[int]*openToolCall{}
		}
		id, _ := cb["id"].(string)
		name, _ := cb["name"].(string)
		o.calls[index] = &openToolCall{id: id, name: name, open: true}
	case "content_block_delta":
		if c := o.calls[index]; c != nil {
			if delta, ok := data["delta"].(map[string]any); ok {
				if pj, ok := delta["partial_json"].(string); ok {
					c.input.WriteString(pj)
				}
			}
		}
	case "content_block_stop":
		if c := o.calls[index]; c != nil {
			c.open = false
		}
	}
}

func (o *openToolCalls) anyOpen() bool {
	for _, c := range o.calls {
		if c.open {
			return true
		}
	}
	return false
}

// lastName is the name of the open tool call with the highest index, or "".
func (o *openToolCalls) lastName() string {
	name, best := "", -1
	for i, c := range o.calls {
		if c.open && i > best {
			name, best = c.name, i
		}
	}
	return name
}

// completions returns, for every open tool call in index order, the
// input_json_delta that makes its input valid JSON (none when it already is
// or is empty), and the ids of those calls. It closes nothing: the
// translator's Finish closes the blocks.
func (o *openToolCalls) completions() ([]parser.SSEEvent, []string) {
	indices := make([]int, 0, len(o.calls))
	for i, c := range o.calls {
		if c.open {
			indices = append(indices, i)
		}
	}
	sort.Ints(indices)
	var events []parser.SSEEvent
	var ids []string
	for _, i := range indices {
		c := o.calls[i]
		ids = append(ids, c.id)
		if suffix := jsonCompletionSuffix(c.input.String()); suffix != "" {
			events = append(events, parser.SSEEvent{Event: "content_block_delta", Data: map[string]any{
				"type":  "content_block_delta",
				"index": i,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": suffix},
			}})
		}
	}
	return events, ids
}

// cutToolAnswer translates the complete frames of a response the backend
// cut, closes its open tool calls with valid JSON from the input that arrived
// and closes every block. ids are the calls it closed that way; with none
// open it returns no ids and the events are not meant to be used.
func cutToolAnswer(partial []byte) (events []parser.SSEEvent, ids []string, lastTool string) {
	tr := parser.NewStreamTranslator()
	var calls openToolCalls
	r := bytes.NewReader(partial)
	for {
		payload, err := parser.ReadFramePayload(r)
		if err != nil {
			break
		}
		for _, e := range tr.Translate(payload) {
			calls.observe(e)
			events = append(events, e)
		}
	}
	lastTool = calls.lastName()
	if !calls.anyOpen() {
		return events, nil, lastTool
	}
	completions, ids := calls.completions()
	events = append(events, completions...)
	events = append(events, tr.Finish()...)
	return events, ids, lastTool
}
