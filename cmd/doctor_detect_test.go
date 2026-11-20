package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClaudeCodeRoutingFindsWhatDisableRemoves(t *testing.T) {
	home := t.TempDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	statePath := filepath.Join(home, ".claude.json")

	found, err := ClaudeCodeRouting(settingsPath, statePath)
	if err != nil || len(found) != 0 {
		t.Fatalf("missing files: found=%v err=%v", found, err)
	}

	writeTestJSON(t, settingsPath, map[string]any{
		"env":            map[string]any{"ANTHROPIC_BASE_URL": "http://localhost:8080", "OTHER": "x"},
		"enabledPlugins": map[string]any{"kiro-proxy@claude2kiro": true},
	})
	writeTestJSON(t, statePath, map[string]any{"primaryAccountUuid": "claude2kiro-local"})
	found, err = ClaudeCodeRouting(settingsPath, statePath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"settings.json env routes Claude Code to the proxy", "kiro-proxy plugin enabled", ".claude.json has the proxy account"}
	if !reflect.DeepEqual(found, want) {
		t.Fatalf("found %v, want %v", found, want)
	}

	// After `claude2kiro disable` the same files report nothing.
	if _, err := cleanupClaude2KiroConfig(cleanupPaths{homeDir: home}); err != nil {
		t.Fatal(err)
	}
	found, err = ClaudeCodeRouting(settingsPath, statePath)
	if err != nil || len(found) != 0 {
		t.Fatalf("after disable: found=%v err=%v", found, err)
	}
}

func TestClaudeCodeRoutingUnrelatedSettings(t *testing.T) {
	home := t.TempDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	writeTestJSON(t, settingsPath, map[string]any{"env": map[string]any{"ANTHROPIC_BASE_URL": "https://api.example.com"}})
	found, err := ClaudeCodeRouting(settingsPath, filepath.Join(home, ".claude.json"))
	if err != nil || len(found) != 0 {
		t.Fatalf("unrelated settings: found=%v err=%v", found, err)
	}
}

func TestClaudeCodeRoutingInvalidJSON(t *testing.T) {
	home := t.TempDir()
	settingsPath := filepath.Join(home, "settings.json")
	if err := os.WriteFile(settingsPath, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := ClaudeCodeRouting(settingsPath, filepath.Join(home, ".claude.json"))
	if err == nil || !strings.Contains(err.Error(), "settings.json") {
		t.Fatalf("want an error naming the file, got %v", err)
	}
}

func TestDesktopGatewayConfigured(t *testing.T) {
	local := t.TempDir()
	if DesktopGatewayConfigured(local) || DesktopGatewayConfigured("") {
		t.Fatal("no config library means not configured")
	}
	lib := filepath.Join(local, "Claude-3p", "configLibrary")
	writeTestJSON(t, filepath.Join(lib, "other.json"), map[string]any{"gateway": "https://corp.example.com"})
	if DesktopGatewayConfigured(local) {
		t.Fatal("a gateway written by someone else is not ours")
	}
	writeTestJSON(t, filepath.Join(lib, "claude2kiro.json"), map[string]any{"name": "claude2kiro", "gateway": "http://localhost:8080"})
	if !DesktopGatewayConfigured(local) {
		t.Fatal("claude2kiro gateway config not detected")
	}
}
