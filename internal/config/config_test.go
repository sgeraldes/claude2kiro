package config

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDefaultEnablesStableConversationID(t *testing.T) {
	if !Default().Advanced.StableConversationID {
		t.Fatal("stable_conversation_id should default to true to match Kiro IDE sessions")
	}
}

func TestDefaultRequestDietSettingsAreConservative(t *testing.T) {
	cfg := Default()
	if cfg.Advanced.HistoryMode != "full" {
		t.Fatalf("history_mode default = %q, want full", cfg.Advanced.HistoryMode)
	}
	if cfg.Advanced.HistoryRecentTurns != 4 {
		t.Fatalf("history_recent_turns default = %d, want 4", cfg.Advanced.HistoryRecentTurns)
	}
	if cfg.Advanced.ToolMode != "full" {
		t.Fatalf("tool_mode default = %q, want full", cfg.Advanced.ToolMode)
	}
	if cfg.Advanced.ToolCompactMaxChars != 1024 {
		t.Fatalf("tool_compact_max_chars default = %d, want 1024", cfg.Advanced.ToolCompactMaxChars)
	}
	if cfg.Advanced.AggressiveCachePoints {
		t.Fatal("aggressive_cache_points should default to false")
	}
}

func TestThinkingModeDefaultsToClientAndFallsBackToIt(t *testing.T) {
	if got := Default().Advanced.ThinkingMode; got != "client" {
		t.Fatalf("thinking_mode default = %q, want client", got)
	}
	cfg := Default()
	if err := yaml.Unmarshal([]byte("advanced:\n  thinking_mode: backend\n"), cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Advanced.EffectiveThinkingMode(); got != "backend" {
		t.Fatalf("thinking_mode backend read as %q", got)
	}
	for _, v := range []string{"", "client", "bogus", "adaptive"} {
		if got := (AdvancedConfig{ThinkingMode: v}).EffectiveThinkingMode(); got != "client" {
			t.Errorf("thinking_mode %q = %q, want client", v, got)
		}
	}
	if got := (AdvancedConfig{ThinkingMode: " Backend "}).EffectiveThinkingMode(); got != "backend" {
		t.Errorf("thinking_mode %q = %q, want backend", " Backend ", got)
	}
}

func TestReasoningHistoryMaxBytesDefaultsTo4MiBAndZeroStaysZero(t *testing.T) {
	if got := Default().Advanced.ReasoningHistoryMaxBytes; got != 4194304 {
		t.Fatalf("reasoning_history_max_bytes default = %d, want 4194304", got)
	}
	cfg := Default()
	if err := yaml.Unmarshal([]byte("advanced:\n  stable_conversation_id: true\n"), cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Advanced.ReasoningHistoryMaxBytes; got != 4194304 {
		t.Fatalf("a config without the key read as %d, want the default", got)
	}
	cfg = Default()
	if err := yaml.Unmarshal([]byte("advanced:\n  reasoning_history_max_bytes: 0\n"), cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Advanced.ReasoningHistoryMaxBytes; got != 0 {
		t.Fatalf("reasoning_history_max_bytes 0 read as %d, want 0 (feature off)", got)
	}
}

func TestAuthFallbackProfilesParseAndDefaultEmpty(t *testing.T) {
	if got := Default().Auth.FallbackProfiles; len(got) != 0 {
		t.Fatalf("default fallback profiles must be empty, got %v", got)
	}
	cfg := Default()
	if err := yaml.Unmarshal([]byte("auth:\n  fallback_profiles: [kiro2, ops]\n"), cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Auth.FallbackProfiles; len(got) != 2 || got[0] != "kiro2" || got[1] != "ops" {
		t.Fatalf("parsed: %v", got)
	}
}

func TestStreamKeepaliveIntervalDefaultsTo15sAndZeroDisables(t *testing.T) {
	if got := Default().Network.StreamKeepaliveInterval; got != 15*time.Second {
		t.Fatalf("stream_keepalive_interval default = %v, want 15s", got)
	}
	// A config file written before the setting existed keeps the default.
	cfg := Default()
	if err := yaml.Unmarshal([]byte("network:\n  max_concurrent_requests: 2\n"), cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Network.StreamKeepaliveInterval; got != 15*time.Second {
		t.Fatalf("a file without the key gave %v, want 15s", got)
	}
	cfg = Default()
	if err := yaml.Unmarshal([]byte("network:\n  stream_keepalive_interval: 0s\n"), cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Network.StreamKeepaliveInterval; got != 0 {
		t.Fatalf("an explicit 0s gave %v, want 0 (pings off)", got)
	}
}

func TestAuthBlockIsOmittedWhenEmpty(t *testing.T) {
	data, err := yaml.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "auth:") {
		t.Fatalf("an empty auth block must not be written:\n%s", data)
	}
}

// A listener runs on every Set until it is removed.
func TestOnChangeRunsOnSetUntilRemoved(t *testing.T) {
	prev := Get()
	t.Cleanup(func() { Set(prev) })

	calls := 0
	remove := OnChange(func() { calls++ })
	next := *prev
	Set(&next)
	if calls != 1 {
		t.Fatalf("calls after one Set = %d, want 1", calls)
	}
	remove()
	Set(&next)
	if calls != 1 {
		t.Fatalf("calls after removal = %d, want still 1", calls)
	}
}
