package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// setTestHome points the home-dir resolution at a temp dir for the test.
func setTestHome(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	} else {
		t.Setenv("HOME", dir)
	}
}

func readClaudeJSON(t *testing.T, home string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse .claude.json: %v", err)
	}
	return cfg
}

func TestEnsureClaudeConfigVirginMachine(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	ensureClaudeConfig()

	cfg := readClaudeJSON(t, home)
	if v, _ := cfg["hasCompletedOnboarding"].(bool); !v {
		t.Error("hasCompletedOnboarding not set")
	}
	if v, _ := cfg["hasSeenApiKeyPrompt"].(bool); !v {
		t.Error("hasSeenApiKeyPrompt not set")
	}
	oauth, _ := cfg["oauthAccount"].(map[string]any)
	if oauth == nil {
		t.Fatal("oauthAccount stub not created")
	}
	if v, _ := oauth["isApiKeyPrimaryAuth"].(bool); !v {
		t.Error("oauthAccount stub missing isApiKeyPrimaryAuth")
	}
	if cfg["primaryAccountUuid"] != "claude2kiro-local" {
		t.Errorf("primaryAccountUuid = %v", cfg["primaryAccountUuid"])
	}
	if v, _ := cfg["claude2kiro"].(bool); !v {
		t.Error("claude2kiro marker not set")
	}
	responses, _ := cfg["customApiKeyResponses"].(map[string]any)
	approved, _ := responses["approved"].([]any)
	if len(approved) != 1 || approved[0] != "claude2kiro" {
		t.Errorf("approved = %#v, want [claude2kiro]", approved)
	}
}

func TestEnsureClaudeConfigPreservesExistingLogin(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	existing := map[string]any{
		"hasCompletedOnboarding": true,
		"oauthAccount": map[string]any{
			"accountUuid": "real-account",
		},
		"primaryAccountUuid": "real-account",
		"customApiKeyResponses": map[string]any{
			"approved": []any{"other-key"},
		},
	}
	data, _ := json.Marshal(existing)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	ensureClaudeConfig()

	cfg := readClaudeJSON(t, home)
	oauth, _ := cfg["oauthAccount"].(map[string]any)
	if oauth["accountUuid"] != "real-account" {
		t.Errorf("real oauthAccount was clobbered: %#v", oauth)
	}
	if cfg["primaryAccountUuid"] != "real-account" {
		t.Errorf("primaryAccountUuid was clobbered: %v", cfg["primaryAccountUuid"])
	}
	if _, ok := cfg["claude2kiro"]; ok {
		t.Error("claude2kiro marker set despite real login present")
	}
	responses, _ := cfg["customApiKeyResponses"].(map[string]any)
	approved, _ := responses["approved"].([]any)
	if len(approved) != 2 || approved[0] != "other-key" || approved[1] != "claude2kiro" {
		t.Errorf("approved = %#v, want [other-key claude2kiro]", approved)
	}
	// hasSeenApiKeyPrompt should be filled in even on existing installs.
	if v, _ := cfg["hasSeenApiKeyPrompt"].(bool); !v {
		t.Error("hasSeenApiKeyPrompt not set on existing install")
	}
}

func TestEnsureClaudeConfigLeavesCorruptFile(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	corrupt := `{"oauthAccount": {"accountUuid": "real-acc` // truncated JSON
	path := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(path, []byte(corrupt), 0600); err != nil {
		t.Fatal(err)
	}

	ensureClaudeConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != corrupt {
		t.Errorf("corrupt .claude.json was rewritten: %s", data)
	}
}

func TestEnsureClaudeConfigIdempotent(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	ensureClaudeConfig()
	first, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	ensureClaudeConfig()
	second, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("second run modified the file")
	}
}

