package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sgeraldes/claude2kiro/internal/config"
)

// uuidRe matches a canonical, well-formed UUID string. The version nibble is left
// open (4 for random, 5 for our name-based derivation) but the variant is fixed.
var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// withConfig temporarily replaces the global config, restoring the prior config
// when the test finishes.
func withConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	orig := config.Get()
	config.Set(cfg)
	t.Cleanup(func() { config.Set(orig) })
}

// withStableConversationID temporarily sets the feature flag, restoring the prior
// config when the test finishes.
func withStableConversationID(t *testing.T, enabled bool) {
	t.Helper()
	orig := config.Get()
	clone := *orig
	clone.Advanced.StableConversationID = enabled
	config.Set(&clone)
	t.Cleanup(func() { config.Set(orig) })
}

func TestStableConversationID(t *testing.T) {
	const keyA = "ce40736e-1347-467a-9cce-181e245edd92"
	const keyB = "11111111-2222-3333-4444-555555555555"

	// (a) Same session key -> same, valid UUID, across repeated calls.
	a1 := stableConversationID(keyA)
	a2 := stableConversationID(keyA)
	if a1 != a2 {
		t.Errorf("stableConversationID not stable for same key: %q != %q", a1, a2)
	}
	if !uuidRe.MatchString(a1) {
		t.Errorf("stableConversationID(%q) = %q, not a well-formed UUID", keyA, a1)
	}

	// (b) Different keys -> different ids.
	b := stableConversationID(keyB)
	if !uuidRe.MatchString(b) {
		t.Errorf("stableConversationID(%q) = %q, not a well-formed UUID", keyB, b)
	}
	if a1 == b {
		t.Errorf("stableConversationID collided for different keys: both %q", a1)
	}

	// (c) Empty key -> random, non-empty, valid UUID (and not all-equal, i.e. random).
	r1 := stableConversationID("")
	r2 := stableConversationID("")
	if r1 == "" || r2 == "" {
		t.Fatal("stableConversationID(\"\") returned empty string")
	}
	if !uuidRe.MatchString(r1) || !uuidRe.MatchString(r2) {
		t.Errorf("stableConversationID(\"\") not a well-formed UUID: %q, %q", r1, r2)
	}
	if r1 == r2 {
		t.Errorf("stableConversationID(\"\") should be random but returned identical ids: %q", r1)
	}
}

func TestExtractSessionID(t *testing.T) {
	const sessionUUID = "ce40736e-1347-467a-9cce-181e245edd92"

	tests := []struct {
		name     string
		metadata map[string]any
	}{
		{
			name:     "legacy suffix",
			metadata: map[string]any{"user_id": "user_abc_session_" + sessionUUID},
		},
		{
			name: "claude code json string",
			metadata: map[string]any{
				"user_id": `{"device_id":"dev_123","account_uuid":"","session_id":"` + sessionUUID + `"}`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			short, full := extractSessionID(tt.metadata)
			if full != sessionUUID {
				t.Fatalf("full session id = %q, want %q", full, sessionUUID)
			}
			if short != "245edd92" {
				t.Fatalf("short session id = %q, want 245edd92", short)
			}
		})
	}
}

func TestBuildCodeWhispererRequestConversationIDDefault(t *testing.T) {
	// DEFAULT behavior: reuse a session-derived conversationId across turns,
	// matching current Kiro IDE behavior.
	withConfig(t, config.Default())

	const sessionUUID = "ce40736e-1347-467a-9cce-181e245edd92"
	metadata := map[string]any{
		"user_id": `{"device_id":"dev_123","account_uuid":"","session_id":"` + sessionUUID + `"}`,
	}
	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
		Metadata:  metadata,
	}

	cw1 := buildCodeWhispererRequest(req, TokenData{})
	cw2 := buildCodeWhispererRequest(req, TokenData{})
	if cw1.ConversationState.ConversationId != cw2.ConversationState.ConversationId {
		t.Errorf("default conversationId not stable across turns: %q != %q",
			cw1.ConversationState.ConversationId, cw2.ConversationState.ConversationId)
	}
	want := stableConversationID(sessionUUID)
	if cw1.ConversationState.ConversationId != want {
		t.Errorf("default conversationId = %q, want %q (derived from session key)",
			cw1.ConversationState.ConversationId, want)
	}
}

