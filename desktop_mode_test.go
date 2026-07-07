package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureDesktopDeploymentMode pins the un-parking behavior: Desktop's
// startup gate refuses the 3p profile while claude_desktop_config.json says
// deploymentMode "1p" (how Desktop parks a profile after rejecting a managed
// config), so `desktop` must flip it to "3p" while preserving every other
// preference — and must not touch files that aren't parked.
func TestEnsureDesktopDeploymentMode(t *testing.T) {
	t.Run("parked profile is flipped, other prefs preserved", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "claude_desktop_config.json")
		orig := `{
  "coworkUserFilesPath": "C:\\Users\\X\\Claude",
  "preferences": {"localAgentModeTrustedFolders": ["G:\\Code"], "keepAwakeEnabled": true},
  "deploymentMode": "1p"
}`
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := ensureDesktopDeploymentMode(dir); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		data, _ := os.ReadFile(path)
		var cfg map[string]any
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatalf("rewritten file unparseable: %v", err)
		}
		if cfg["deploymentMode"] != "3p" {
			t.Fatalf("deploymentMode = %v, want 3p", cfg["deploymentMode"])
		}
		if cfg["coworkUserFilesPath"] != `C:\Users\X\Claude` {
			t.Fatalf("coworkUserFilesPath lost: %v", cfg["coworkUserFilesPath"])
		}
		prefs, ok := cfg["preferences"].(map[string]any)
		if !ok || prefs["keepAwakeEnabled"] != true {
			t.Fatalf("preferences lost: %v", cfg["preferences"])
		}
	})

	t.Run("not parked (3p) left untouched", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "claude_desktop_config.json")
		orig := `{"deploymentMode": "3p", "x": 1}`
		os.WriteFile(path, []byte(orig), 0o644)
		if err := ensureDesktopDeploymentMode(dir); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != orig {
			t.Fatalf("file rewritten though not parked: %s", got)
		}
	})

	t.Run("missing file is fine", func(t *testing.T) {
		if err := ensureDesktopDeploymentMode(t.TempDir()); err != nil {
			t.Fatalf("missing file should be nil, got %v", err)
		}
	})

	t.Run("corrupt file fails closed without rewrite", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "claude_desktop_config.json")
		os.WriteFile(path, []byte("{not json"), 0o644)
		if err := ensureDesktopDeploymentMode(dir); err == nil {
			t.Fatal("expected error for corrupt file")
		}
		got, _ := os.ReadFile(path)
		if string(got) != "{not json" {
			t.Fatalf("corrupt file was rewritten: %s", got)
		}
	})
}
