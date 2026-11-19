package main

import (
	"encoding/json"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/models"
)

// Schemas as the live backend publishes them in ListAvailableModels.
var liveRequestFieldSchemas = map[string]string{
	"claude-opus-5.5":   `{"type":"object","properties":{"thinking":{"type":"object","properties":{"type":{"type":"string","enum":["adaptive"]},"display":{"type":"string","enum":["summarized","omitted"]}},"required":["type"]},"output_config":{"type":"object","properties":{"effort":{"type":"string","enum":["low","medium","high","xhigh","max"],"default":"medium"}}},"max_tokens":{"type":"integer","minimum":1024,"maximum":64000}},"additionalProperties":false}`,
	"claude-sonnet-5.5": `{"type":"object","properties":{"thinking":{"type":"object","properties":{"type":{"type":"string","enum":["adaptive","between_tools"]}},"required":["type"]}},"additionalProperties":false}`,
	"claude-opus-4.8":   `{"type":"object","properties":{"thinking":{"type":"object","properties":{"type":{"type":"string","enum":["adaptive","disabled"]}},"required":["type"]},"output_config":{"type":"object","properties":{"effort":{"type":"string","enum":["low","medium","high","xhigh","max"],"default":"high"}}},"max_tokens":{"type":"integer","minimum":1024,"maximum":64000}},"additionalProperties":false}`,
	"claude-haiku-5.5":  `{"type":"object","properties":{"thinking":{"type":"object","properties":{"type":{"type":"string","enum":["adaptive","disabled"]}},"required":["type"]},"max_tokens":{"type":"integer","minimum":1}},"additionalProperties":false}`,
	"gpt-5.6-sol":       `{"type":"object","properties":{"reasoning":{"type":"object"}}}`,
	"auto":              `null`,
}

func modelWithSchema(t *testing.T, id string) models.KiroModel {
	t.Helper()
	m := models.KiroModel{ModelID: id, ModelName: id}
	if err := json.Unmarshal([]byte(liveRequestFieldSchemas[id]), &m.RequestFields); err != nil {
		t.Fatalf("schema of %s: %v", id, err)
	}
	return m
}

// withLiveSchemaCatalog serves the models above. claude-opus-4.6 is left out on
// purpose: the catalog does not know it.
func withLiveSchemaCatalog(t *testing.T) {
	t.Helper()
	list := make([]models.KiroModel, 0, len(liveRequestFieldSchemas))
	for id := range liveRequestFieldSchemas {
		list = append(list, modelWithSchema(t, id))
	}
	withStubCatalog(t, list)
}

func withThinkingMode(t *testing.T, mode string) {
	t.Helper()
	cfg := config.Default()
	cfg.Advanced.ThinkingMode = mode
	withConfig(t, cfg)
}

func thinkingRequest(model string, thinking *AnthropicThinking, maxTokens int) AnthropicRequest {
	return AnthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
		Thinking:  thinking,
	}
}

// requestFields returns additionalModelRequestFields as it goes on the wire,
// nil when the key is absent.
func requestFields(t *testing.T, cw CodeWhispererRequest) map[string]any {
	t.Helper()
	body, err := json.Marshal(cw)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	fields, present := wire["additionalModelRequestFields"]
	if !present {
		return nil
	}
	m, ok := fields.(map[string]any)
	if !ok {
		t.Fatalf("additionalModelRequestFields = %v (%T), want an object", fields, fields)
	}
	if len(m) == 0 {
		t.Fatal("additionalModelRequestFields is present but empty; it must be omitted")
	}
	return m
}