func TestBuildCodeWhispererRequestConversationIDStableOptOut(t *testing.T) {
	// OPT-OUT behavior (flag off): a fresh random UUID per request, even when a
	// session key is present.
	withStableConversationID(t, false)

	const sessionUUID = "ce40736e-1347-467a-9cce-181e245edd92"
	metadata := map[string]any{
		"user_id": `{"device_id":"dev_123","account_uuid":"","session_id":"` + sessionUUID + `"}`,
	}
	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
		Metadata:  metadata,
	}

	cw1 := buildCodeWhispererRequest(req, TokenData{})
	cw2 := buildCodeWhispererRequest(req, TokenData{})
	if !uuidRe.MatchString(cw1.ConversationState.ConversationId) {
		t.Errorf("opt-out conversationId = %q, not a well-formed UUID", cw1.ConversationState.ConversationId)
	}
	if cw1.ConversationState.ConversationId == cw2.ConversationState.ConversationId {
		t.Errorf("opt-out conversationId should be random per request, got identical: %q",
			cw1.ConversationState.ConversationId)
	}
	if cw1.ConversationState.ConversationId == stableConversationID(sessionUUID) {
		t.Errorf("opt-out conversationId should NOT be the session-derived id when flag is off")
	}

	// Even with the default on, no session key -> random UUID per request.
	withStableConversationID(t, true)
	reqNoMeta := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
	}
	n1 := buildCodeWhispererRequest(reqNoMeta, TokenData{}).ConversationState.ConversationId
	n2 := buildCodeWhispererRequest(reqNoMeta, TokenData{}).ConversationState.ConversationId
	if !uuidRe.MatchString(n1) {
		t.Errorf("stable enabled, no-metadata conversationId = %q, not a well-formed UUID", n1)
	}
	if n1 == n2 {
		t.Errorf("stable enabled with no session key should still be random per request, got identical: %q", n1)
	}
}

func TestBuildCodeWhispererRequestCachePoint(t *testing.T) {
	schema := map[string]any{"type": "object"}

	// With cache_control on the tool -> a cachePoint entry follows the tool.
	reqWith := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
		Tools: []AnthropicTool{{
			Name:         "get_weather",
			Description:  "Get weather",
			InputSchema:  schema,
			CacheControl: map[string]any{"type": "ephemeral"},
		}},
	}
	cwWith := buildCodeWhispererRequest(reqWith, TokenData{})
	toolsWith := cwWith.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools
	if len(toolsWith) != 2 {
		t.Fatalf("expected 2 tool entries (tool + cachePoint), got %d", len(toolsWith))
	}
	if toolsWith[0].ToolSpecification == nil || toolsWith[0].ToolSpecification.Name != "get_weather" {
		t.Errorf("first entry should be the tool spec; got %+v", toolsWith[0])
	}
	if toolsWith[0].CachePoint != nil {
		t.Errorf("first entry should not carry a cachePoint; got %+v", toolsWith[0].CachePoint)
	}
	cp := toolsWith[1]
	if cp.CachePoint == nil || cp.CachePoint.Type != "default" {
		t.Errorf("second entry should be a default cachePoint; got %+v", cp)
	}
	if cp.ToolSpecification != nil {
		t.Errorf("cachePoint entry should omit toolSpecification; got %+v", cp.ToolSpecification)
	}

	// Without cache_control -> no cachePoint entry, just the tool.
	reqWithout := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
		Tools: []AnthropicTool{{
			Name:        "get_weather",
			Description: "Get weather",
			InputSchema: schema,
		}},
	}
	cwWithout := buildCodeWhispererRequest(reqWithout, TokenData{})
	toolsWithout := cwWithout.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools
	if len(toolsWithout) != 1 {
		t.Fatalf("expected 1 tool entry (no cachePoint), got %d", len(toolsWithout))
	}
	if toolsWithout[0].CachePoint != nil {
		t.Errorf("expected no cachePoint without cache_control; got %+v", toolsWithout[0].CachePoint)
	}
}

