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
	// toolSignatures maps each emitted tool name to its input schema as JSON.
	toolSignatures map[string]string
	toolUses       map[string]bool
	history        []any
}

var (
	requestDietControl   = requestDietReplayProfile{name: "control-full", historyMode: "full", toolMode: "full", wantToolDefinitions: 85}
	requestDietCandidate = requestDietReplayProfile{name: "candidate-recent-compact", historyMode: "recent", toolMode: "compact", recentTurns: 6, compactChars: 320, wantToolDefinitions: 85}
	// This profile deliberately documents the cache-point budget interaction.
	// It is not a rollout candidate until cache points stop displacing tools;
	// when they do, this expectation becomes 85.
	requestDietAggressiveCache = requestDietReplayProfile{name: "experimental-aggressive-cache", historyMode: "full", toolMode: "full", aggressiveCachePoints: true, wantToolDefinitions: 43}
)

// TestRequestDietControlledABReplay is an offline A/B gate calibrated to the
// observed Claude Code workload: 85 large tool definitions, a long history,
// interposed system messages, historical tool pairs, and a current tool result.
// It measures wire bytes only; backend credits and response quality still need
// the live comparison described in docs/benchmarks/2026-07-10-request-diet-ab-plan.md.
func TestRequestDietControlledABReplay(t *testing.T) {
	const currentID = "toolu_replay_13" // the latest turn's tool use
	fixture := requestDietReplayFixture(currentID)

	metrics := make(map[string]requestDietReplayMetrics)
	for _, profile := range []requestDietReplayProfile{requestDietControl, requestDietCandidate, requestDietAggressiveCache} {
		metrics[profile.name] = translateRequestDietReplay(t, profile, fixture, currentID)
	}

	control := metrics[requestDietControl.name]
	candidate := metrics[requestDietCandidate.name]
	if candidate.bytes >= control.bytes {
		t.Fatalf("candidate did not reduce wire bytes: candidate=%d control=%d", candidate.bytes, control.bytes)
	}
	if saving := 1 - float64(candidate.bytes)/float64(control.bytes); saving < 0.20 {
		t.Fatalf("candidate wire-byte saving %.1f%% is below the 20%% offline gate", saving*100)
	}
	// The 20% gate alone could be met by one lever; each must act on its own.
	if candidate.historyEntries >= control.historyEntries {
		t.Fatalf("recent history kept every entry: candidate=%d control=%d", candidate.historyEntries, control.historyEntries)
	}
	if control.cachePoints != 0 || candidate.cachePoints != 0 {
		t.Fatalf("control and candidate must not emit cache points: control=%d candidate=%d", control.cachePoints, candidate.cachePoints)
	}
	// Every user turn before the current one answers the turn before it, so
	// each tool use outside the recent window is needed by a kept tool result.
	// The candidate must keep all of them; it saves history only on the
	// injected system messages.
	for id := range control.toolUses {
		if !candidate.toolUses[id] {
			t.Errorf("candidate dropped tool use %s that a kept tool result needs", id)
		}
	}
	// Same callable tools: same names and identical schemas, not just a count.
	if len(candidate.toolSignatures) != len(control.toolSignatures) {
		t.Fatalf("candidate changed tool availability: candidate=%d control=%d", len(candidate.toolSignatures), len(control.toolSignatures))
	}
	for name, schema := range control.toolSignatures {
		got, ok := candidate.toolSignatures[name]
		if !ok {
			t.Errorf("candidate lost tool %s", name)
			continue
		}
		if got != schema {
			t.Errorf("candidate changed the schema of tool %s:\n got %s\nwant %s", name, got, schema)
		}
	}
}

