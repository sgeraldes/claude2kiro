package settings

import (
	"strings"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/config"
)

func TestStableConversationIDSettingDefaultsOnAndCanDisable(t *testing.T) {
	orig := config.Get()
	config.Set(config.Default())
	t.Cleanup(func() { config.Set(orig) })

	m := New(120, 40, false)
	var stable *Setting
	for i := range m.settings[TabAdvanced] {
		if m.settings[TabAdvanced][i].Key == "advanced.stable_conversation_id" {
			stable = &m.settings[TabAdvanced][i]
			break
		}
	}
	if stable == nil {
		t.Fatal("advanced.stable_conversation_id setting not found")
	}
	if stable.Type != TypeToggle {
		t.Fatalf("stable conversation setting type = %v, want TypeToggle", stable.Type)
	}
	if stable.Value != "true" {
		t.Fatalf("stable conversation setting value = %q, want true", stable.Value)
	}
	if stable.ExtendedHelp.DefaultValue != "true" {
		t.Fatalf("stable conversation default help = %q, want true", stable.ExtendedHelp.DefaultValue)
	}
	if stable.ExtendedHelp.RecommendedValue != "true" {
		t.Fatalf("stable conversation recommended help = %q, want true", stable.ExtendedHelp.RecommendedValue)
	}
	if strings.Contains(strings.ToLower(stable.ExtendedHelp.DetailedDesc), "experimental") {
		t.Fatalf("stable conversation help should no longer call the default behavior experimental: %q", stable.ExtendedHelp.DetailedDesc)
	}

	m.applySetting(Setting{Key: "advanced.stable_conversation_id", Value: "false"})
	if m.config.Advanced.StableConversationID {
		t.Fatal("stable conversation setting should be disableable from the TUI")
	}
}

func findAdvancedSetting(m Model, key string) *Setting {
	for i := range m.settings[TabAdvanced] {
		if m.settings[TabAdvanced][i].Key == key {
			return &m.settings[TabAdvanced][i]
		}
	}
	return nil
}

func TestRequestDietSettingsDefaultConservativeAndApply(t *testing.T) {
	orig := config.Get()
	config.Set(config.Default())
	t.Cleanup(func() { config.Set(orig) })

	m := New(120, 40, false)

	history := findAdvancedSetting(m, "advanced.history_mode")
	if history == nil {
		t.Fatal("advanced.history_mode setting not found")
	}
	if history.Type != TypeSelect {
		t.Fatalf("history mode setting type = %v, want TypeSelect", history.Type)
	}
	if history.Value != "full" {
		t.Fatalf("history mode value = %q, want full", history.Value)
	}
	if !strings.Contains(strings.Join(history.Options, ","), "current_only") {
		t.Fatalf("history mode options = %v, want current_only option", history.Options)
	}

	toolMode := findAdvancedSetting(m, "advanced.tool_mode")
	if toolMode == nil {
		t.Fatal("advanced.tool_mode setting not found")
	}
	if toolMode.Type != TypeSelect {
		t.Fatalf("tool mode setting type = %v, want TypeSelect", toolMode.Type)
	}
	if toolMode.Value != "full" {
		t.Fatalf("tool mode value = %q, want full", toolMode.Value)
	}
	if !strings.Contains(strings.Join(toolMode.Options, ","), "none_text") {
		t.Fatalf("tool mode options = %v, want none_text option", toolMode.Options)
	}

	cachePoints := findAdvancedSetting(m, "advanced.aggressive_cache_points")
	if cachePoints == nil {
		t.Fatal("advanced.aggressive_cache_points setting not found")
	}
	if cachePoints.Type != TypeToggle {
		t.Fatalf("aggressive cache points type = %v, want TypeToggle", cachePoints.Type)
	}
	if cachePoints.Value != "false" {
		t.Fatalf("aggressive cache points value = %q, want false", cachePoints.Value)
	}

	m.applySetting(Setting{Key: "advanced.history_mode", Value: "current_only"})
	m.applySetting(Setting{Key: "advanced.history_recent_turns", Value: "2"})
	m.applySetting(Setting{Key: "advanced.tool_mode", Value: "none_text"})
	m.applySetting(Setting{Key: "advanced.tool_compact_max_chars", Value: "256"})
	m.applySetting(Setting{Key: "advanced.aggressive_cache_points", Value: "true"})

	if m.config.Advanced.HistoryMode != "current_only" {
		t.Fatalf("applied history mode = %q, want current_only", m.config.Advanced.HistoryMode)
	}
	if m.config.Advanced.HistoryRecentTurns != 2 {
		t.Fatalf("applied history recent turns = %d, want 2", m.config.Advanced.HistoryRecentTurns)
	}
	if m.config.Advanced.ToolMode != "none_text" {
		t.Fatalf("applied tool mode = %q, want none_text", m.config.Advanced.ToolMode)
	}
	if m.config.Advanced.ToolCompactMaxChars != 256 {
		t.Fatalf("applied compact chars = %d, want 256", m.config.Advanced.ToolCompactMaxChars)
	}
	if !m.config.Advanced.AggressiveCachePoints {
		t.Fatal("aggressive cache points should apply true")
	}
}
