package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeTestJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func readTestJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCleanupClaude2KiroConfigRemovesPersistentEffects(t *testing.T) {
	homeDir := t.TempDir()
	appData := filepath.Join(t.TempDir(), "Roaming")
	localAppData := filepath.Join(t.TempDir(), "Local")
	exeDir := filepath.Join(t.TempDir(), "exe")

	paths := cleanupPaths{
		homeDir:      homeDir,
		appData:      appData,
		localAppData: localAppData,
		exeDir:       exeDir,
	}

	writeTestJSON(t, filepath.Join(homeDir, ".claude.json"), map[string]any{
		"claude2kiro":        true,
		"primaryAccountUuid": "claude2kiro-local",
		"oauthAccount": map[string]any{
			"accountUuid":  "real-account",
			"emailAddress": "user@example.com",
		},
		"customApiKeyResponses": map[string]any{
			"approved": []any{"other-key", "claude2kiro", "kiro2cc"},
			"rejected": []any{"keep-rejected"},
		},
	})

	writeTestJSON(t, filepath.Join(homeDir, ".claude", "settings.json"), map[string]any{
		"env": map[string]any{
			"AWS_ACCESS_KEY_ID":       "claude2kiro",
			"AWS_SECRET_ACCESS_KEY":   "secretkey",
			"AWS_REGION":              "us-east-1",
			"CLAUDE_CODE_USE_BEDROCK": "1",
			"ANTHROPIC_BASE_URL":      "http://localhost:8080",
			"ANTHROPIC_AUTH_TOKEN":    "claude2kiro",
			"CLAUDE_CODE_NO_FLICKER":  "1",
		},
		"model": "us.anthropic.claude-opus-4-8[1m]",
		"enabledPlugins": map[string]any{
			"kiro-proxy@claude2kiro": true,
			"other-plugin@example":   true,
		},
	})

	writeTestJSON(t, filepath.Join(homeDir, ".claude", "plugins", "known_marketplaces.json"), map[string]any{
		"claude2kiro": map[string]any{"installLocation": "old"},
		"other":       map[string]any{"installLocation": "keep"},
	})
	writeTestJSON(t, filepath.Join(homeDir, ".claude", "plugins", "installed_plugins.json"), map[string]any{
		"version": 2,
		"plugins": map[string]any{
			"kiro-proxy@claude2kiro": []any{map[string]any{"version": "dev"}},
			"other-plugin@example":   []any{map[string]any{"version": "1.0.0"}},
		},
	})

	for _, dir := range []string{
		filepath.Join(homeDir, ".claude", "plugins", "marketplaces", "claude2kiro"),
		filepath.Join(homeDir, ".claude", "plugins", "cache", "claude2kiro"),
		filepath.Join(homeDir, ".claude", "plugins", "local", "kiro-proxy"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}

	desktopConfigDir := filepath.Join(localAppData, "Claude-3p", "configLibrary")
	writeTestJSON(t, filepath.Join(desktopConfigDir, "_meta.json"), map[string]any{
		"appliedId": "c14211b2-08e2-4c99-9c7b-e3d5e2b442fa",
		"entries": []any{map[string]any{
			"id":   "c14211b2-08e2-4c99-9c7b-e3d5e2b442fa",
			"name": "Claude2Kiro (local proxy)",
		}},
	})
	writeTestJSON(t, filepath.Join(desktopConfigDir, "c14211b2-08e2-4c99-9c7b-e3d5e2b442fa.json"), map[string]any{
		"inferenceProvider":       "gateway",
		"inferenceGatewayBaseUrl": "http://localhost:8080",
	})

	startupShortcut := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "Claude2Kiro Proxy.lnk")
	if err := os.MkdirAll(filepath.Dir(startupShortcut), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(startupShortcut, []byte("shortcut"), 0600); err != nil {
		t.Fatal(err)
	}

	actions, err := cleanupClaude2KiroConfig(paths)
	if err != nil {
		t.Fatalf("cleanupClaude2KiroConfig returned error: %v", err)
	}
	if len(actions) == 0 {
		t.Fatal("cleanupClaude2KiroConfig reported no actions")
	}

	claudeJSON := readTestJSON(t, filepath.Join(homeDir, ".claude.json"))
	if _, ok := claudeJSON["claude2kiro"]; ok {
		t.Fatal(".claude.json still has claude2kiro marker")
	}
	if _, ok := claudeJSON["primaryAccountUuid"]; ok {
		t.Fatal(".claude.json still has claude2kiro primary account")
	}
	oauth := claudeJSON["oauthAccount"].(map[string]any)
	if oauth["accountUuid"] != "real-account" {
		t.Fatalf("real oauth account was not preserved: %#v", oauth)
	}
	responses := claudeJSON["customApiKeyResponses"].(map[string]any)
	approved := responses["approved"].([]any)
	if len(approved) != 1 || approved[0] != "other-key" {
		t.Fatalf("approved API keys = %#v, want only other-key", approved)
	}

	settings := readTestJSON(t, filepath.Join(homeDir, ".claude", "settings.json"))
	if _, ok := settings["model"]; ok {
		t.Fatal("settings.json still has Claude2Kiro model override")
	}
	env := settings["env"].(map[string]any)
	for _, key := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION", "CLAUDE_CODE_USE_BEDROCK", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN"} {
		if _, ok := env[key]; ok {
			t.Fatalf("settings env still has %s", key)
		}
	}
	if env["CLAUDE_CODE_NO_FLICKER"] != "1" {
		t.Fatalf("unrelated env value was not preserved: %#v", env)
	}
	enabled := settings["enabledPlugins"].(map[string]any)
	if _, ok := enabled["kiro-proxy@claude2kiro"]; ok {
		t.Fatal("kiro plugin still enabled")
	}
	if enabled["other-plugin@example"] != true {
		t.Fatalf("unrelated plugin was not preserved: %#v", enabled)
	}

	known := readTestJSON(t, filepath.Join(homeDir, ".claude", "plugins", "known_marketplaces.json"))
	if _, ok := known["claude2kiro"]; ok {
		t.Fatal("claude2kiro marketplace still registered")
	}
	if _, ok := known["other"]; !ok {
		t.Fatal("unrelated marketplace was removed")
	}
	installed := readTestJSON(t, filepath.Join(homeDir, ".claude", "plugins", "installed_plugins.json"))
	plugins := installed["plugins"].(map[string]any)
	if _, ok := plugins["kiro-proxy@claude2kiro"]; ok {
		t.Fatal("kiro plugin still installed")
	}
	if _, ok := plugins["other-plugin@example"]; !ok {
		t.Fatal("unrelated installed plugin was removed")
	}

	for _, path := range []string{
		filepath.Join(homeDir, ".claude", "plugins", "marketplaces", "claude2kiro"),
		filepath.Join(homeDir, ".claude", "plugins", "cache", "claude2kiro"),
		filepath.Join(homeDir, ".claude", "plugins", "local", "kiro-proxy"),
		desktopConfigDir,
		startupShortcut,
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after cleanup", path)
		}
	}
}