// TestBuildCodeWhispererRequestCachePointRespectsToolLimit verifies that the
// total emitted entries (tools + cachePoints) never exceed maxTools, even when
// every tool carries a cache breakpoint.
func TestBuildCodeWhispererRequestCachePointRespectsToolLimit(t *testing.T) {
	cfg := config.Get()
	maxTools := cfg.Network.MaxToolsPerRequest
	if maxTools < 1 {
		maxTools = 85
	}

	schema := map[string]any{"type": "object"}
	// Provide more tools than the limit, each with cache_control so cachePoints
	// would inflate the array if not capped.
	n := maxTools + 50
	tools := make([]AnthropicTool, 0, n)
	for range n {
		tools = append(tools, AnthropicTool{
			Name:         "tool",
			Description:  "d",
			InputSchema:  schema,
			CacheControl: map[string]any{"type": "ephemeral"},
		})
	}
	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
		Tools:     tools,
	}
	cw := buildCodeWhispererRequest(req, TokenData{})
	got := cw.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools
	if len(got) > maxTools {
		t.Errorf("emitted %d tool entries, exceeds maxTools=%d", len(got), maxTools)
	}
}

func TestKiroRuntimeEndpoint(t *testing.T) {
	if got, want := kiroRuntimeEndpoint("us-west-2"), "https://runtime.us-west-2.kiro.dev/"; got != want {
		t.Fatalf("kiroRuntimeEndpoint = %q, want %q", got, want)
	}
	if got, want := kiroRuntimeEndpoint(""), "https://runtime.us-east-1.kiro.dev/"; got != want {
		t.Fatalf("kiroRuntimeEndpoint empty region = %q, want %q", got, want)
	}
}

func TestBuildCodeWhispererRequestAddsNativeEffort(t *testing.T) {
	var req AnthropicRequest
	err := json.Unmarshal([]byte(`{
		"model": "claude-sonnet-4-6",
		"max_tokens": 64,
		"messages": [{"role": "user", "content": "think lightly"}],
		"output_config": {"effort": "xhigh"}
	}`), &req)
	if err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	cw := buildCodeWhispererRequest(req, TokenData{})
	fields := cw.AdditionalModelRequestFields
	if fields == nil || fields.OutputConfig == nil {
		t.Fatal("expected additionalModelRequestFields.output_config")
	}
	if got, want := fields.OutputConfig.Effort, "max"; got != want {
		t.Fatalf("effort = %q, want %q (xhigh clamped for sonnet 4.6)", got, want)
	}
}

func TestBuildCodeWhispererRequestAddsEnvState(t *testing.T) {
	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		System: []AnthropicSystemMessage{{
			Type: "text",
			Text: "system prefix\n<env>\nWorking directory: G:\\Code\\Claude2Kiro\nPlatform: win32\n</env>\n",
		}},
		Messages: []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
	}

	cw := buildCodeWhispererRequest(req, TokenData{})
	env := cw.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.EnvState
	if env == nil {
		t.Fatal("expected current message envState")
	}
	if got, want := env.OperatingSystem, "windows"; got != want {
		t.Fatalf("operatingSystem = %q, want %q", got, want)
	}
	if got, want := env.CurrentWorkingDirectory, "G:\\Code\\Claude2Kiro"; got != want {
		t.Fatalf("currentWorkingDirectory = %q, want %q", got, want)
	}
}

func TestBuildCodeWhispererRequestHistoryModeCurrentOnly(t *testing.T) {
	cfg := config.Default()
	cfg.Advanced.HistoryMode = "current_only"
	withConfig(t, cfg)

	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		System:    []AnthropicSystemMessage{{Type: "text", Text: "system instructions"}},
		Messages: []AnthropicRequestMessage{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "second"},
			{Role: "user", Content: "third"},
		},
	}

	cw := buildCodeWhispererRequest(req, TokenData{})
	// current_only drops conversation turns but must keep the synthesized
	// system-prompt pair — the model needs its instructions on every request.
	if got := len(cw.ConversationState.History); got != 2 {
		t.Fatalf("history length = %d, want 2 (system pair) in current_only mode", got)
	}
	first, ok := cw.ConversationState.History[0].(HistoryUserMessage)
	if !ok {
		t.Fatalf("first history entry type = %T, want HistoryUserMessage", cw.ConversationState.History[0])
	}
	if got, want := first.UserInputMessage.Content, "system instructions"; got != want {
		t.Fatalf("kept history content = %q, want system prompt %q", got, want)
	}
	if got, want := cw.ConversationState.CurrentMessage.UserInputMessage.Content, "third"; got != want {
		t.Fatalf("current content = %q, want %q", got, want)
	}
}