// assertOnlyDeclaredKeys fails when the request carries a key the model's
// schema does not declare: the schemas say additionalProperties:false.
func assertOnlyDeclaredKeys(t *testing.T, kiroModel string, fields map[string]any) {
	t.Helper()
	if fields == nil {
		return
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	raw, listed := liveRequestFieldSchemas[kiroModel]
	if listed {
		json.Unmarshal([]byte(raw), &schema)
	}
	for key := range fields {
		if _, declared := schema.Properties[key]; !declared {
			t.Errorf("model %s: request carries %q, which its schema does not declare (fields=%v)", kiroModel, key, fields)
		}
	}
}

func thinkingType(fields map[string]any) string {
	thinking, ok := fields["thinking"].(map[string]any)
	if !ok {
		return ""
	}
	typ, _ := thinking["type"].(string)
	return typ
}

func TestThinkingIsTranslatedFromWhatTheClientAsked(t *testing.T) {
	withLiveSchemaCatalog(t)

	disabled := &AnthropicThinking{Type: "disabled"}
	enabled := &AnthropicThinking{Type: "enabled", BudgetTokens: 2048}
	adaptive := &AnthropicThinking{Type: "adaptive"}

	cases := []struct {
		name         string
		mode         string
		model        string
		thinking     *AnthropicThinking
		wantThinking string // "" = no thinking field
		wantNoFields bool   // additionalModelRequestFields must be absent
	}{
		{"absent, model can disable", "client", "claude-opus-4.8", nil, "disabled", false},
		{"disabled, model can disable", "client", "claude-opus-4.8", disabled, "disabled", false},
		{"enabled maps to adaptive", "client", "claude-opus-4.8", enabled, "adaptive", false},
		{"adaptive stays adaptive", "client", "claude-opus-4.8", adaptive, "adaptive", false},
		{"default mode is client", "", "claude-opus-4.8", nil, "disabled", false},
		{"unknown mode falls back to client", "bogus", "claude-opus-4.8", nil, "disabled", false},
		{"adaptive-only model cannot be turned off", "client", "claude-sonnet-5.5", nil, "", true},
		{"adaptive-only model with thinking", "client", "claude-sonnet-5.5", enabled, "adaptive", false},
		{"adaptive-only model with max_tokens schema", "client", "claude-opus-5.5", nil, "", false},
		{"backend mode never sends thinking", "backend", "claude-opus-4.8", nil, "", false},
		{"backend mode ignores client thinking", "backend", "claude-sonnet-5.5", enabled, "", true},
		{"schema-less model", "client", "auto", enabled, "", true},
		{"schema without thinking", "client", "gpt-5.6-sol", nil, "", true},
		{"model the catalog does not know", "client", "claude-opus-4.6", nil, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withThinkingMode(t, c.mode)
			cw := buildCodeWhispererRequest(thinkingRequest(c.model, c.thinking, 0), TokenData{})
			if got := cw.ConversationState.CurrentMessage.UserInputMessage.ModelId; got != c.model {
				t.Fatalf("model resolved to %q, want %q", got, c.model)
			}
			fields := requestFields(t, cw)
			assertOnlyDeclaredKeys(t, c.model, fields)
			if c.wantNoFields && fields != nil {
				t.Fatalf("additionalModelRequestFields = %v, want it omitted", fields)
			}
			if got := thinkingType(fields); got != c.wantThinking {
				t.Fatalf("thinking.type = %q, want %q (fields=%v)", got, c.wantThinking, fields)
			}
		})
	}
}

func TestMaxTokensIsForwardedClampedToTheSchema(t *testing.T) {
	withLiveSchemaCatalog(t)

	cases := []struct {
		name      string
		mode      string
		model     string
		maxTokens int
		want      float64 // 0 = no max_tokens key
	}{
		{"above the maximum", "client", "claude-opus-4.8", 128000, 64000},
		{"below the minimum", "client", "claude-opus-4.8", 64, 1024},
		{"inside the range", "client", "claude-opus-4.8", 5000, 5000},
		{"not sent by the client", "client", "claude-opus-4.8", 0, 0},
		{"no maximum published", "client", "claude-haiku-5.5", 128000, 128000},
		{"backend mode forwards it too", "backend", "claude-opus-5.5", 128000, 64000},
		{"schema without max_tokens", "client", "claude-sonnet-5.5", 128000, 0},
		{"schema-less model", "client", "auto", 128000, 0},
		{"model the catalog does not know", "client", "claude-opus-4.6", 128000, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withThinkingMode(t, c.mode)
			cw := buildCodeWhispererRequest(thinkingRequest(c.model, nil, c.maxTokens), TokenData{})
			fields := requestFields(t, cw)
			assertOnlyDeclaredKeys(t, c.model, fields)
			got, present := fields["max_tokens"]
			if c.want == 0 {
				if present {
					t.Fatalf("max_tokens = %v, want it omitted", got)
				}
				return
			}
			if got != c.want {
				t.Fatalf("max_tokens = %v, want %v", got, c.want)
			}
		})
	}
}

func TestEffortThinkingAndMaxTokensTravelTogether(t *testing.T) {
	withLiveSchemaCatalog(t)
	withThinkingMode(t, "client")

	req := thinkingRequest("claude-opus-4.8", &AnthropicThinking{Type: "enabled"}, 128000)
	req.OutputConfig = &AnthropicOutputConfig{Effort: "high"}
	fields := requestFields(t, buildCodeWhispererRequest(req, TokenData{}))
	assertOnlyDeclaredKeys(t, "claude-opus-4.8", fields)

	if got := thinkingType(fields); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive", got)
	}
	if oc, _ := fields["output_config"].(map[string]any); oc["effort"] != "high" {
		t.Fatalf("output_config = %v, want effort high", fields["output_config"])
	}
	if fields["max_tokens"] != float64(64000) {
		t.Fatalf("max_tokens = %v, want 64000", fields["max_tokens"])
	}

	// With thinking off the effort still goes along with the disabled thinking.
	req.Thinking = nil
	fields = requestFields(t, buildCodeWhispererRequest(req, TokenData{}))
	if got := thinkingType(fields); got != "disabled" {
		t.Fatalf("thinking.type = %q, want disabled", got)
	}
	if oc, _ := fields["output_config"].(map[string]any); oc["effort"] != "high" {
		t.Fatalf("output_config = %v, want effort high", fields["output_config"])
	}
}
