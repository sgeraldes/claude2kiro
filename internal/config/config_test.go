package config

import "testing"

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