func TestBuildCodeWhispererRequestHistoryModeRecentKeepsSystemPrompt(t *testing.T) {
	cfg := config.Default()
	cfg.Advanced.HistoryMode = "recent"
	cfg.Advanced.HistoryRecentTurns = 1
	withConfig(t, cfg)

	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		System:    []AnthropicSystemMessage{{Type: "text", Text: "system instructions"}},
		Messages: []AnthropicRequestMessage{
			{Role: "user", Content: "old user"},
			{Role: "assistant", Content: "old assistant"},
			{Role: "user", Content: "recent user"},
			{Role: "assistant", Content: "recent assistant"},
			{Role: "user", Content: "current"},
		},
	}

	cw := buildCodeWhispererRequest(req, TokenData{})
	// 2 system-pair entries + the last user/assistant turn.
	if got := len(cw.ConversationState.History); got != 4 {
		t.Fatalf("history length = %d, want 4 (system pair + recent turn)", got)
	}
	first, ok := cw.ConversationState.History[0].(HistoryUserMessage)
	if !ok {
		t.Fatalf("first history entry type = %T, want HistoryUserMessage", cw.ConversationState.History[0])
	}
	if got, want := first.UserInputMessage.Content, "system instructions"; got != want {
		t.Fatalf("first history content = %q, want system prompt %q", got, want)
	}
	third, ok := cw.ConversationState.History[2].(HistoryUserMessage)
	if !ok {
		t.Fatalf("third history entry type = %T, want HistoryUserMessage", cw.ConversationState.History[2])
	}
	if got, want := third.UserInputMessage.Content, "recent user"; got != want {
		t.Fatalf("first kept turn content = %q, want %q", got, want)
	}
}

func TestBuildCodeWhispererRequestCurrentOnlyKeepsToolUseForCurrentToolResult(t *testing.T) {
	cfg := config.Default()
	cfg.Advanced.HistoryMode = "current_only"
	withConfig(t, cfg)

	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages: []AnthropicRequestMessage{
			{Role: "user", Content: "run a command"},
			{Role: "assistant", Content: []any{map[string]any{
				"type":  "tool_use",
				"id":    "tooluse_1",
				"name":  "Bash",
				"input": map[string]any{"command": "Write-Output TOOL_OK"},
			}}},
			{Role: "user", Content: []any{map[string]any{
				"type":        "tool_result",
				"tool_use_id": "tooluse_1",
				"content":     "TOOL_OK",
			}}},
		},
	}

	cw := buildCodeWhispererRequest(req, TokenData{})
	if got := cw.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.ToolResults; len(got) != 1 {
		t.Fatalf("current toolResults length = %d, want 1", len(got))
	}
	if got := len(cw.ConversationState.History); got != 2 {
		t.Fatalf("history length = %d, want minimal user/assistant tool-use pair", got)
	}
	if _, ok := cw.ConversationState.History[0].(HistoryUserMessage); !ok {
		t.Fatalf("first history entry type = %T, want HistoryUserMessage", cw.ConversationState.History[0])
	}
	assistant, ok := cw.ConversationState.History[1].(HistoryAssistantMessage)
	if !ok {
		t.Fatalf("second history entry type = %T, want HistoryAssistantMessage", cw.ConversationState.History[1])
	}
	if got := len(assistant.AssistantResponseMessage.ToolUses); got != 1 {
		t.Fatalf("assistant toolUses length = %d, want 1", got)
	}
	toolUse, ok := assistant.AssistantResponseMessage.ToolUses[0].(HistoryToolUse)
	if !ok {
		t.Fatalf("toolUse type = %T, want HistoryToolUse", assistant.AssistantResponseMessage.ToolUses[0])
	}
	if got, want := toolUse.ToolUseId, "tooluse_1"; got != want {
		t.Fatalf("toolUseId = %q, want %q", got, want)
	}
}

