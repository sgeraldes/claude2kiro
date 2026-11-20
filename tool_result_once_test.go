package main

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/config"
)

// marshalCW marshals a built request the way the proxy sends it.
func marshalCW(t *testing.T, cw CodeWhispererRequest) string {
	t.Helper()
	body, err := json.Marshal(cw)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return string(body)
}

func toolUseBlock(id string) map[string]any {
	return map[string]any{
		"type":  "tool_use",
		"id":    id,
		"name":  "Read",
		"input": map[string]any{"file_path": "x"},
	}
}

func toolResultBlock(id, text string) map[string]any {
	return map[string]any{
		"type":        "tool_result",
		"tool_use_id": id,
		"content":     text,
	}
}

func TestToolResultSentOnceCurrentMessageOnlyResult(t *testing.T) {
	withConfig(t, config.Default())

	const marker = "RESULT_MARKER_CURRENT_ONLY"
	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages: []AnthropicRequestMessage{
			{Role: "user", Content: "read the file"},
			{Role: "assistant", Content: []any{toolUseBlock("tu_1")}},
			{Role: "user", Content: []any{toolResultBlock("tu_1", marker)}},
		},
	}

	cw := buildCodeWhispererRequest(req, TokenData{})
	if got := strings.Count(marshalCW(t, cw), marker); got != 1 {
		t.Fatalf("tool result text appears %d times in the request, want 1", got)
	}
	current := cw.ConversationState.CurrentMessage.UserInputMessage
	if current.Content != "" {
		t.Fatalf("current content = %q, want empty when the message holds only tool results", current.Content)
	}
	if got := current.UserInputMessageContext.ToolResults; len(got) != 1 || got[0].Content[0].Text != marker {
		t.Fatalf("current toolResults = %+v, want the one result carrying %q", got, marker)
	}
}

func TestToolResultSentOnceCurrentMessageTextAndResult(t *testing.T) {
	withConfig(t, config.Default())

	const marker = "RESULT_MARKER_WITH_TEXT"
	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages: []AnthropicRequestMessage{
			{Role: "user", Content: "read the file"},
			{Role: "assistant", Content: []any{toolUseBlock("tu_1")}},
			{Role: "user", Content: []any{
				toolResultBlock("tu_1", marker),
				map[string]any{"type": "text", "text": "now summarize it"},
			}},
		},
	}

	cw := buildCodeWhispererRequest(req, TokenData{})
	if got := strings.Count(marshalCW(t, cw), marker); got != 1 {
		t.Fatalf("tool result text appears %d times in the request, want 1", got)
	}
	if got, want := cw.ConversationState.CurrentMessage.UserInputMessage.Content, "now summarize it"; got != want {
		t.Fatalf("current content = %q, want only the text block %q", got, want)
	}
}

func TestToolResultSentOnceHistoryUserMessage(t *testing.T) {
	withConfig(t, config.Default())

	const marker = "RESULT_MARKER_HISTORY"
	const arrayMarker = "ARRAY_BLOCK_MARKER"
	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages: []AnthropicRequestMessage{
			{Role: "user", Content: "read two files"},
			{Role: "assistant", Content: []any{toolUseBlock("tu_1"), toolUseBlock("tu_2")}},
			{Role: "user", Content: []any{
				toolResultBlock("tu_1", marker),
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "tu_2",
					"content":     []any{map[string]any{"type": "text", "text": arrayMarker}},
				},
				map[string]any{"type": "text", "text": "both read"},
			}},
			{Role: "assistant", Content: "done"},
			{Role: "user", Content: "thanks"},
		},
	}

	cw := buildCodeWhispererRequest(req, TokenData{})
	body := marshalCW(t, cw)
	for _, m := range []string{marker, arrayMarker} {
		if got := strings.Count(body, m); got != 1 {
			t.Fatalf("tool result text %q appears %d times in the request, want 1", m, got)
		}
	}
	user, ok := cw.ConversationState.History[2].(HistoryUserMessage)
	if !ok {
		t.Fatalf("third history entry type = %T, want HistoryUserMessage", cw.ConversationState.History[2])
	}
	if got, want := user.UserInputMessage.Content, "both read"; got != want {
		t.Fatalf("history user content = %q, want only the text block %q", got, want)
	}
	if user.UserInputMessage.UserInputMessageContext == nil || len(user.UserInputMessage.UserInputMessageContext.ToolResults) != 2 {
		t.Fatalf("history user context = %+v, want 2 structured tool results", user.UserInputMessage.UserInputMessageContext)
	}
}

