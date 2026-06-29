package main

import (
	"regexp"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/config"
)

// uuidRe matches a canonical, well-formed UUID string. The version nibble is left
// open (4 for random, 5 for our name-based derivation) but the variant is fixed.
var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// withStableConversationID temporarily sets the opt-in flag, restoring the prior
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

func TestBuildCodeWhispererRequestConversationIDDefault(t *testing.T) {
	// DEFAULT behavior (flag off): a fresh random UUID per request, even when a
	// session key is present. This reproduces the original pre-mitigation behavior.
	withStableConversationID(t, false)

	const sessionUUID = "ce40736e-1347-467a-9cce-181e245edd92"
	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
		Metadata:  map[string]any{"user_id": "user_abc_session_" + sessionUUID},
	}

	cw1 := buildCodeWhispererRequest(req, TokenData{})
	cw2 := buildCodeWhispererRequest(req, TokenData{})
	if !uuidRe.MatchString(cw1.ConversationState.ConversationId) {
		t.Errorf("default conversationId = %q, not a well-formed UUID", cw1.ConversationState.ConversationId)
	}
	if cw1.ConversationState.ConversationId == cw2.ConversationState.ConversationId {
		t.Errorf("default conversationId should be random per request, got identical: %q",
			cw1.ConversationState.ConversationId)
	}
	if cw1.ConversationState.ConversationId == stableConversationID(sessionUUID) {
		t.Errorf("default conversationId should NOT be the session-derived id when flag is off")
	}
}

func TestBuildCodeWhispererRequestConversationIDStableOptIn(t *testing.T) {
	// OPT-IN behavior (flag on): a stable, session-derived conversationId reused
	// across turns of the same session.
	withStableConversationID(t, true)

	const sessionUUID = "ce40736e-1347-467a-9cce-181e245edd92"
	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
		Metadata:  map[string]any{"user_id": "user_abc_session_" + sessionUUID},
	}

	cw1 := buildCodeWhispererRequest(req, TokenData{})
	cw2 := buildCodeWhispererRequest(req, TokenData{})
	if cw1.ConversationState.ConversationId != cw2.ConversationState.ConversationId {
		t.Errorf("opt-in conversationId not stable across turns: %q != %q",
			cw1.ConversationState.ConversationId, cw2.ConversationState.ConversationId)
	}
	want := stableConversationID(sessionUUID)
	if cw1.ConversationState.ConversationId != want {
		t.Errorf("opt-in conversationId = %q, want %q (derived from session key)",
			cw1.ConversationState.ConversationId, want)
	}

	// Even with the flag on, no session key -> random UUID per request.
	reqNoMeta := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
	}
	n1 := buildCodeWhispererRequest(reqNoMeta, TokenData{}).ConversationState.ConversationId
	n2 := buildCodeWhispererRequest(reqNoMeta, TokenData{}).ConversationState.ConversationId
	if !uuidRe.MatchString(n1) {
		t.Errorf("opt-in, no-metadata conversationId = %q, not a well-formed UUID", n1)
	}
	if n1 == n2 {
		t.Errorf("opt-in with no session key should still be random per request, got identical: %q", n1)
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
	for i := 0; i < n; i++ {
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