func TestBuildCodeWhispererRequestRecentKeepsToolUseForSelectedHistoricalToolResult(t *testing.T) {
	cfg := config.Default()
	cfg.Advanced.HistoryMode = "recent"
	cfg.Advanced.HistoryRecentTurns = 2
	withConfig(t, cfg)

	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages: []AnthropicRequestMessage{
			{Role: "user", Content: "run first command"},
			{Role: "assistant", Content: []any{map[string]any{
				"type":  "tool_use",
				"id":    "tooluse_1",
				"name":  "PowerShell",
				"input": map[string]any{"command": "Write-Output TOOL_OK_1"},
			}}},
			{Role: "user", Content: []any{map[string]any{
				"type":        "tool_result",
				"tool_use_id": "tooluse_1",
				"content":     "TOOL_OK_1",
			}}},
			{Role: "assistant", Content: "TOOL_OK_1"},
			{Role: "user", Content: "run second command"},
			{Role: "assistant", Content: []any{map[string]any{
				"type":  "tool_use",
				"id":    "tooluse_2",
				"name":  "PowerShell",
				"input": map[string]any{"command": "Write-Output TOOL_OK_2"},
			}}},
			{Role: "user", Content: []any{map[string]any{
				"type":        "tool_result",
				"tool_use_id": "tooluse_2",
				"content":     "TOOL_OK_2",
			}}},
		},
	}

	cw := buildCodeWhispererRequest(req, TokenData{})
	if got := len(cw.ConversationState.History); got != 6 {
		t.Fatalf("history length = %d, want selected recent entries plus first tool-use pair", got)
	}

	var toolUseIDs []string
	for _, entry := range cw.ConversationState.History {
		assistant, ok := entry.(HistoryAssistantMessage)
		if !ok {
			continue
		}
		for _, raw := range assistant.AssistantResponseMessage.ToolUses {
			toolUseIDs = append(toolUseIDs, historyToolUseID(raw))
		}
	}
	if got, want := strings.Join(toolUseIDs, ","), "tooluse_1,tooluse_2"; got != want {
		t.Fatalf("tool use ids = %q, want %q", got, want)
	}
}

func TestBuildCodeWhispererRequestHistoryModeRecent(t *testing.T) {
	cfg := config.Default()
	cfg.Advanced.HistoryMode = "recent"
	cfg.Advanced.HistoryRecentTurns = 1
	withConfig(t, cfg)

	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages: []AnthropicRequestMessage{
			{Role: "user", Content: "old user"},
			{Role: "assistant", Content: "old assistant"},
			{Role: "user", Content: "recent user"},
			{Role: "assistant", Content: "recent assistant"},
			{Role: "user", Content: "current"},
		},
	}

	cw := buildCodeWhispererRequest(req, TokenData{})
	if got := len(cw.ConversationState.History); got != 2 {
		t.Fatalf("history length = %d, want last user/assistant pair", got)
	}
	first, ok := cw.ConversationState.History[0].(HistoryUserMessage)
	if !ok {
		t.Fatalf("first history entry type = %T, want HistoryUserMessage", cw.ConversationState.History[0])
	}
	if got, want := first.UserInputMessage.Content, "recent user"; got != want {
		t.Fatalf("first recent history content = %q, want %q", got, want)
	}
}

func TestBuildCodeWhispererRequestToolModeNoneText(t *testing.T) {
	cfg := config.Default()
	cfg.Advanced.ToolMode = "none_text"
	withConfig(t, cfg)

	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Tools: []AnthropicTool{{
			Name:        "Bash",
			Description: "Run shell commands",
			InputSchema: map[string]any{
				"type": "object",
			},
		}},
		Messages: []AnthropicRequestMessage{
			{Role: "user", Content: "run ls"},
			{Role: "assistant", Content: []any{map[string]any{
				"type":  "tool_use",
				"id":    "tu_1",
				"name":  "Bash",
				"input": map[string]any{"command": "ls"},
			}}},
			{Role: "user", Content: "continue"},
		},
	}

	cw := buildCodeWhispererRequest(req, TokenData{})
	if got := cw.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools; len(got) != 0 {
		t.Fatalf("tools length = %d, want 0 in none_text mode", len(got))
	}
	assistant, ok := cw.ConversationState.History[1].(HistoryAssistantMessage)
	if !ok {
		t.Fatalf("second history entry type = %T, want HistoryAssistantMessage", cw.ConversationState.History[1])
	}
	if len(assistant.AssistantResponseMessage.ToolUses) != 0 {
		t.Fatalf("toolUses length = %d, want 0 in none_text mode", len(assistant.AssistantResponseMessage.ToolUses))
	}
	if !strings.Contains(assistant.AssistantResponseMessage.Content, "[Tool: Bash (tu_1)]") {
		t.Fatalf("assistant content = %q, want textual tool call", assistant.AssistantResponseMessage.Content)
	}
}

