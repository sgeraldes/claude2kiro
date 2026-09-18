package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/config"
)

type requestDietReplayProfile struct {
	name                  string
	historyMode           string
	toolMode              string
	recentTurns           int
	compactChars          int
	aggressiveCachePoints bool
	wantToolDefinitions   int
}

type requestDietReplayMetrics struct {
	bytes           int
	historyEntries  int
	toolDefinitions int
	cachePoints     int
}

// TestRequestDietControlledABReplay is an offline A/B gate calibrated to the
// observed Claude Code workload: 85 large tool definitions, a long history,
// interposed system messages, historical tool pairs, and a current tool result.
// It measures wire bytes only; backend credits and response quality still need
// the live canary described in docs/benchmarks/2026-07-10-request-diet-ab-plan.md.
func TestRequestDietControlledABReplay(t *testing.T) {
	fixture := requestDietReplayFixture()
	profiles := []requestDietReplayProfile{
		{name: "control-full", historyMode: "full", toolMode: "full", wantToolDefinitions: 85},
		{name: "candidate-recent-compact", historyMode: "recent", toolMode: "compact", recentTurns: 6, compactChars: 320, wantToolDefinitions: 85},
		// This profile deliberately documents the cache-point budget interaction.
		// It is not a rollout candidate until cache points stop displacing tools.
		{name: "experimental-aggressive-cache", historyMode: "full", toolMode: "full", aggressiveCachePoints: true, wantToolDefinitions: 43},
	}

	metrics := make(map[string]requestDietReplayMetrics, len(profiles))
	for _, profile := range profiles {
		cfg := config.Default()
		cfg.Network.MaxToolsPerRequest = 85
		cfg.Advanced.HistoryMode = profile.historyMode
		cfg.Advanced.HistoryRecentTurns = profile.recentTurns
		cfg.Advanced.ToolMode = profile.toolMode
		cfg.Advanced.ToolCompactMaxChars = profile.compactChars
		cfg.Advanced.AggressiveCachePoints = profile.aggressiveCachePoints
		config.Set(cfg)

		got := buildCodeWhispererRequest(fixture, TokenData{})
		assertRequestDietReplayInvariants(t, profile, got)
		body, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("%s: marshal translated request: %v", profile.name, err)
		}
		m := requestDietReplayMetrics{bytes: len(body), historyEntries: len(got.ConversationState.History)}
		for _, tool := range got.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools {
			switch {
			case tool.ToolSpecification != nil:
				m.toolDefinitions++
			case tool.CachePoint != nil:
				m.cachePoints++
			}
		}
		metrics[profile.name] = m
		t.Logf("%s: bytes=%d history=%d tool_definitions=%d cache_points=%d", profile.name, m.bytes, m.historyEntries, m.toolDefinitions, m.cachePoints)
	}

	control := metrics["control-full"]
	candidate := metrics["candidate-recent-compact"]
	if candidate.bytes >= control.bytes {
		t.Fatalf("candidate did not reduce wire bytes: candidate=%d control=%d", candidate.bytes, control.bytes)
	}
	if saving := 1 - float64(candidate.bytes)/float64(control.bytes); saving < 0.20 {
		t.Fatalf("candidate wire-byte saving %.1f%% is below the 20%% offline gate", saving*100)
	}
	if candidate.toolDefinitions != control.toolDefinitions {
		t.Fatalf("candidate silently changed tool availability: candidate=%d control=%d", candidate.toolDefinitions, control.toolDefinitions)
	}
}