// TestRequestDietReplayKeepsOlderCurrentToolUse covers the protection the
// window cannot provide: the current tool result answers turn 02, far outside
// the six recent turns, and turn 02's own user message carries no result, so
// the chain of historical tool results from the window stops at turn 03. Only
// the current-result protection can keep turn 02's tool use; the invariants
// check that the candidate kept it.
func TestRequestDietReplayKeepsOlderCurrentToolUse(t *testing.T) {
	const currentID = "toolu_replay_02"
	fixture := requestDietReplayFixture(currentID)
	full := translateRequestDietReplay(t, requestDietControl, fixture, currentID)
	candidate := translateRequestDietReplay(t, requestDietCandidate, fixture, currentID)
	if candidate.historyEntries >= full.historyEntries {
		t.Fatalf("recent history kept every entry: candidate=%d control=%d", candidate.historyEntries, full.historyEntries)
	}

	// The same window and historical-chain protection, without the current
	// result, must drop turn 02; otherwise this case proves nothing.
	keep := historyKeepMask(full.history, config.AdvancedConfig{HistoryMode: requestDietCandidate.historyMode, HistoryRecentTurns: requestDietCandidate.recentTurns})
	markToolResultHistory(keep, full.history, nil)
	for i, entry := range full.history {
		assistant, ok := entry.(HistoryAssistantMessage)
		if !ok || !keep[i] {
			continue
		}
		for _, raw := range assistant.AssistantResponseMessage.ToolUses {
			if historyToolUseID(raw) == currentID {
				t.Fatalf("the window or the historical chain already keeps %s; the case no longer exercises the current-result protection", currentID)
			}
		}
	}
}

// translateRequestDietReplay translates fixture under profile, checks the
// protocol invariants, and returns what the A/B gate compares.
func translateRequestDietReplay(t *testing.T, profile requestDietReplayProfile, fixture AnthropicRequest, currentID string) requestDietReplayMetrics {
	t.Helper()
	cfg := config.Default()
	// Explicit, so a change to the default cannot move the 85 and 43 expectations.
	cfg.Network.MaxToolsPerRequest = 85
	cfg.Advanced.HistoryMode = profile.historyMode
	cfg.Advanced.HistoryRecentTurns = profile.recentTurns
	cfg.Advanced.ToolMode = profile.toolMode
	cfg.Advanced.ToolCompactMaxChars = profile.compactChars
	cfg.Advanced.AggressiveCachePoints = profile.aggressiveCachePoints
	// Restored when the test ends, so later tests in the package never run
	// with this profile's diet settings.
	withConfig(t, cfg)

	got := buildCodeWhispererRequest(fixture, TokenData{})
	// Stored reasoning would add bytes to the history that have nothing to
	// do with the diet; the fixture's ids are unique, so none may attach.
	if got.reasoning.turns != 0 {
		t.Fatalf("%s: %d turns of stored reasoning attached; the byte comparison would not measure the diet alone", profile.name, got.reasoning.turns)
	}
	m := requestDietReplayMetrics{
		historyEntries: len(got.ConversationState.History),
		history:        got.ConversationState.History,
		toolSignatures: map[string]string{},
		toolUses:       assertRequestDietReplayInvariants(t, profile, got, currentID),
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s: marshal translated request: %v", profile.name, err)
	}
	m.bytes = len(body)
	for _, tool := range got.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools {
		switch {
		case tool.ToolSpecification != nil:
			m.toolDefinitions++
			spec := tool.ToolSpecification
			if profile.toolMode == "compact" && len(spec.Description) > profile.compactChars {
				t.Errorf("%s: tool %s description is %d chars, want at most %d", profile.name, spec.Name, len(spec.Description), profile.compactChars)
			}
			schema, err := json.Marshal(spec.InputSchema)
			if err != nil {
				t.Fatalf("%s: marshal schema of tool %s: %v", profile.name, spec.Name, err)
			}
			m.toolSignatures[spec.Name] = string(schema)
		case tool.CachePoint != nil:
			m.cachePoints++
		}
	}
	if m.toolDefinitions != profile.wantToolDefinitions {
		t.Errorf("%s: tool definitions=%d, want %d", profile.name, m.toolDefinitions, profile.wantToolDefinitions)
	}
	t.Logf("%s (current result %s): bytes=%d history=%d tool_uses=%d tool_definitions=%d cache_points=%d", profile.name, currentID, m.bytes, m.historyEntries, len(m.toolUses), m.toolDefinitions, m.cachePoints)
	return m
}

