package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/kiroide"
	"github.com/sgeraldes/claude2kiro/internal/reasoning"
	"github.com/sgeraldes/claude2kiro/parser"
)

// Recovery from an interrupted response, as the Kiro clients do it (CLI 3.5.0
// and IDE share one agent core; see backend_idle_cut.go for the cut itself).
//
// The proxy holds every tool_use block of a response back until the response
// has ended normally; text streams as it arrives. Kiro sends a tool call's
// input in one burst once it is complete, and the client can only run a tool
// after the message ends, so holding costs the client nothing it could use.
// When a response is interrupted (Kiro's idle cut, a reset stream) or stops at
// MAX_TOKENS with a tool call still open, nothing incomplete reaches the
// client:
//
//   - no tool frame yet: the same request is sent again after about 500 ms; if
//     text was already forwarded, the request is extended with that text as an
//     assistant turn and a user turn asking to continue;
//   - a tool frame arrived: the request is extended with an assistant turn
//     (the text and every tool call of the interrupted attempt) and a user turn
//     answering each tool call with a failed tool result whose text says what
//     happened and how to split the work, as Kiro's own client does;
//
// and the new answer streams to the client as the continuation of the same
// message. At most maxRecoveries times per response; after that the client
// gets a clear error. The exchange never reaches the client's transcript, so
// the proxy keeps it (recoveredTurns) and puts it back into the history of
// every later request of the conversation, before the assistant message the
// client recorded for that response.

// maxRecoveries caps the recoveries of one response, like Kiro's client (3).
const maxRecoveries = 3

// continueAfterInterruption is the user turn after an interrupted text-only
// attempt (Kiro's wording).
const continueAfterInterruption = "The previous response was interrupted. Continue from the recorded response; do not repeat completed work."

// splitWriteGuidance is Kiro's split guidance, adapted to Claude Code's tools
// (Write and Edit; there is no append).
const splitWriteGuidance = "Retry with less input per call: write the first part of the file with Write (at most about 50 lines), " +
	"then add the rest with several small Edit calls. Do not repeat parts already written successfully by earlier calls."

// interruptedGenericResult is the tool result of an interrupted call that is
// not a large file write.
const interruptedGenericResult = "The tool was NOT executed because its input stream was interrupted. Review the completed tool results and decide the next step."

// fileWriteTools are the client tools whose interrupted input gets the split
// guidance (Kiro: fs_write, fs_append, str_replace).
var fileWriteTools = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true}

// heldTool is a tool call of an attempt: its name, id and the input that
// arrived, and whether its block was closed (its input complete).
type heldTool struct {
	id, name string
	input    strings.Builder
	complete bool
	events   []parser.SSEEvent
}

// interruptedResultText is the tool result the proxy answers an interrupted
// tool call with. truncated is a MAX_TOKENS stop; idleCut is Kiro's idle cut.
// The cut tool is the one still open, any other call of the attempt was
// complete but not executed.
//
// Kiro's client gives the split guidance to a file write only after 2,000
// argument characters arrived. Kiro holds a Write's content back until it is
// complete, so after an idle cut only the file_path has arrived (128
// characters, measured live on 2026-11-20), and with the generic text the
// model sent the same whole-file Write again. A file write cut by the idle
// limit therefore gets the guidance whatever arrived of it.
func interruptedResultText(t *heldTool, truncated, idleCut bool) string {
	if t.complete {
		return interruptedGenericResult
	}
	if truncated {
		return "The model hit its output token limit before this tool call finished streaming, so the call was truncated and the tool was NOT executed. " +
			"Tool call arguments are generated as model output and count against the model's output token limit, " +
			"so retrying this call with the same input will be truncated again. " + splitWriteGuidance
	}
	chars := t.input.Len()
	if fileWriteTools[t.name] && (chars >= 2000 || idleCut) {
		path := "the file"
		var in map[string]any
		if json.Unmarshal([]byte(t.input.String()+jsonCompletionSuffix(t.input.String())), &in) == nil {
			if p, ok := in["file_path"].(string); ok && p != "" {
				path = p
			}
		}
		if chars < 2000 {
			return fmt.Sprintf("The connection was interrupted while this tool call was still producing its content for %s: "+
				"the backend closes a response after about 240 seconds without output, and the input of this call took longer than that to produce. "+
				"This interrupted call did not write anything. %s", path, splitWriteGuidance)
		}
		return fmt.Sprintf("The connection was interrupted while this tool call was still streaming its content for %s; about %d argument characters had arrived. "+
			"This interrupted call did not write anything. %s", path, chars, splitWriteGuidance)
	}
	return interruptedGenericResult
}