func TestEnsureDesktopGatewayConfigWritesConfig(t *testing.T) {
	localAppData := t.TempDir()
	t.Setenv("LOCALAPPDATA", localAppData)

	if err := ensureDesktopGatewayConfig("http://localhost:8080"); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(localAppData, "Claude-3p", "configLibrary")
	metaData, err := os.ReadFile(filepath.Join(dir, "_meta.json"))
	if err != nil {
		t.Fatalf("read _meta.json: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["appliedId"] != desktopGatewayConfigID {
		t.Errorf("appliedId = %v", meta["appliedId"])
	}

	entryData, err := os.ReadFile(filepath.Join(dir, desktopGatewayConfigID+".json"))
	if err != nil {
		t.Fatalf("read entry json: %v", err)
	}
	var entry map[string]any
	if err := json.Unmarshal(entryData, &entry); err != nil {
		t.Fatal(err)
	}
	if entry["inferenceProvider"] != "gateway" {
		t.Errorf("inferenceProvider = %v", entry["inferenceProvider"])
	}
	if entry["inferenceGatewayBaseUrl"] != "http://localhost:8080" {
		t.Errorf("inferenceGatewayBaseUrl = %v", entry["inferenceGatewayBaseUrl"])
	}
	// Desktop's custom3p validator requires a credential for the gateway
	// provider; without these the config is rejected and Desktop won't route.
	if entry["inferenceCredentialKind"] != "static" {
		t.Errorf("inferenceCredentialKind = %v, want static", entry["inferenceCredentialKind"])
	}
	if key, _ := entry["inferenceApiKey"].(string); key == "" {
		t.Errorf("inferenceApiKey = %q, want a non-empty dummy credential", key)
	}
}

func TestEnsureDesktopGatewayConfigUpdatesPort(t *testing.T) {
	localAppData := t.TempDir()
	t.Setenv("LOCALAPPDATA", localAppData)

	if err := ensureDesktopGatewayConfig("http://localhost:8080"); err != nil {
		t.Fatal(err)
	}
	if err := ensureDesktopGatewayConfig("http://localhost:9090"); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(localAppData, "Claude-3p", "configLibrary")
	entryData, err := os.ReadFile(filepath.Join(dir, desktopGatewayConfigID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal(entryData, &entry); err != nil {
		t.Fatal(err)
	}
	if entry["inferenceGatewayBaseUrl"] != "http://localhost:9090" {
		t.Errorf("inferenceGatewayBaseUrl = %v, want updated port", entry["inferenceGatewayBaseUrl"])
	}
}

func TestEnsureDesktopGatewayConfigLeavesForeignConfig(t *testing.T) {
	localAppData := t.TempDir()
	t.Setenv("LOCALAPPDATA", localAppData)

	dir := filepath.Join(localAppData, "Claude-3p", "configLibrary")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	foreign := `{"appliedId":"someone-elses-config","entries":[]}`
	metaPath := filepath.Join(dir, "_meta.json")
	if err := os.WriteFile(metaPath, []byte(foreign), 0644); err != nil {
		t.Fatal(err)
	}

	if err := ensureDesktopGatewayConfig("http://localhost:8080"); err == nil {
		t.Fatal("expected an error for a foreign managed config so the caller aborts the launch")
	}

	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != foreign {
		t.Errorf("foreign managed config was overwritten: %s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, desktopGatewayConfigID+".json")); !os.IsNotExist(err) {
		t.Error("claude2kiro entry was written alongside a foreign config")
	}
}

func TestFindClaudeBinaryFallsBackToInstallLocations(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	// Hide any real claude on PATH.
	t.Setenv("PATH", home)
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
		t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	}

	if _, err := findClaudeBinary(); err == nil {
		t.Fatal("expected error with no claude anywhere")
	}

	name := "claude"
	if runtime.GOOS == "windows" {
		name = "claude.exe"
	}
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, name), []byte("stub"), 0755); err != nil {
		t.Fatal(err)
	}

	p, err := findClaudeBinary()
	if err != nil {
		t.Fatalf("findClaudeBinary: %v", err)
	}
	if p != filepath.Join(binDir, name) {
		t.Errorf("found %s, want %s", p, filepath.Join(binDir, name))
	}
}
