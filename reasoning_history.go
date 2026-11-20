package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
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

// reasoningScope places an assistant turn: the client session and the
// conversation within it that the turn belongs to, and the turn's position
// among the conversation's assistant messages.
type reasoningScope struct {
	session      string // extractSessionID's full id; "" when the client sent none
	conversation string // conversationFingerprint of the request; set only with a session
	turn         int
}

// responseScope is the scope of the turn a response to req produces: req's
// session and conversation, and the position after req's own assistant
// messages. The position counts req.Messages, never the history sent to the
// backend, so a history mode that drops turns does not move it.
// buildCodeWhispererRequest computes it once and keeps it on the request:
// lookup keys the history's turns with its session and conversation, and
// capture stores the response under it, so both see the same fingerprint.
func responseScope(req AnthropicRequest) reasoningScope {
	_, session := extractSessionID(req.Metadata)
	scope := reasoningScope{session: session}
	if session != "" {
		scope.conversation = conversationFingerprint(req.Messages)
	}
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			scope.turn++
		}
	}
	return scope
}

// storeScope is the scope string the store keys carry: the session and the
// conversation fingerprint, or "" without a session. Claude Code sends one
// session id for a conversation and every Task subagent it spawns, so the
// session alone would let a parent and its subagent share keys.
func (s reasoningScope) storeScope() string {
	if s.session == "" {
		return ""
	}
	return s.session + "\x00" + s.conversation
}

// keys are the store keys of a turn in this scope. A turn with tool calls is
// keyed by its tool use ids only. A turn without one is keyed by its text and
// position, and only within a session: without a session there is nothing to
// keep two conversations' identical texts apart, so such a turn has no key.
func (s reasoningScope) keys(toolUseIDs []string, text string) []string {
	scope := s.storeScope()
	if len(toolUseIDs) > 0 {
		keys := make([]string, 0, len(toolUseIDs))
		for _, id := range toolUseIDs {
			keys = append(keys, reasoning.ToolKey(scope, id))
		}
		return keys
	}
	if scope == "" || reasoning.NormalizeText(text) == "" {
		return nil
	}
	return []string{reasoning.TextKey(scope, s.turn, text)}
}

// systemReminderPattern matches the <system-reminder> spans Claude Code injects
// into a user message.
var systemReminderPattern = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// onConversationFingerprint, when set, is called by every
// conversationFingerprint. Tests set it to count the calls.
var onConversationFingerprint func()

// conversationFingerprint identifies the conversation msgs belong to by its
// first message: a hash of its role and its text. Claude Code opens that
// message with <system-reminder> blocks (instructions, a git status snapshot,
// an attribution note) that can change between requests of one conversation,
// and ends it with the prompt, which for a Task subagent is the task its parent
// wrote. So the hash covers the text with the reminder spans removed: stable
// across a conversation's turns, different between a parent and its
// subagents. A first message that is all reminders hashes all its text, and
// one with no text at all hashes its JSON without cache_control markers,
// which the client moves between requests. When the client compacts or
// rewrites the first message the fingerprint changes and earlier entries are
// simply not found.
func conversationFingerprint(msgs []AnthropicRequestMessage) string {
	if onConversationFingerprint != nil {
		onConversationFingerprint()
	}
	if len(msgs) == 0 {
		return ""
	}
	first := msgs[0]
	var blocks []string
	switch v := first.Content.(type) {
	case string:
		blocks = []string{v}
	case []any:
		for _, block := range v {
			if m, ok := block.(map[string]any); ok && m["type"] == "text" {
				if s, ok := m["text"].(string); ok {
					blocks = append(blocks, s)
				}
			}
		}
	}
	// The blocks are joined with a space before the reminders are removed, so a
	// reminder that opens in one block and closes in another goes too. A space
	// is what normalizing turns any block boundary into anyway, so a first
	// message whose reminders each sit in one block hashes as it did when they
	// were removed block by block, and a space can never complete a tag.
	joined := strings.Join(blocks, " ")
	text := reasoning.NormalizeText(systemReminderPattern.ReplaceAllString(joined, " "))
	if text == "" {
		text = reasoning.NormalizeText(joined) // all reminders, or no text at all
	}
	var basis string
	if text != "" {
		basis = "text\x00" + text
	} else {
		content := withoutCacheControl(first.Content)
		raw, err := json.Marshal(content)
		if err != nil {
			// Content decoded from JSON always marshals; for anything else
			// fmt's form is stable too (it prints map keys sorted).
			raw = []byte(fmt.Sprintf("%#v", content))
		}
		basis = "json\x00" + string(raw)
	}
	sum := sha256.Sum256([]byte(first.Role + "\x00" + basis))
	return hex.EncodeToString(sum[:16])
}

// withoutCacheControl is content with the cache_control field of each block
// removed, leaving content itself unchanged.
func withoutCacheControl(content any) any {
	list, ok := content.([]any)
	if !ok {
		return content
	}
	out := make([]any, len(list))
	for i, block := range list {
		m, ok := block.(map[string]any)
		if !ok {
			out[i] = block
			continue
		}
		copied := make(map[string]any, len(m))
		for k, v := range m {
			if k != "cache_control" {
				copied[k] = v
			}
		}
		out[i] = copied
	}
	return out
}

// assistantTurnText is the text of a history assistant turn as its reasoning
// key sees it: its text blocks concatenated with nothing between them, as the
// response's text frames were (reasoning.NormalizeText evens out whitespace).
func assistantTurnText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var text strings.Builder
		for _, block := range v {
			if m, ok := block.(map[string]any); ok && m["type"] == "text" {
				if s, ok := m["text"].(string); ok {
					text.WriteString(s)
				}
			}
		}
		return text.String()
	default:
		return ""
	}
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
// have stored reasoning of kiroModel, newest first, and skips a turn whose
// reasoning would take the attached bytes past maxBytes, so one oversized
// block does not cost every older turn its reasoning. maxBytes 0 attaches
// nothing. maxBlocks above 0 stops after that many turns. Each
// turn is looked up by the keys buildCodeWhispererRequest gave it
// (reasoningScope.keys), the same keys rememberReasoning stored under.
func attachReasoning(history []any, kiroModel string, maxBytes, maxBlocks int) reasoningAttachment {
	var att reasoningAttachment
	if maxBytes <= 0 {
		return att
	}
	for i := len(history) - 1; i >= 0; i-- {
		if maxBlocks > 0 && att.turns >= maxBlocks {
			break
		}
		msg, ok := history[i].(HistoryAssistantMessage)
		if !ok || len(msg.turnKeys) == 0 {
			continue
		}
		entry, found := reasoningStore.Get(kiroModel, msg.turnKeys...)
		if !found {
			continue
		}
		if att.bytes+entry.Size() > maxBytes {
			continue // too big for the room left; an older, smaller turn may fit
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
// {"reason":"THINKING_SIGNATURE_INVALID",...}. A JSON body counts only by its
// reason field, never by its message, which can echo request content. A body
// that is not JSON counts when it contains the enum, as in isInvalidModelID.
func isThinkingSignatureInvalid(body []byte) bool {
	var parsed struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		return strings.EqualFold(strings.TrimSpace(parsed.Reason), "thinking_signature_invalid")
	}
	return strings.Contains(strings.ToLower(string(body)), "thinking_signature_invalid")
}