// blockEmitter sits between a stream translator and the client. Text and
// other blocks go out at once; tool_use blocks are held until flushTools.
// Block indices are given in the order the client receives the blocks, across
// every attempt of the response.
type blockEmitter struct {
	emit        func(parser.SSEEvent)
	next        int         // next client block index
	clientIndex map[int]int // this attempt's translator index -> client index
	openBlocks  map[int]bool
	tools       map[int]*heldTool // this attempt's tool calls, by translator index
	toolOrder   []int
	text        strings.Builder // text forwarded in this attempt
}

func newBlockEmitter(emit func(parser.SSEEvent)) *blockEmitter {
	return &blockEmitter{emit: emit, clientIndex: map[int]int{}, openBlocks: map[int]bool{}, tools: map[int]*heldTool{}}
}

func withIndex(e parser.SSEEvent, index int) parser.SSEEvent {
	data, ok := e.Data.(map[string]any)
	if !ok {
		return e
	}
	copied := make(map[string]any, len(data))
	for k, v := range data {
		copied[k] = v
	}
	copied["index"] = index
	return parser.SSEEvent{Event: e.Event, Data: copied}
}

// push takes the events a translator produced for one frame.
func (b *blockEmitter) push(events []parser.SSEEvent) {
	for _, e := range events {
		data, _ := e.Data.(map[string]any)
		idx, hasIndex := blockIndex(data)
		if !hasIndex {
			b.emit(e)
			continue
		}
		if t := b.tools[idx]; t != nil {
			t.events = append(t.events, e)
			switch e.Event {
			case "content_block_delta":
				if delta, ok := data["delta"].(map[string]any); ok {
					if pj, ok := delta["partial_json"].(string); ok {
						t.input.WriteString(pj)
					}
				}
			case "content_block_stop":
				t.complete = true
			}
			continue
		}
		if e.Event == "content_block_start" {
			if cb, ok := data["content_block"].(map[string]any); ok && cb["type"] == "tool_use" {
				t := &heldTool{events: []parser.SSEEvent{e}}
				t.id, _ = cb["id"].(string)
				t.name, _ = cb["name"].(string)
				b.tools[idx] = t
				b.toolOrder = append(b.toolOrder, idx)
				continue
			}
			b.clientIndex[idx] = b.next
			b.openBlocks[b.next] = true
			b.next++
		}
		ci, known := b.clientIndex[idx]
		if !known {
			continue // a delta or stop for a block this attempt never started
		}
		if delta, ok := data["delta"].(map[string]any); ok {
			if text, ok := delta["text"].(string); ok {
				b.text.WriteString(text)
			}
		}
		if e.Event == "content_block_stop" {
			delete(b.openBlocks, ci)
		}
		b.emit(withIndex(e, ci))
	}
}

// toolSeen reports a tool frame in this attempt.
func (b *blockEmitter) toolSeen() bool { return len(b.toolOrder) > 0 }

// openTool reports a tool call of this attempt whose input is not complete.
func (b *blockEmitter) openTool() bool {
	for _, i := range b.toolOrder {
		if !b.tools[i].complete {
			return true
		}
	}
	return false
}

// heldTools are this attempt's tool calls in arrival order.
func (b *blockEmitter) heldTools() []*heldTool {
	out := make([]*heldTool, 0, len(b.toolOrder))
	for _, i := range b.toolOrder {
		out = append(out, b.tools[i])
	}
	return out
}