func TestBuildCodeWhispererRequestToolModeCompact(t *testing.T) {
	cfg := config.Default()
	cfg.Advanced.ToolMode = "compact"
	cfg.Advanced.ToolCompactMaxChars = 12
	withConfig(t, cfg)

	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
		Tools: []AnthropicTool{{
			Name:        "Bash",
			Description: "abcdefghijklmnopqrstuvwxyz",
			InputSchema: map[string]any{
				"type":        "object",
				"description": "this schema description is intentionally long",
			},
		}},
	}

	cw := buildCodeWhispererRequest(req, TokenData{})
	tool := cw.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools[0].ToolSpecification
	if got := len(tool.Description); got > 12 {
		t.Fatalf("compact description length = %d, want <= 12", got)
	}
}

func TestCompactToolDescriptionRuneSafe(t *testing.T) {
	// "descripción útil" — truncation limits that land inside the two-byte
	// "ó"/"ú" runes must back up to the rune boundary, never split it.
	desc := "descripción útil"
	for maxChars := 1; maxChars < len(desc); maxChars++ {
		got := compactToolDescription(desc, maxChars)
		if len(got) > maxChars {
			t.Fatalf("maxChars=%d: result %d bytes, want <= %d", maxChars, len(got), maxChars)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("maxChars=%d: result %q is not valid UTF-8", maxChars, got)
		}
	}
	if got := compactToolDescription(desc, len(desc)); got != desc {
		t.Fatalf("maxChars=len(desc) should return unchanged, got %q", got)
	}
}

func TestBuildCodeWhispererRequestAggressiveCachePoint(t *testing.T) {
	cfg := config.Default()
	cfg.Advanced.AggressiveCachePoints = true
	withConfig(t, cfg)

	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
		Tools: []AnthropicTool{{
			Name:        "Bash",
			Description: "Run shell commands",
			InputSchema: map[string]any{
				"type": "object",
			},
		}},
	}

	cw := buildCodeWhispererRequest(req, TokenData{})
	tools := cw.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools
	if len(tools) != 2 {
		t.Fatalf("tool entries = %d, want tool + aggressive cachePoint", len(tools))
	}
	if tools[1].CachePoint == nil || tools[1].CachePoint.Type != "default" {
		t.Fatalf("second entry = %+v, want default cachePoint", tools[1])
	}
}

func TestRequestMetricsSummaryIncludesDietFields(t *testing.T) {
	cfg := config.Default()
	cfg.Advanced.HistoryMode = "recent"
	cfg.Advanced.ToolMode = "compact"
	cfg.Advanced.AggressiveCachePoints = true
	withConfig(t, cfg)

	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages: []AnthropicRequestMessage{
			{Role: "user", Content: "old"},
			{Role: "assistant", Content: "old response"},
			{Role: "user", Content: "current"},
		},
		Tools: []AnthropicTool{{
			Name:        "Bash",
			Description: "Run shell commands",
			InputSchema: map[string]any{"type": "object"},
		}},
	}

	cwReq := buildCodeWhispererRequest(req, TokenData{})
	body, err := json.Marshal(cwReq)
	if err != nil {
		t.Fatal(err)
	}

	got := requestMetricsSummary(cwReq, len(body), cfg)
	for _, want := range []string{
		"reqBytes=",
		"approxInputTokens=",
		"historyLen=2",
		"tools=2",
		"historyMode=recent",
		"toolMode=compact",
		"aggressiveCachePoints=true",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("requestMetricsSummary() = %q, want substring %q", got, want)
		}
	}
}