// requestDietReplayFixture builds 14 assistant tool-use turns, each followed
// by an injected system message and the user's tool result. The tool use
// named currentID is answered by the current message instead of its own user
// turn, which then carries text only.
func requestDietReplayFixture(currentID string) AnthropicRequest {
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
	const turns = 14
	for i := 0; i < turns; i++ {
		id := fmt.Sprintf("toolu_replay_%02d", i)
		messages = append(messages,
			AnthropicRequestMessage{Role: "assistant", Content: []any{
				map[string]any{"type": "text", "text": strings.Repeat(fmt.Sprintf("Analysis turn %02d. ", i), 55)},
				map[string]any{"type": "tool_use", "id": id, "name": fmt.Sprintf("mcp_replay_tool_%02d", i), "input": map[string]any{"path": fmt.Sprintf("src/file_%02d.go", i)}},
			}},
			AnthropicRequestMessage{Role: "system", Content: fmt.Sprintf("Injected reminder %02d: keep tool ancestry valid.", i)},
		)
		if id == currentID && i == turns-1 {
			break // the latest turn is answered by the current message
		}
		var user []any
		if id != currentID {
			user = append(user, map[string]any{"type": "tool_result", "tool_use_id": id, "content": strings.Repeat(fmt.Sprintf("result-%02d ", i), 70)})
		}
		user = append(user, map[string]any{"type": "text", "text": fmt.Sprintf("Continue turn %02d.", i)})
		messages = append(messages, AnthropicRequestMessage{Role: "user", Content: user})
	}

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

// assertRequestDietReplayInvariants checks the protocol invariants every
// profile must keep and returns the tool use ids left in the history.
func assertRequestDietReplayInvariants(t *testing.T, profile requestDietReplayProfile, got CodeWhispererRequest, currentID string) map[string]bool {
	t.Helper()
	current := got.ConversationState.CurrentMessage.UserInputMessage
	if !strings.Contains(current.Content, "SYNTHESIZE_FINAL_ANSWER") {
		t.Errorf("%s: current user text was lost", profile.name)
	}
	results := current.UserInputMessageContext.ToolResults
	if len(results) != 1 || results[0].ToolUseId != currentID || results[0].Status != "success" ||
		len(results[0].Content) != 1 || results[0].Content[0].Text != "FINAL_TOOL_RESULT" {
		t.Errorf("%s: current tool result was lost or changed: %#v", profile.name, results)
	}

	seenUses := map[string]bool{}
	lastRole := ""
	for i, entry := range got.ConversationState.History {
		switch msg := entry.(type) {
		case HistoryAssistantMessage:
			if lastRole == "assistant" {
				t.Errorf("%s: adjacent assistant history entries at %d", profile.name, i)
			}
			lastRole = "assistant"
			for _, raw := range msg.AssistantResponseMessage.ToolUses {
				if id := historyToolUseID(raw); id != "" {
					seenUses[id] = true
				}
			}
		case HistoryUserMessage:
			if lastRole == "user" {
				t.Errorf("%s: adjacent user history entries at %d", profile.name, i)
			}
			lastRole = "user"
			for _, result := range historyUserToolResults(msg) {
				if !seenUses[result.ToolUseId] {
					t.Errorf("%s: tool result %q at history entry %d has no prior tool use", profile.name, result.ToolUseId, i)
				}
			}
		default:
			t.Errorf("%s: unexpected history type %T at %d", profile.name, entry, i)
		}
	}
	if !seenUses[currentID] {
		t.Errorf("%s: current tool result %s has no retained prior tool use", profile.name, currentID)
	}
	return seenUses
}