// flushTools sends the held tool calls, each with the next client index.
func (b *blockEmitter) flushTools() {
	for _, i := range b.toolOrder {
		ci := b.next
		b.next++
		for _, e := range b.tools[i].events {
			b.emit(withIndex(e, ci))
		}
	}
	b.tools = map[int]*heldTool{}
	b.toolOrder = nil
}

// endAttempt closes the blocks the client has open, drops the held tool
// calls and starts the bookkeeping of a new attempt (a new translator, whose
// indices start at 0 again). It returns the attempt's text and tool calls.
func (b *blockEmitter) endAttempt() (string, []*heldTool) {
	text, tools := b.text.String(), b.heldTools()
	for ci := range b.openBlocks {
		b.emit(parser.SSEEvent{Event: "content_block_stop", Data: map[string]any{"type": "content_block_stop", "index": ci}})
	}
	b.openBlocks = map[int]bool{}
	b.clientIndex = map[int]int{}
	b.tools = map[int]*heldTool{}
	b.toolOrder = nil
	b.text.Reset()
	return text, tools
}

// recoveryTurns returns the assistant turn of an interrupted attempt (its
// text and tool calls, inputs completed into valid JSON) and the user turn
// that answers it: a failed tool result per tool call, or the request to
// continue when the attempt had no tool call.
func recoveryTurns(text string, tools []*heldTool, truncated, idleCut bool) []AnthropicRequestMessage {
	var assistant []any
	if strings.TrimSpace(text) != "" {
		assistant = append(assistant, map[string]any{"type": "text", "text": text})
	}
	var user []any
	for _, t := range tools {
		input := map[string]any{}
		raw := t.input.String()
		if raw != "" {
			if err := json.Unmarshal([]byte(raw+jsonCompletionSuffix(raw)), &input); err != nil {
				input = map[string]any{}
			}
		}
		assistant = append(assistant, map[string]any{"type": "tool_use", "id": t.id, "name": t.name, "input": input})
		user = append(user, map[string]any{"type": "tool_result", "tool_use_id": t.id, "is_error": true,
			"content": interruptedResultText(t, truncated, idleCut)})
	}
	if len(tools) == 0 {
		user = append(user, map[string]any{"type": "text", "text": continueAfterInterruption})
	}
	if len(assistant) == 0 {
		assistant = append(assistant, map[string]any{"type": "text", "text": "..."})
	}
	return []AnthropicRequestMessage{{Role: "assistant", Content: assistant}, {Role: "user", Content: user}}
}

// extendedRequest is req with turns appended after its messages.
func extendedRequest(req AnthropicRequest, turns []AnthropicRequestMessage) AnthropicRequest {
	out := req
	out.Messages = append(append([]AnthropicRequestMessage(nil), req.Messages...), turns...)
	return out
}

// recoveredExchange is what the client never saw of one response: the turns
// to put back before the assistant message it recorded, and the texts that
// message starts with which belong to those turns instead.
type recoveredExchange struct {
	Turns     []AnthropicRequestMessage
	LeadTexts []string
}

// recoveredTurns keeps the exchanges, per conversation, bounded and expiring.
var recoveredTurns = newRecoveredStore(256, 6*time.Hour)

type recoveredEntry struct {
	ex    recoveredExchange
	added time.Time
}

type recoveredStore struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	entries map[string]recoveredEntry
	now     func() time.Time
}

func newRecoveredStore(max int, ttl time.Duration) *recoveredStore {
	return &recoveredStore{max: max, ttl: ttl, entries: map[string]recoveredEntry{}, now: time.Now}
}

// recoveryKeys identify the assistant message a client records for a
// response: its tool use ids, or, without any, its text.
func recoveryKeys(scope string, toolIDs []string, text string) []string {
	if len(toolIDs) > 0 {
		keys := make([]string, 0, len(toolIDs))
		for _, id := range toolIDs {
			keys = append(keys, scope+"\x00tool\x00"+id)
		}
		return keys
	}
	norm := reasoning.NormalizeText(text)
	if norm == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(norm))
	return []string{scope + "\x00text\x00" + hex.EncodeToString(sum[:])}
}

