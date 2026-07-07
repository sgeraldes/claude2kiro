package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAgent(t *testing.T, subdir, name, meta string, jsonl []string) {
	t.Helper()
	base := filepath.Join(subdir, "agent-a"+name+"-deadbeef")
	if err := os.WriteFile(base+".meta.json", []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	content := strings.Join(jsonl, "\n") + "\n"
	if err := os.WriteFile(base+".jsonl", []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSessionStats(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	session := "a1f0f199-d1de-447a-ab2c-ceaa090ceb21"
	subdir := filepath.Join(root, "projects", "someproj", session, "subagents")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeAgent(t, subdir, "dev-licenses",
		`{"agentType":"dev-licenses","name":"dev-licenses","description":"Build Licenses view","model":"opus","color":"yellow"}`,
		[]string{
			`{"type":"user","message":{"role":"user","content":"go"},"timestamp":"2026-07-06T08:00:00.000Z"}`,
			`{"type":"assistant","timestamp":"2026-07-06T08:00:10.000Z","message":{"model":"claude-opus-4-8","usage":{"input_tokens":50000,"output_tokens":400}}}`,
			`{"type":"assistant","timestamp":"2026-07-06T08:00:30.000Z","message":{"model":"claude-opus-4-8","usage":{"input_tokens":60000,"output_tokens":600}}}`,
		})

	stats := SessionStats(session)
	if len(stats) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(stats))
	}
	s := stats[0]
	if s.Name != "dev-licenses" || s.Description != "Build Licenses view" || s.Color != "yellow" {
		t.Fatalf("meta not parsed: %+v", s.Info)
	}
	if s.Turns != 2 {
		t.Fatalf("Turns=%d, want 2", s.Turns)
	}
	if s.OutputTokens != 1000 {
		t.Fatalf("OutputTokens=%d, want 1000", s.OutputTokens)
	}
	if s.PeakInputToken != 60000 {
		t.Fatalf("PeakInputToken=%d, want 60000", s.PeakInputToken)
	}
	if s.TotalInputTok != 110000 {
		t.Fatalf("TotalInputTok=%d, want 110000", s.TotalInputTok)
	}
	if d := s.Duration(); d.Seconds() != 20 {
		t.Fatalf("Duration=%v, want 20s", d)
	}
	if tps := s.OutputTokensPerSec(); tps != 50 { // 1000 tokens / 20s
		t.Fatalf("OutputTokensPerSec=%v, want 50", tps)
	}
}

func TestSessionStats_NoLocalFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	if got := SessionStats("nonexistent-session"); got != nil {
		t.Fatalf("expected nil for missing session, got %v", got)
	}
}

func TestMostRecentSession(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	session := "beefbeef-d1de-447a-ab2c-ceaa090ceb21"
	subdir := filepath.Join(root, "projects", "p", session, "subagents")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAgent(t, subdir, "dev-x", `{"name":"dev-x"}`,
		[]string{`{"type":"assistant","timestamp":"2026-07-06T08:00:10.000Z","message":{"usage":{"input_tokens":1,"output_tokens":1}}}`})
	if got := MostRecentSession(); got != session {
		t.Fatalf("MostRecentSession=%q, want %q", got, session)
	}
}
