package config

import (
	"strings"
	"testing"

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

func TestAuthBlockIsOmittedWhenEmpty(t *testing.T) {
	data, err := yaml.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "auth:") {
		t.Fatalf("an empty auth block must not be written:\n%s", data)
	}
}