// TestRequestMetricsSummaryCountsToolResultChars keeps the size log honest:
// contentChars counts only the content field, so the tool result volume that
// used to hide inside it is reported on its own.
func TestRequestMetricsSummaryCountsToolResultChars(t *testing.T) {
	cfg := config.Default()
	withConfig(t, cfg)

	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages: []AnthropicRequestMessage{
			{Role: "user", Content: "read the file"},
			{Role: "assistant", Content: []any{toolUseBlock("tu_1")}},
			{Role: "user", Content: []any{
				toolResultBlock("tu_1", "0123456789"),
				map[string]any{"type": "text", "text": "abc"},
			}},
		},
	}
	cw := buildCodeWhispererRequest(req, TokenData{})
	got := requestMetricsSummary(cw, len(marshalCW(t, cw)), cfg)
	for _, want := range []string{"toolResults=1", "contentChars=3", "toolResultChars=10"} {
		if !strings.Contains(got, want) {
			t.Fatalf("requestMetricsSummary() = %q, want substring %q", got, want)
		}
	}
}

// TestToolResultNoneTextUnchanged pins the none_text output: tool traffic is
// plain text in content and no structured tool results are sent. The expected
// strings are the output of the proxy before the send-once change.
func TestToolResultNoneTextUnchanged(t *testing.T) {
	cfg := config.Default()
	cfg.Advanced.ToolMode = "none_text"
	withConfig(t, cfg)

	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages: []AnthropicRequestMessage{
			{Role: "user", Content: "read the file"},
			{Role: "assistant", Content: []any{toolUseBlock("tu_1")}},
			{Role: "user", Content: []any{
				toolResultBlock("tu_1", "FIRST_RESULT"),
				map[string]any{"type": "text", "text": "keep going"},
			}},
			{Role: "assistant", Content: []any{toolUseBlock("tu_2")}},
			{Role: "user", Content: []any{toolResultBlock("tu_2", "SECOND_RESULT")}},
		},
	}

	cw := buildCodeWhispererRequest(req, TokenData{})
	current := cw.ConversationState.CurrentMessage.UserInputMessage
	if got, want := current.Content, "[Tool Result (tu_2)]\nSECOND_RESULT"; got != want {
		t.Fatalf("current content = %q, want %q", got, want)
	}
	if len(current.UserInputMessageContext.ToolResults) != 0 {
		t.Fatalf("current toolResults = %+v, want none in none_text mode", current.UserInputMessageContext.ToolResults)
	}
	user, ok := cw.ConversationState.History[2].(HistoryUserMessage)
	if !ok {
		t.Fatalf("third history entry type = %T, want HistoryUserMessage", cw.ConversationState.History[2])
	}
	if got, want := user.UserInputMessage.Content, "[Tool Result (tu_1)]\nFIRST_RESULT\nkeep going"; got != want {
		t.Fatalf("history user content = %q, want %q", got, want)
	}
	if user.UserInputMessage.UserInputMessageContext != nil {
		t.Fatalf("history user context = %+v, want nil in none_text mode", user.UserInputMessage.UserInputMessageContext)
	}
	body := marshalCW(t, cw)
	if strings.Contains(body, "toolResults") {
		t.Fatalf("none_text request carries structured toolResults: %s", body)
	}
	for _, m := range []string{"FIRST_RESULT", "SECOND_RESULT"} {
		if got := strings.Count(body, m); got != 1 {
			t.Fatalf("none_text: %q appears %d times, want 1", m, got)
		}
	}
}

