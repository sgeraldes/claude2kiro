package main

import (
	"encoding/json"
	"strings"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/reasoning"
	"github.com/sgeraldes/claude2kiro/parser"
)

// The backend streams the model's reasoning and the official Kiro client sends
// it back in the history of later turns. Claude Code runs with thinking off
// and never sees it, so the proxy keeps it here and attaches it itself.
// Without it the model plans every turn again: on one measured agent step the
// second turn took 290 s and 4.22 credits without it, 44.5 s and 1.36 credits
// with it.

// reasoningStore holds the reasoning of recent turns for every session.
var reasoningStore = reasoning.NewStore(reasoning.DefaultMaxBytes)

// ReasoningContent is assistantResponseMessage.reasoningContent: exactly one
// of reasoningText or redactedContent.
type ReasoningContent struct {
	ReasoningText   *ReasoningText  `json:"reasoningText,omitempty"`
	RedactedContent json.RawMessage `json:"redactedContent,omitempty"`
}

// ReasoningText is reasoning text with the signature that vouches for it.
type ReasoningText struct {
	Text      string `json:"text"`
	Signature string `json:"signature"`
}

// reasoningAttachment records what attachReasoning put in a request.
type reasoningAttachment struct {
	turns int      // assistant turns that carry reasoning
	bytes int      // reasoning bytes attached (reasoning.Entry.Size)
	keys  []string // the store keys of those turns, to evict them if rejected
}

// reasoningScope places an assistant turn: the client session it belongs to
// and the turn's position among the conversation's assistant messages.
type reasoningScope struct {
	session string // extractSessionID's full id; "" when the client sent none
	turn    int
}

// responseScope is the scope of the turn a response to req produces: req's
// session, and the position after req's own assistant messages. The position
// counts req.Messages, never the history sent to the backend, so a history
// mode that drops turns does not move it.
func responseScope(req AnthropicRequest) reasoningScope {
	_, session := extractSessionID(req.Metadata)
	scope := reasoningScope{session: session}
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			scope.turn++
		}
	}
	return scope
}

// keys are the store keys of a turn in this scope. A turn with tool calls is
// keyed by its tool use ids only. A turn without one is keyed by its text and
// position, and only within a session: without a session there is nothing to
// keep two conversations' identical texts apart, so such a turn has no key.
func (s reasoningScope) keys(toolUseIDs []string, text string) []string {
	if len(toolUseIDs) > 0 {
		keys := make([]string, 0, len(toolUseIDs))
		for _, id := range toolUseIDs {
			keys = append(keys, reasoning.ToolKey(s.session, id))
		}
		return keys
	}
	if s.session == "" || strings.TrimSpace(text) == "" {
		return nil
	}
	return []string{reasoning.TextKey(s.session, s.turn, text)}
}

// historyToolUseIDs are the ids of a history turn's tool calls.
func historyToolUseIDs(toolUses []HistoryToolUse) []string {
	ids := make([]string, 0, len(toolUses))
	for _, tu := range toolUses {
		ids = append(ids, tu.ToolUseId)
	}
	return ids
}

// rememberReasoning keeps the reasoning of a successful response produced by
// kiroModel for the turn at scope: under each tool use id the response
// emitted or, when it emitted none, under its text (see reasoningScope.keys).
// Unsigned reasoning is not kept, and nothing is kept while
// advanced.reasoning_history_max_bytes is 0. A turn whose reasoning is not
// kept also forgets what an earlier turn left under the same keys, so a later
// lookup can never find reasoning this turn did not produce.
func rememberReasoning(kiroModel string, scope reasoningScope, block parser.ReasoningBlock, toolUseIDs []string, text string) {
	keys := scope.keys(toolUseIDs, text)
	if len(keys) == 0 {
		return
	}
	if !block.Forwardable() || config.Get().Advanced.ReasoningHistoryMaxBytes <= 0 {
		reasoningStore.Evict(keys...)
		return
	}
	reasoningStore.Put(reasoning.Entry{
		Model:     kiroModel,
		Text:      block.Text,
		Signature: block.Signature,
		Redacted:  block.Redacted,
	}, keys...)
}

// attachReasoning sets reasoningContent on the history's assistant turns that
// have stored reasoning of kiroModel, newest first, and stops before the
// attached bytes would exceed maxBytes. maxBytes 0 attaches nothing. Each
// turn is looked up by the keys buildCodeWhispererRequest gave it
// (reasoningScope.keys), the same keys rememberReasoning stored under.
func attachReasoning(history []any, kiroModel string, maxBytes int) reasoningAttachment {
	var att reasoningAttachment
	if maxBytes <= 0 {
		return att
	}
	for i := len(history) - 1; i >= 0; i-- {
		msg, ok := history[i].(HistoryAssistantMessage)
		if !ok || len(msg.turnKeys) == 0 {
			continue
		}
		entry, found := reasoningStore.Get(kiroModel, msg.turnKeys...)
		if !found {
			continue
		}
		if att.bytes+entry.Size() > maxBytes {
			break
		}
		if len(entry.Redacted) > 0 && (entry.Text == "" || entry.Signature == "") {
			msg.AssistantResponseMessage.ReasoningContent = &ReasoningContent{RedactedContent: entry.Redacted}
		} else {
			msg.AssistantResponseMessage.ReasoningContent = &ReasoningContent{
				ReasoningText: &ReasoningText{Text: entry.Text, Signature: entry.Signature},
			}
		}
		history[i] = msg
		att.turns++
		att.bytes += entry.Size()
		att.keys = append(att.keys, msg.turnKeys...)
	}
	return att
}

// stripReasoning removes every reasoningContent from the request's history and
// returns the store keys of the turns that carried one.
func (r *CodeWhispererRequest) stripReasoning() []string {
	for i, h := range r.ConversationState.History {
		if msg, ok := h.(HistoryAssistantMessage); ok && msg.AssistantResponseMessage.ReasoningContent != nil {
			msg.AssistantResponseMessage.ReasoningContent = nil
			r.ConversationState.History[i] = msg
		}
	}
	keys := r.reasoning.keys
	r.reasoning = reasoningAttachment{}
	return keys
}

// isThinkingSignatureInvalid reports a 400 whose reason says the backend
// rejected a reasoning signature the request carried:
// {"reason":"THINKING_SIGNATURE_INVALID",...}. Only the structured reason is
// trusted (see isInvalidModelID).
func isThinkingSignatureInvalid(body []byte) bool {
	var parsed struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		return strings.EqualFold(strings.TrimSpace(parsed.Reason), "thinking_signature_invalid")
	}
	return strings.Contains(strings.ToLower(string(body)), "thinking_signature_invalid")
}
