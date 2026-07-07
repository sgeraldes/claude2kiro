package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

func TestAgentsEndpointAndDashboard(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	session := "aaaa1111-2222-4333-8444-ceaa090ceb21"
	subdir := filepath.Join(root, "projects", "proj", session, "subagents")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(subdir, "agent-adev-x-deadbeef")
	os.WriteFile(base+".meta.json", []byte(`{"name":"dev-x","model":"opus","color":"yellow"}`), 0o644)
	os.WriteFile(base+".jsonl", []byte(
		`{"type":"assistant","timestamp":"2026-07-06T08:00:10.000Z","message":{"usage":{"input_tokens":100,"output_tokens":5}}}`+"\n"), 0o644)

	mux := buildServerMux(logger.NewLogger(10))

	// /agents?session= returns the named agent.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/agents?session="+session, nil))
	if rec.Code != 200 {
		t.Fatalf("/agents status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Session string           `json:"session"`
		Agents  []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v (%s)", err, rec.Body.String())
	}
	if len(resp.Agents) != 1 || resp.Agents[0]["name"] != "dev-x" {
		t.Fatalf("unexpected agents payload: %s", rec.Body.String())
	}
	if _, ok := resp.Agents[0]["inputTokensPerSec"]; !ok {
		t.Fatalf("derived inputTokensPerSec missing: %s", rec.Body.String())
	}

	// /agents with no local files returns an empty (non-null) list.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/agents?session=nope", nil))
	if !strings.Contains(rec.Body.String(), `"agents":[]`) {
		t.Fatalf("expected empty agents array, got %s", rec.Body.String())
	}

	// /dashboard embeds the Subagents card.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/dashboard", nil))
	if !strings.Contains(rec.Body.String(), "Subagents") || !strings.Contains(rec.Body.String(), "loadAgents") {
		t.Fatal("dashboard HTML missing the Subagents card / loader")
	}
}