func (s *recoveredStore) put(keys []string, ex recoveredExchange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, e := range s.entries {
		if now.Sub(e.added) > s.ttl {
			delete(s.entries, k)
		}
	}
	for _, k := range keys {
		s.entries[k] = recoveredEntry{ex: ex, added: now}
	}
	for len(s.entries) > s.max {
		oldestKey, oldest := "", now
		for k, e := range s.entries {
			if oldestKey == "" || e.added.Before(oldest) {
				oldestKey, oldest = k, e.added
			}
		}
		delete(s.entries, oldestKey)
	}
}

func (s *recoveredStore) get(keys []string) (recoveredExchange, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if e, ok := s.entries[k]; ok && s.now().Sub(e.added) <= s.ttl {
			return e.ex, true
		}
	}
	return recoveredExchange{}, false
}

func (s *recoveredStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// recoveryScope is the store scope of req's conversation (as the reasoning
// store scopes it).
func recoveryScope(req AnthropicRequest) string {
	scope := reasoningScope{}
	if _, session := extractSessionID(req.Metadata); session != "" {
		scope.session = session
		scope.conversation = conversationFingerprint(req.Messages)
	}
	return scope.storeScope()
}

// messageToolIDsAndText reads an assistant message's tool use ids and text.
func messageToolIDsAndText(content any) ([]string, string) {
	var ids []string
	var text strings.Builder
	switch v := content.(type) {
	case string:
		text.WriteString(v)
	case []any:
		for _, block := range v {
			m, ok := block.(map[string]any)
			if !ok {
				continue
			}
			switch m["type"] {
			case "tool_use":
				if id, ok := m["id"].(string); ok {
					ids = append(ids, id)
				}
			case "text":
				if s, ok := m["text"].(string); ok {
					text.WriteString(s)
				}
			}
		}
	}
	return ids, text.String()
}

// dropLeadTexts removes from content the leading text blocks equal to texts,
// in order, and stops at the first that differs.
func dropLeadTexts(content any, texts []string) any {
	blocks, ok := content.([]any)
	if !ok || len(texts) == 0 {
		return content
	}
	i := 0
	for i < len(blocks) && i < len(texts) {
		m, ok := blocks[i].(map[string]any)
		if !ok || m["type"] != "text" || reasoning.NormalizeText(fmt.Sprint(m["text"])) != reasoning.NormalizeText(texts[i]) {
			break
		}
		i++
	}
	if i == len(blocks) {
		return blocks // never leave the message empty
	}
	return blocks[i:]
}

// withRecoveredTurns puts every recovered exchange of req's conversation back
// before the assistant message the client recorded for that response. It
// returns req unchanged when there is none.
func withRecoveredTurns(req AnthropicRequest) (AnthropicRequest, int) {
	if recoveredTurns.len() == 0 {
		return req, 0
	}
	scope := recoveryScope(req)
	var out []AnthropicRequestMessage
	inserted := 0
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			ids, text := messageToolIDsAndText(m.Content)
			if ex, ok := recoveredTurns.get(recoveryKeys(scope, ids, text)); ok {
				out = append(out, ex.Turns...)
				m = AnthropicRequestMessage{Role: m.Role, Content: dropLeadTexts(m.Content, ex.LeadTexts)}
				inserted++
			}
		}
		out = append(out, m)
	}
	if inserted == 0 {
		return req, 0
	}
	req.Messages = out
	return req, inserted
}

// How an attempt ended, for recovery.
const (
	interruptNone      = iota // finished, or broken in a way recovery does not cover
	interruptResend           // interrupted before any tool frame
	interruptTool             // interrupted after a tool frame
	interruptTruncated        // MAX_TOKENS with a tool call still open
)

