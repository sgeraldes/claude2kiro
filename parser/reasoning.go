package parser

import (
	"encoding/json"
	"strings"
)

// ReasoningBlock is the model's reasoning for one response, as the backend
// streamed it: text from the {"text":...} frames, the {"signature":...} that
// vouches for it, or a {"redactedContent":...} blob kept in the JSON encoding
// it arrived in. It is never sent to the client; the proxy keeps it so the
// next request can give the backend its own reasoning back.
type ReasoningBlock struct {
	Text      string
	Signature string
	Redacted  json.RawMessage
}

// Forwardable reports whether the backend accepts the block back in history:
// text with its signature, or redacted content. Unsigned reasoning is not.
func (b ReasoningBlock) Forwardable() bool {
	return len(b.Redacted) > 0 || (b.Text != "" && b.Signature != "")
}

// responseCollector gathers what a response says besides its client events:
// the last reasoning block, the tool use ids, the assistant text and the model
// the backend says served it. Both translators feed it every parsed frame.
type responseCollector struct {
	reasoning      ReasoningBlock
	contentStarted bool // assistant text or a tool call came after the current block began
	toolUseIDs     []string
	seenTool       map[string]bool
	text           strings.Builder
	servedModel    string
}

// isReasoningFrame reports a frame that carries reasoning and no assistant
// content or tool.
func (e assistantResponseEvent) isReasoningFrame() bool {
	if e.Content != "" || e.ToolUseId != "" {
		return false
	}
	return e.Reasoning != "" || e.Signature != "" || e.hasRedacted()
}

func (e assistantResponseEvent) hasRedacted() bool {
	return len(e.RedactedContent) > 0 && string(e.RedactedContent) != "null"
}

func (c *responseCollector) observe(evt assistantResponseEvent) {
	if evt.ModelId != "" {
		c.servedModel = evt.ModelId
	}
	if evt.Content != "" {
		c.contentStarted = true
		c.text.WriteString(evt.Content)
	}
	if evt.ToolUseId != "" {
		c.contentStarted = true
		if evt.Name != "" && !c.seenTool[evt.ToolUseId] {
			if c.seenTool == nil {
				c.seenTool = map[string]bool{}
			}
			c.seenTool[evt.ToolUseId] = true
			c.toolUseIDs = append(c.toolUseIDs, evt.ToolUseId)
		}
	}
	if !evt.isReasoningFrame() {
		return
	}
	// Only the last reasoning block of a response goes back: reasoning that
	// follows assistant text or a tool call starts a block that replaces it.
	if c.contentStarted {
		c.reasoning = ReasoningBlock{}
		c.contentStarted = false
	}
	c.reasoning.Text += evt.Reasoning
	if evt.Signature != "" {
		c.reasoning.Signature = evt.Signature
	}
	if evt.hasRedacted() {
		c.reasoning.Redacted = append(json.RawMessage(nil), evt.RedactedContent...)
	}
}