func requestDietReplayFixture() AnthropicRequest {
	tools := make([]AnthropicTool, 85)
	for i := range tools {
		tools[i] = AnthropicTool{
			Name:        fmt.Sprintf("mcp_replay_tool_%02d", i),
			Description: strings.Repeat(fmt.Sprintf("Tool %02d safely reads structured project data. ", i), 34),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":  map[string]any{"type": "string", "description": strings.Repeat("Repository-relative path. ", 12)},
					"query": map[string]any{"type": "string", "description": strings.Repeat("Exact query to execute. ", 12)},
				},
				"required": []any{"path"},
			},
		}
	}

	messages := []AnthropicRequestMessage{{Role: "user", Content: "Inspect the request translator and preserve protocol invariants."}}
	for i := 0; i < 14; i++ {
		id := fmt.Sprintf("toolu_replay_%02d", i)
		messages = append(messages,
			AnthropicRequestMessage{Role: "assistant", Content: []any{
				map[string]any{"type": "text", "text": strings.Repeat(fmt.Sprintf("Analysis turn %02d. ", i), 55)},
				map[string]any{"type": "tool_use", "id": id, "name": fmt.Sprintf("mcp_replay_tool_%02d", i), "input": map[string]any{"path": fmt.Sprintf("src/file_%02d.go", i)}},
			}},
			AnthropicRequestMessage{Role: "system", Content: fmt.Sprintf("Injected reminder %02d: keep tool ancestry valid.", i)},
			AnthropicRequestMessage{Role: "user", Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": id, "content": strings.Repeat(fmt.Sprintf("result-%02d ", i), 70)},
				map[string]any{"type": "text", "text": fmt.Sprintf("Continue turn %02d.", i)},
			}},
		)
	}

	currentID := "toolu_replay_13"
	messages = append(messages, AnthropicRequestMessage{Role: "user", Content: []any{
		map[string]any{"type": "tool_result", "tool_use_id": currentID, "content": "FINAL_TOOL_RESULT"},
		map[string]any{"type": "text", "text": "SYNTHESIZE_FINAL_ANSWER"},
	}})

	return AnthropicRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 4096,
		System: []AnthropicSystemMessage{
			{Type: "text", Text: strings.Repeat("You are an authorized software engineering assistant. ", 80)},
		},
		Messages: messages,
		Tools:    tools,
		Metadata: map[string]any{"user_id": `{"session_id":"11111111-2222-4333-8444-555555555555"}`},
	}
}

func assertRequestDietReplayInvariants(t *testing.T, profile requestDietReplayProfile, got CodeWhispererRequest) {
	t.Helper()
	current := got.ConversationState.CurrentMessage.UserInputMessage
	if !strings.Contains(current.Content, "SYNTHESIZE_FINAL_ANSWER") {
		t.Errorf("%s: current user text was lost", profile.name)
	}
	if len(current.UserInputMessageContext.ToolResults) != 1 || current.UserInputMessageContext.ToolResults[0].ToolUseId != "toolu_replay_13" {
		t.Errorf("%s: current tool result was lost or changed: %#v", profile.name, current.UserInputMessageContext.ToolResults)
	}

	seenUses := map[string]bool{}
	lastRole := ""
	toolDefinitions := 0
	for i, entry := range got.ConversationState.History {
		switch msg := entry.(type) {
		case HistoryAssistantMessage:
			if lastRole == "assistant" {
				t.Errorf("%s: adjacent assistant history entries at %d", profile.name, i)
			}
			lastRole = "assistant"
			for _, raw := range msg.AssistantResponseMessage.ToolUses {
				switch toolUse := raw.(type) {
				case HistoryToolUse:
					seenUses[toolUse.ToolUseId] = true
				case map[string]any:
					if id, _ := toolUse["toolUseId"].(string); id != "" {
						seenUses[id] = true
					}
				}
			}
		case HistoryUserMessage:
			if lastRole == "user" {
				t.Errorf("%s: adjacent user history entries at %d", profile.name, i)
			}
			lastRole = "user"
			if msg.UserInputMessage.UserInputMessageContext != nil {
				for _, result := range msg.UserInputMessage.UserInputMessageContext.ToolResults {
					if !seenUses[result.ToolUseId] {
						t.Errorf("%s: tool result %q at history entry %d has no prior tool use", profile.name, result.ToolUseId, i)
					}
				}
			}
		default:
			t.Errorf("%s: unexpected history type %T at %d", profile.name, entry, i)
		}
	}
	if !seenUses["toolu_replay_13"] {
		t.Errorf("%s: current tool result has no retained prior tool use", profile.name)
	}

	for _, tool := range current.UserInputMessageContext.Tools {
		if tool.ToolSpecification != nil {
			toolDefinitions++
		}
	}
	if toolDefinitions != profile.wantToolDefinitions {
		t.Errorf("%s: tool definitions=%d, want %d", profile.name, toolDefinitions, profile.wantToolDefinitions)
	}
}