// interruptionKind classifies an attempt from its read error, its translator
// and the tool calls the emitter saw.
func interruptionKind(readErr error, silence, idleCutAfter time.Duration, tr *parser.StreamTranslator, em *blockEmitter) int {
	if readErr == nil {
		if tr.StopReason() == "max_tokens" && em.openTool() {
			return interruptTruncated
		}
		return interruptNone
	}
	// A malformed frame is a broken answer. So is a body that ends inside a
	// frame at once; after Kiro's idle silence it is the cut (over HTTP/1.1 a
	// reset connection reads that way).
	if errors.Is(readErr, parser.ErrInvalidFrame) {
		return interruptNone
	}
	if errors.Is(readErr, io.ErrUnexpectedEOF) && !isBackendIdleCut(readErr, silence, idleCutAfter) {
		return interruptNone
	}
	if em.toolSeen() {
		return interruptTool
	}
	return interruptResend
}

// interruptionCause names the cause for the log.
func interruptionCause(kind int, readErr error, silence, idleCutAfter time.Duration) string {
	switch {
	case kind == interruptTruncated:
		return "MAX_TOKENS with a tool call open"
	case isBackendIdleCut(readErr, silence, idleCutAfter):
		return "Kiro's idle cut"
	default:
		return "stream broke"
	}
}

// recoveryPath is the path number the log names: 1 for a plain resend, 2 for
// a resend that carries the interrupted attempt.
func recoveryPath(kind int, text string) string {
	if kind == interruptResend && strings.TrimSpace(text) == "" {
		return "1"
	}
	return "2"
}

// describeRecovery says for the log what a path 2 recovery sent.
func describeRecovery(tools []*heldTool) string {
	if len(tools) == 0 {
		return "continuing after the text already sent"
	}
	parts := make([]string, 0, len(tools))
	for _, t := range tools {
		state := "complete"
		if !t.complete {
			state = fmt.Sprintf("cut at %d chars", t.input.Len())
		}
		parts = append(parts, fmt.Sprintf("%s %s (%s)", t.name, t.id, state))
	}
	return "answering " + strings.Join(parts, ", ") + " with failed tool results and asking again"
}

// interruptionErrorMessage is what the client is told when recovery is off
// or used up.
func interruptionErrorMessage(kind int, silence time.Duration, recoveries int) string {
	if recoveries >= maxRecoveries {
		return fmt.Sprintf("The response was interrupted %d times in a row (Kiro ends a response that stays silent for about 240 s, "+
			"and a tool call's input counts against the model's output limit). The proxy recovered it %d times and stops here. "+
			"Ask for a smaller step (for example, write a large file in several smaller Write and Edit calls), then retry.", recoveries+1, recoveries)
	}
	if kind == interruptTruncated {
		return "The model hit its output token limit inside a tool call, so the call was truncated and was not passed on. " +
			"Retrying the same request will be truncated again. Ask for less per call (for example, write a large file in several smaller Write and Edit calls), then retry."
	}
	return backendIdleCutMessage(silence)
}

// recoveryBackoff is the pause before a plain resend: 500 ms with 20% jitter,
// as Kiro's client waits.
func recoveryBackoff() time.Duration {
	return 400*time.Millisecond + time.Duration(cryptoRandIntn(200))*time.Millisecond
}

// sleepCtx waits d or until ctx ends; false when ctx ended.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// recoveryRequest builds the backend request for req on the same
// conversation as the interrupted one.
func recoveryRequest(req AnthropicRequest, token TokenData, conversationID string) (CodeWhispererRequest, []byte, error) {
	cw := buildCodeWhispererRequest(req, token)
	cw.ConversationState.ConversationId = conversationID
	body, err := json.Marshal(cw)
	return cw, body, err
}

// sendRecoveryRequest sends body to the backend with the same headers as any
// request and returns the response when it is a 200.
func sendRecoveryRequest(ctx context.Context, cfg *config.Config, token TokenData, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Advanced.CodeWhispererEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", kiroide.UserAgent())
	resp, err := proxyHttpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	return resp, nil
}
