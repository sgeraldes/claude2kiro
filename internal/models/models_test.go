package models

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeAnthropicID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"claude-opus-4-8", "claude-opus-4.8"},
		{"claude-opus-4-8-20260101", "claude-opus-4.8"},
		{"claude-opus-4.8", "claude-opus-4.8"},
		{"claude-opus-4-8[1m]", "claude-opus-4.8"},
		{"claude-opus-4-7", "claude-opus-4.7"},
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4.5"},
		{"claude-sonnet-4-20250514", "claude-sonnet-4"}, // date, not minor
		{"claude-sonnet-4", "claude-sonnet-4"},
		{"claude-haiku-4-5", "claude-haiku-4.5"},
		{"claude-opus-5-0", "claude-opus-5"}, // future version, minor 0 -> no suffix
		{"claude-opus-5-1", "claude-opus-5.1"},
		{"CLAUDE-OPUS-4-8", "claude-opus-4.8"}, // case-insensitive
		{"deepseek-3.2", ""},                   // not a claude id
		{"gpt-4", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeAnthropicID(c.in); got != c.want {
			t.Errorf("NormalizeAnthropicID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestVersionScore(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"claude-opus-4.8", 4.8},
		{"claude-opus-4.7", 4.7},
		{"claude-sonnet-4", 4},
		{"minimax-m2.5", 2.5},
		{"qwen3-coder-next", 3},
		{"glm-5", 5},
		{"auto", 0},
	}
	for _, c := range cases {
		if got := versionScore(c.in); got != c.want {
			t.Errorf("versionScore(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func sampleModels() []KiroModel {
	mk := func(id string, rate float64) KiroModel {
		m := KiroModel{ModelID: id, ModelName: id, RateMultiplier: rate}
		m.TokenLimits.MaxInputTokens = 200000
		m.TokenLimits.MaxOutputTokens = 64000
		return m
	}
	return []KiroModel{
		mk("auto", 1.0),
		mk("claude-opus-4.8", 2.2),
		mk("claude-opus-4.7", 2.2),
		mk("claude-opus-4.6", 2.2),
		mk("claude-sonnet-4.6", 1.3),
		mk("claude-haiku-4.5", 0.4),
		mk("glm-5", 0.5),
	}
}

func TestCatalogHasAndResolveFamily(t *testing.T) {
	cat := NewCatalog(time.Minute, func() ([]KiroModel, error) {
		return sampleModels(), nil
	})

	if !cat.Has("claude-opus-4.8") {
		t.Error("expected catalog to have claude-opus-4.8")
	}
	if cat.Has("claude-opus-9.9") {
		t.Error("did not expect catalog to have claude-opus-9.9")
	}

	if id, ok := cat.ResolveFamily("opus"); !ok || id != "claude-opus-4.8" {
		t.Errorf("ResolveFamily(opus) = %q,%v; want claude-opus-4.8,true", id, ok)
	}
	if id, ok := cat.ResolveFamily("glm"); !ok || id != "glm-5" {
		t.Errorf("ResolveFamily(glm) = %q,%v; want glm-5,true", id, ok)
	}
	if _, ok := cat.ResolveFamily("nonexistent"); ok {
		t.Error("ResolveFamily(nonexistent) should be false")
	}
}

func TestCatalogServesStaleOnError(t *testing.T) {
	var calls int
	var fail bool
	var mu sync.Mutex
	cat := NewCatalog(0, func() ([]KiroModel, error) { // ttl=0 -> always stale
		mu.Lock()
		defer mu.Unlock()
		calls++
		if fail {
			return nil, fmt.Errorf("boom")
		}
		return sampleModels(), nil
	})

	// First call populates the cache.
	if !cat.Has("claude-opus-4.8") {
		t.Fatal("expected initial fetch to populate cache")
	}

	// Now make the fetcher fail; the catalog must still serve the last good data.
	mu.Lock()
	fail = true
	mu.Unlock()
	if !cat.Has("claude-opus-4.8") {
		t.Error("expected stale cache to be served on fetch error")
	}
}

func TestCatalogEmptyOnFirstFailure(t *testing.T) {
	cat := NewCatalog(time.Minute, func() ([]KiroModel, error) {
		return nil, fmt.Errorf("no network")
	})
	if cat.Available() {
		t.Error("expected empty catalog when first fetch fails")
	}
	if cat.Has("claude-opus-4.8") {
		t.Error("empty catalog should not report any model as available")
	}
	if _, ok := cat.ResolveFamily("opus"); ok {
		t.Error("empty catalog ResolveFamily should be false")
	}
}

func TestFingerprintOrderIndependent(t *testing.T) {
	a := sampleModels()
	b := make([]KiroModel, len(a))
	copy(b, a)
	// Reverse b.
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	if Fingerprint(a) != Fingerprint(b) {
		t.Error("fingerprint should be order-independent")
	}

	// A changed rate must change the fingerprint.
	c := make([]KiroModel, len(a))
	copy(c, a)
	c[1].RateMultiplier = 9.9
	if Fingerprint(a) == Fingerprint(c) {
		t.Error("fingerprint should change when a model's rate changes")
	}

	// An added model must change the fingerprint.
	d := append(append([]KiroModel{}, a...), KiroModel{ModelID: "new-model-1"})
	if Fingerprint(a) == Fingerprint(d) {
		t.Error("fingerprint should change when a model is added")
	}
}

func TestOnChangeFiresOnChangeOnly(t *testing.T) {
	list := sampleModels()
	var calls int
	cat := NewCatalog(0, func() ([]KiroModel, error) { // ttl=0 -> refresh every call
		return list, nil
	})
	cat.SetOnChange(func([]KiroModel) { calls++ })

	cat.Models() // first fetch -> change (from empty)
	cat.Models() // same set -> no change
	cat.Models() // same set -> no change
	if calls != 1 {
		t.Errorf("onChange fired %d times, want 1 (only on the initial change)", calls)
	}

	// Mutate the served list; next refresh should fire onChange again.
	list = append(append([]KiroModel{}, list...), KiroModel{ModelID: "extra-model"})
	cat.Models()
	if calls != 2 {
		t.Errorf("onChange fired %d times after a real change, want 2", calls)
	}
}

func TestRenderMarkdown(t *testing.T) {
	out := RenderMarkdown(sampleModels())

	// Deterministic: rendering the same list twice is byte-identical (no timestamps).
	if out != RenderMarkdown(sampleModels()) {
		t.Error("RenderMarkdown should be deterministic")
	}
	for _, want := range []string{
		"description: Show available Kiro models",
		"allowed-tools",
		"AUTO-GENERATED",
		"| claude-opus-4.8 |",
		"## Switching the active model",
		"/model <id>",
		"200,000", // thousands-grouped token limit
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered markdown missing %q", want)
		}
	}
}

func TestGroupThousands(t *testing.T) {
	cases := map[int]string{
		0:       "0",
		999:     "999",
		1000:    "1,000",
		200000:  "200,000",
		1000000: "1,000,000",
		164000:  "164,000",
	}
	for in, want := range cases {
		if got := groupThousands(in); got != want {
			t.Errorf("groupThousands(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestListModelsURL(t *testing.T) {
	got, err := ListModelsURL("https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://codewhisperer.us-east-1.amazonaws.com/ListAvailableModels"
	if got != want {
		t.Errorf("ListModelsURL = %q, want %q", got, want)
	}
}
