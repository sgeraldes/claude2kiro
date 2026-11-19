package main

import (
	"errors"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/models"
)

func modelWithEffort(id string, levels ...string) models.KiroModel {
	m := models.KiroModel{ModelID: id}
	if len(levels) > 0 {
		m.RequestFields = &models.RequestFieldsSchema{}
		m.RequestFields.Properties.OutputConfig.Properties.Effort.Enum = levels
	}
	return m
}

func effortRequest(effort string) AnthropicRequest {
	return AnthropicRequest{Model: "x", OutputConfig: &AnthropicOutputConfig{Effort: effort}}
}

// A model released after the static table was written still gets the effort
// the client asked for, because the catalog publishes its levels.
func TestEffortIsForwardedForModelsOnlyTheCatalogKnows(t *testing.T) {
	withStubCatalog(t, []models.KiroModel{
		modelWithEffort("claude-sonnet-5.5", "low", "medium", "high", "xhigh", "max"),
		modelWithEffort("auto"),
	})

	if got := resolveNativeEffort("claude-sonnet-5.5", effortRequest("medium")); got != "medium" {
		t.Fatalf("effort for claude-sonnet-5.5 = %q, want medium", got)
	}
	if got := resolveNativeEffort("auto", effortRequest("medium")); got != "" {
		t.Fatalf("effort for auto = %q, want none: auto accepts no request fields", got)
	}
	if got := resolveNativeEffort("claude-sonnet-5.5", AnthropicRequest{Model: "x"}); got != "" {
		t.Fatalf("effort with none requested = %q, want none", got)
	}
}

// Without a catalog the levels of the models known at build time still apply.
func TestEffortFallsBackToTheStaticTableWithoutACatalog(t *testing.T) {
	orig := modelCatalog
	modelCatalog = models.NewCatalog(time.Minute, func() ([]models.KiroModel, error) { return nil, errors.New("offline") })
	t.Cleanup(func() { modelCatalog = orig })

	if got := resolveNativeEffort("claude-opus-4.8", effortRequest("xhigh")); got != "xhigh" {
		t.Fatalf("effort for claude-opus-4.8 = %q, want xhigh", got)
	}
	if got := resolveNativeEffort("claude-sonnet-5.5", effortRequest("medium")); got != "" {
		t.Fatalf("effort for an unknown model = %q, want none", got)
	}
}

// A client asking for adaptive thinking is thinking-on like one asking for
// enabled thinking; the [1m] model-id rule is unchanged.
func TestIsThinkingEnabledTreatsAdaptiveLikeEnabled(t *testing.T) {
	cases := []struct {
		name string
		req  AnthropicRequest
		want bool
	}{
		{"enabled", AnthropicRequest{Model: "claude-opus-4-8", Thinking: &AnthropicThinking{Type: "enabled"}}, true},
		{"adaptive", AnthropicRequest{Model: "claude-opus-4-8", Thinking: &AnthropicThinking{Type: "adaptive"}}, true},
		{"Adaptive any case", AnthropicRequest{Model: "claude-opus-4-8", Thinking: &AnthropicThinking{Type: "Adaptive"}}, true},
		{"disabled", AnthropicRequest{Model: "claude-opus-4-8", Thinking: &AnthropicThinking{Type: "disabled"}}, false},
		{"absent", AnthropicRequest{Model: "claude-opus-4-8"}, false},
		{"[1m] model id", AnthropicRequest{Model: "claude-opus-4-8[1m]"}, true},
	}
	for _, c := range cases {
		if got := isThinkingEnabled(c.req); got != c.want {
			t.Errorf("%s: isThinkingEnabled = %v, want %v", c.name, got, c.want)
		}
	}
}

// Adaptive thinking gets the same default effort as enabled thinking.
func TestAdaptiveThinkingGetsTheDefaultEffort(t *testing.T) {
	withStubCatalog(t, []models.KiroModel{
		modelWithEffort("claude-sonnet-5.5", "low", "medium", "high", "xhigh", "max"),
	})
	for _, typ := range []string{"enabled", "adaptive"} {
		req := AnthropicRequest{Model: "x", Thinking: &AnthropicThinking{Type: typ}}
		if got := resolveNativeEffort("claude-sonnet-5.5", req); got != defaultNativeEffort {
			t.Errorf("thinking %s: effort = %q, want the default %q", typ, got, defaultNativeEffort)
		}
	}
}