// TestToolResultSentOnceEveryHistoryMode checks, for each history mode, that
// every tool result kept in the request appears exactly once, that it lives in
// toolResults and not in content, and that the diet drops only the turns it
// dropped before (whole entries, never the structured copy alone).
func TestToolResultSentOnceEveryHistoryMode(t *testing.T) {
	messages := []AnthropicRequestMessage{
		{Role: "user", Content: "start"},
		{Role: "assistant", Content: []any{toolUseBlock("tu_1")}},
		{Role: "user", Content: []any{toolResultBlock("tu_1", "MARK_R1")}},
		{Role: "assistant", Content: []any{toolUseBlock("tu_2")}},
		{Role: "user", Content: []any{
			toolResultBlock("tu_2", "MARK_R2"),
			map[string]any{"type": "text", "text": "and then"},
		}},
		{Role: "assistant", Content: "done with both"},
		{Role: "user", Content: "one more"},
		{Role: "assistant", Content: []any{toolUseBlock("tu_3")}},
		{Role: "user", Content: []any{toolResultBlock("tu_3", "MARK_R3")}},
	}
	all := []string{"MARK_R1", "MARK_R2", "MARK_R3"}

	tests := []struct {
		mode        string
		recentTurns int
		want        []string
	}{
		{mode: "full", want: all},
		{mode: "all", want: all},
		{mode: "", want: all},
		{mode: "recent", recentTurns: 1, want: []string{"MARK_R3"}},
		// Keeping R2 pulls in the tu_2 call and the user turn before it,
		// which holds R1, so the tool-result protection keeps all three.
		{mode: "recent", recentTurns: 2, want: all},
		{mode: "current_only", want: []string{"MARK_R3"}},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			cfg := config.Default()
			cfg.Advanced.HistoryMode = tt.mode
			cfg.Advanced.HistoryRecentTurns = tt.recentTurns
			withConfig(t, cfg)

			req := AnthropicRequest{
				Model:     "claude-sonnet-4-20250514",
				MaxTokens: 64,
				System:    []AnthropicSystemMessage{{Type: "text", Text: "system instructions"}},
				Messages:  messages,
			}
			cw := buildCodeWhispererRequest(req, TokenData{})
			body := marshalCW(t, cw)

			var present []string
			for _, m := range all {
				switch got := strings.Count(body, m); got {
				case 0:
				case 1:
					present = append(present, m)
				default:
					t.Fatalf("%q appears %d times, want at most 1", m, got)
				}
			}
			sort.Strings(present)
			if got, want := strings.Join(present, ","), strings.Join(tt.want, ","); got != want {
				t.Fatalf("tool results present = %q, want %q", got, want)
			}

			// Every result kept in the request is carried structurally; no
			// user content repeats or replaces it.
			if c := cw.ConversationState.CurrentMessage.UserInputMessage.Content; strings.Contains(c, "MARK_") {
				t.Fatalf("current content %q carries tool result text", c)
			}
			structured := 0
			for _, tr := range cw.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.ToolResults {
				if strings.HasPrefix(tr.Content[0].Text, "MARK_") {
					structured++
				}
			}
			for _, entry := range cw.ConversationState.History {
				user, ok := entry.(HistoryUserMessage)
				if !ok {
					continue
				}
				if strings.Contains(user.UserInputMessage.Content, "MARK_") {
					t.Fatalf("history content %q carries tool result text", user.UserInputMessage.Content)
				}
				for _, tr := range historyUserToolResults(user) {
					if strings.HasPrefix(tr.Content[0].Text, "MARK_") {
						structured++
					}
				}
			}
			if structured != len(tt.want) {
				t.Fatalf("structured tool results = %d, want %d", structured, len(tt.want))
			}
		})
	}
}
