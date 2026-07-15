// Package models fetches and caches the list of models that Kiro/CodeWhisperer
// currently exposes via the ListAvailableModels API, and resolves Anthropic model
// IDs (sent by Claude Code) to the Kiro model IDs the backend actually accepts.
//
// The static ModelMap in main.go is a curated fast path; this package is the
// dynamic layer that lets the proxy discover new models (e.g. a newly released
// Opus version) without a code change.
package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// KiroModel describes one model returned by the CodeWhisperer ListAvailableModels API.
type KiroModel struct {
	ModelID             string   `json:"modelId"`
	ModelName           string   `json:"modelName"`
	Description         string   `json:"description"`
	RateMultiplier      float64  `json:"rateMultiplier"`
	RateUnit            string   `json:"rateUnit"`
	SupportedInputTypes []string `json:"supportedInputTypes"`
	TokenLimits         struct {
		MaxInputTokens  int `json:"maxInputTokens"`
		MaxOutputTokens int `json:"maxOutputTokens"`
	} `json:"tokenLimits"`
}

type listResponse struct {
	DefaultModel KiroModel   `json:"defaultModel"`
	Models       []KiroModel `json:"models"`
}

// ListModelsURL derives the ListAvailableModels endpoint from the
// generateAssistantResponse endpoint (same host, different operation path).
func ListModelsURL(generateEndpoint string) (string, error) {
	u, err := url.Parse(generateEndpoint)
	if err != nil {
		return "", fmt.Errorf("parse endpoint: %w", err)
	}
	// The operation is exposed under the capitalized path on the CodeWhisperer host.
	u.Path = "/ListAvailableModels"
	u.RawQuery = ""
	return u.String(), nil
}

// Fetch calls ListAvailableModels and returns the available models in server order.
// generateEndpoint is the configured CodeWhisperer endpoint
// (…amazonaws.com/generateAssistantResponse); the models path is derived from it.
func Fetch(generateEndpoint, accessToken, profileArn, userAgent string, timeout time.Duration) ([]KiroModel, error) {
	listURL, err := ListModelsURL(generateEndpoint)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(listURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("origin", "AI_EDITOR")
	if profileArn != "" {
		q.Set("profileArn", profileArn)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", userAgent)

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return nil, fmt.Errorf("ListAvailableModels status %d: %s", resp.StatusCode, preview)
	}

	var lr listResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, fmt.Errorf("parse ListAvailableModels response: %w", err)
	}
	if len(lr.Models) == 0 {
		return nil, fmt.Errorf("ListAvailableModels returned no models")
	}
	return lr.Models, nil
}

// Catalog provides cached, concurrency-safe access to the available model list.
// On a fetch error it serves the last good data (if any) rather than failing,
// so a transient API hiccup never breaks model resolution on the request path.
type Catalog struct {
	mu        sync.RWMutex
	models    []KiroModel
	ids       map[string]bool
	fetchedAt time.Time

	fetchMu sync.Mutex // serializes refresh to avoid a fetch stampede
	ttl     time.Duration
	fetch   func() ([]KiroModel, error)

	onChange    func([]KiroModel) // fired when the model set changes
	fingerprint string            // fingerprint of the last seen model set
}

// NewCatalog creates a Catalog that refreshes via fetch when its cache is older
// than ttl. fetch supplies the live model list (typically a closure that reads
// the auth token + config and calls Fetch).
func NewCatalog(ttl time.Duration, fetch func() ([]KiroModel, error)) *Catalog {
	return &Catalog{ttl: ttl, fetch: fetch, ids: map[string]bool{}}
}

// SetOnChange registers a callback invoked whenever a refresh produces a model
// set different from the previous one (including the very first successful
// fetch). The callback receives a copy of the new list. It runs synchronously
// inside the refresh, so keep it cheap (e.g. regenerating a doc file). Call this
// once before the catalog is first used.
func (c *Catalog) SetOnChange(fn func([]KiroModel)) {
	if c == nil {
		return
	}
	c.onChange = fn
}

// refreshIfStale refetches when the cache is empty or older than the TTL.
// A fetch failure is swallowed when usable cached data already exists.
func (c *Catalog) refreshIfStale() {
	if c == nil || c.fetch == nil {
		return
	}

	c.mu.RLock()
	fresh := len(c.models) > 0 && time.Since(c.fetchedAt) < c.ttl
	hadData := len(c.models) > 0
	c.mu.RUnlock()
	if fresh {
		return
	}

	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()

	// Another goroutine may have refreshed while we waited for the fetch lock.
	c.mu.RLock()
	fresh = len(c.models) > 0 && time.Since(c.fetchedAt) < c.ttl
	c.mu.RUnlock()
	if fresh {
		return
	}

	models, err := c.fetch()
	if err != nil {
		// Keep serving stale-but-usable data; only the first fetch failure
		// (with nothing cached) leaves the catalog empty.
		_ = hadData
		return
	}

	ids := make(map[string]bool, len(models))
	for _, m := range models {
		ids[m.ModelID] = true
	}

	fp := Fingerprint(models)

	c.mu.Lock()
	c.models = models
	c.ids = ids
	c.fetchedAt = time.Now()
	changed := fp != c.fingerprint
	c.fingerprint = fp
	c.mu.Unlock()

	// Notify on a real change (the first fetch counts: fingerprint was "").
	if changed && c.onChange != nil {
		snapshot := make([]KiroModel, len(models))
		copy(snapshot, models)
		c.onChange(snapshot)
	}
}

// Warm forces an initial fetch (best-effort). Safe to call from a goroutine at
// startup so the first real request doesn't pay the fetch latency.
func (c *Catalog) Warm() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.fetchedAt = time.Time{} // mark stale
	c.mu.Unlock()
	c.refreshIfStale()
}

// Models returns the cached model list (refreshing if stale). May be empty if
// no fetch has ever succeeded.
func (c *Catalog) Models() []KiroModel {
	if c == nil {
		return nil
	}
	c.refreshIfStale()
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]KiroModel, len(c.models))
	copy(out, c.models)
	return out
}

// Has reports whether modelID is currently available according to the live list.
// Returns false when the catalog has never successfully fetched.
func (c *Catalog) Has(modelID string) bool {
	if c == nil {
		return false
	}
	c.refreshIfStale()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ids[modelID]
}

// Available reports whether the catalog currently holds any model data.
func (c *Catalog) Available() bool {
	if c == nil {
		return false
	}
	c.refreshIfStale()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.models) > 0
}

// Fresh reports whether the catalog holds a list from a recent SUCCESSFUL fetch
// (within the TTL). Unlike Available, it is false when the only cached data is
// stale because the live fetch is currently failing: refreshIfStale keeps
// serving stale-but-usable data on error and does not advance fetchedAt. Use
// this to distinguish "this account definitively lacks a model" (fresh list, so
// a negative is authoritative) from "we can't verify right now" (stale/empty).
func (c *Catalog) Fresh() bool {
	if c == nil {
		return false
	}
	c.refreshIfStale()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.models) > 0 && time.Since(c.fetchedAt) < c.ttl
}

// ResolveFamily returns the highest-version available model ID whose ID contains
// the given family keyword (e.g. "opus", "sonnet", "glm"). ok is false when the
// catalog is empty or no model matches.
func (c *Catalog) ResolveFamily(family string) (id string, ok bool) {
	if c == nil || family == "" {
		return "", false
	}
	c.refreshIfStale()
	c.mu.RLock()
	defer c.mu.RUnlock()

	bestScore := -1.0
	for _, m := range c.models {
		lid := strings.ToLower(m.ModelID)
		if !strings.Contains(lid, family) {
			continue
		}
		if s := versionScore(lid); s > bestScore {
			bestScore = s
			id = m.ModelID
		}
	}
	return id, id != ""
}

var (
	// claudeVerRe captures the family and version from a Claude Code model id,
	// e.g. "claude-opus-4-8", "claude-opus-4-8-20260101", "claude-opus-4.8".
	claudeVerRe = regexp.MustCompile(`^claude-(opus|sonnet|haiku)-(\d+)(?:[-.](\d+))?`)
	// numRe extracts numeric version runs like "4.8" or "5".
	numRe = regexp.MustCompile(`\d+(?:\.\d+)?`)
)

// NormalizeAnthropicID converts a Claude Code model id to the Kiro dotted form,
// e.g. "claude-opus-4-8-20260101" -> "claude-opus-4.8", "claude-sonnet-4-20250514"
// -> "claude-sonnet-4". Returns "" if id is not a versioned Claude model id.
//
// This is the mechanism that lets a brand-new Claude version route correctly the
// moment Kiro exposes it, without touching the static map.
func NormalizeAnthropicID(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	// Strip Claude Code context tags like "[1m]".
	if i := strings.IndexByte(s, '['); i >= 0 {
		s = s[:i]
	}

	m := claudeVerRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	family, major, minor := m[1], m[2], m[3]

	// A long numeric tail is a release date (e.g. 20250514), not a minor version.
	if len(minor) >= 5 {
		minor = ""
	}
	if minor == "" || minor == "0" {
		return fmt.Sprintf("claude-%s-%s", family, major)
	}
	return fmt.Sprintf("claude-%s-%s.%s", family, major, minor)
}

// versionScore extracts the last numeric version token from a model id for
// comparison within a family ("claude-opus-4.8" -> 4.8, "claude-sonnet-4" -> 4).
func versionScore(id string) float64 {
	matches := numRe.FindAllString(id, -1)
	if len(matches) == 0 {
		return 0
	}
	f, _ := strconv.ParseFloat(matches[len(matches)-1], 64)
	return f
}

// Fingerprint returns a stable hash of the set of models and their salient
// attributes (id, rate, token limits, inputs). Two lists with the same models
// produce the same fingerprint regardless of order, so it detects real changes.
func Fingerprint(list []KiroModel) string {
	lines := make([]string, 0, len(list))
	for _, m := range list {
		lines = append(lines, fmt.Sprintf("%s|%g|%d|%d|%s",
			m.ModelID, m.RateMultiplier,
			m.TokenLimits.MaxInputTokens, m.TokenLimits.MaxOutputTokens,
			strings.Join(m.SupportedInputTypes, "+")))
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// isPreview reports whether a model is an experimental/preview release, based on
// its description.
func isPreview(m KiroModel) bool {
	d := strings.ToLower(m.Description)
	return strings.Contains(d, "preview") || strings.Contains(d, "experimental")
}

// groupThousands formats an int with comma thousands separators (e.g. 1000000 ->
// "1,000,000").
func groupThousands(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// RenderMarkdown generates the content of the `/models` slash command document
// from a live model list. The table reflects the supplied list; the surrounding
// prose (resolution order, plans, notes) is constant. Output is deterministic
// (no timestamps), so callers can compare it against an existing file and only
// rewrite on a real change.
func RenderMarkdown(list []KiroModel) string {
	var b strings.Builder

	b.WriteString("---\n")
	b.WriteString("description: Show available Kiro models and credit multipliers, and switch the active model\n")
	b.WriteString("allowed-tools:\n")
	b.WriteString("  - Bash\n")
	b.WriteString("  - Read\n")
	b.WriteString("  - Edit\n")
	b.WriteString("---\n\n")
	b.WriteString("<!-- AUTO-GENERATED by `claude2kiro` from the live ListAvailableModels API.\n")
	b.WriteString("     Do not edit by hand; the proxy regenerates this file when Kiro's model\n")
	b.WriteString("     list changes. Run `claude2kiro models` to fetch the live list. -->\n\n")
	b.WriteString("The proxy resolves Anthropic model IDs (sent by Claude Code) to the Kiro model\n")
	b.WriteString("IDs the CodeWhisperer backend accepts. It fetches the live model list from\n")
	b.WriteString("`ListAvailableModels` (cached, 10-minute TTL) and falls back to a curated static\n")
	b.WriteString("map when the API is unreachable.\n\n")
	b.WriteString("To see the live list straight from Kiro, run:\n\n")
	b.WriteString("```bash\nclaude2kiro models\n```\n\n")

	fmt.Fprintf(&b, "## Available Kiro Models (%d)\n\n", len(list))
	b.WriteString("| Kiro Model ID | Name | Credit Multiplier | Max Input | Max Output | Inputs |\n")
	b.WriteString("|---------------|------|-------------------|-----------|------------|--------|\n")
	for _, m := range list {
		name := m.ModelName
		if isPreview(m) {
			name += " *(preview)*"
		}
		inputs := strings.ToLower(strings.Join(m.SupportedInputTypes, "+"))
		fmt.Fprintf(&b, "| %s | %s | %sx | %s | %s | %s |\n",
			m.ModelID, name, trimRate(m.RateMultiplier),
			groupThousands(m.TokenLimits.MaxInputTokens),
			groupThousands(m.TokenLimits.MaxOutputTokens),
			inputs)
	}

	b.WriteString(`
## Switching the active model

When the user wants to switch models, follow this flow:

1. Show the table above. If the user asks for the live list, refresh it with:
   ` + "`claude2kiro models`" + `
2. Ask which model they want.
3. Tell them to type ` + "`/model <id>`" + ` in Claude Code to switch the current
   session, e.g. ` + "`/model claude-opus-4-8`" + ` or ` + "`/model glm-5`" + `. Both
   Anthropic-style (` + "`claude-opus-4-8`" + `) and Kiro-style (` + "`claude-opus-4.8`" + `)
   ids work — the proxy resolves either. You cannot run ` + "`/model`" + ` for them;
   they must type it themselves.
4. To make the choice stick across sessions, offer one of:
   - ` + "`claude2kiro run --model <id>`" + ` when launching
   - ` + "`\"model\": \"<id>\"`" + ` in ` + "`~/.claude/settings.json`" + ` (you may edit this file for them)
   - ` + "`ANTHROPIC_MODEL=<id>`" + ` in the environment

Important: the dialog shown by ` + "`/model`" + ` (no argument) is built into the
Claude Code binary and varies with its version and login state — the proxy
cannot add entries to it. A model missing from that dialog is still usable by
passing its id explicitly: ` + "`/model <id>`" + ` always reaches the proxy, which
serves it when your account has it and otherwise catches the request and tells
you which models it can use (see resolution order below).

## Checking the current model

To answer "which model am I actually on?", do not guess from conversation
context:

1. Your own system prompt states the exact model id Claude Code sends (the
   "exact model ID is …" line); the statusline shows it too.
2. Map it to the Kiro model that actually serves it. Resolve the proxy URL
   robustly (env var, else the running proxy's advertised port, else :8080):
   ` + "```bash" + `
   base="${ANTHROPIC_BASE_URL}"
   if [ -z "$base" ]; then
     port="$(tr -d '[:space:]' < "$HOME/.claude2kiro/proxy.port" 2>/dev/null)"
     [ -n "$port" ] && base="http://127.0.0.1:$port" || base="http://localhost:8080"
   fi
   curl -s --max-time 6 "$base/resolve?model=<that-id>"
   ` + "```" + `
   (outside a session: ` + "`claude2kiro resolve <that-id>`" + `).
   The JSON answers: ` + "`kiro_model`" + ` (what serves the request) and
   ` + "`in_live_catalog`" + ` (whether THIS account can use it).

## Availability vs. actually working

The table above is this account's live list — Kiro exposes different models
per account/plan, so another user's table may differ. ` + "`in_live_catalog: false`" + `
from /resolve means Kiro will likely reject the model for this account.
To prove a model responds end-to-end (uses a fraction of a credit):

` + "```bash" + `
claude2kiro test "Reply with OK" <kiro-model-id>
` + "```" + `

## Model Resolution Order

When Claude Code sends a model ID, the proxy resolves it as follows:

1. **Static map** (exact match) — curated fast path, handles legacy remaps
   (e.g. Sonnet 3.5/3.7 → Sonnet 4.5, Opus 4.0/4.1 → Opus 4.5).
2. **Normalized + live catalog** — ` + "`claude-opus-4-8`" + ` → ` + "`claude-opus-4.8`" + `; if that
   ID is in the live list, use it. This makes new Claude releases route correctly
   the moment Kiro exposes them, with no code change.
3. **Raw catalog match** — if Claude Code already sent a Kiro-style ID.
4. **Best-effort candidate** — the normalized Kiro-form of a versioned Claude ID
   (` + "`claude-opus-4-9`" + ` → ` + "`claude-opus-4.9`" + `), else the ID as sent. The proxy does
   **not** substitute a different model here.

The proxy resolves but never *substitutes*. If the resolved model isn't servable
on your account (not in the live catalog), the proxy **catches the request and
asks you to switch** instead of routing you to a different model at a different
price: it replies with the error, your available model list, and how to change —
` + "`/model auto`" + ` (Kiro picks the best model per task) or ` + "`/model <id>`" + `. It also
never burns retries on a model Kiro can't serve (a 400 ` + "`INVALID_MODEL_ID`" + ` is
surfaced immediately, not retried).

## Kiro Plans

| Plan | Credits/month | Price |
|------|--------------|-------|
| Free | 50 | $0 |
| Pro | 1,000 | $20 |
| Pro+ | 2,000 | $40 |
| Power | 10,000 | $200 |

## Notes

- "Auto" model uses a 1.0x multiplier (Kiro picks the model per task).
- Kiro has a tool limit (~85 tools per request); Claude Code may send 150+, so the
  proxy truncates silently.
- Run ` + "`/kiro-proxy:credits`" + ` to check your current usage.
`)

	return b.String()
}

// trimRate formats a credit multiplier without trailing zeros (1.0 -> "1", 0.25 -> "0.25").
func trimRate(r float64) string {
	return strconv.FormatFloat(r, 'g', -1, 64)
}

// apiModel is one entry in the Anthropic Models API response shape
// (GET /v1/models). Claude Desktop's gateway model discovery hits this endpoint
// at launch and auto-populates its model picker from it.
//
// MaxInputTokens/MaxTokens were added to the Anthropic Models API in Mar 2026
// and are how Claude Desktop learns a model's context window — without them it
// falls back to a 200K default even for a 1M-context model. They're emitted with
// omitempty so a 0 (unknown) limit is left off rather than forced to a wrong value.
type apiModel struct {
	Type           string `json:"type"`
	ID             string `json:"id"`
	DisplayName    string `json:"display_name"`
	MaxInputTokens int    `json:"max_input_tokens,omitempty"`
	MaxTokens      int    `json:"max_tokens,omitempty"`
}

// apiModelList is the Anthropic Models API list envelope.
type apiModelList struct {
	Data    []apiModel `json:"data"`
	HasMore bool       `json:"has_more"`
	FirstID *string    `json:"first_id"`
	LastID  *string    `json:"last_id"`
}

// RenderModelsAPI renders the live Kiro model list as an Anthropic Models API
// (GET /v1/models) JSON response. Each entry's `id` is the Kiro model ID the
// CodeWhisperer backend accepts (e.g. "claude-opus-4.8"), so a model the picker
// sends back round-trips through getKiroModelID unchanged.
//
// `id` is what Claude Desktop sends on each request and is also what the user
// must put in the `inferenceModels` config if they disable discovery — so it
// must be the exact backend ID, not a friendly alias.
func RenderModelsAPI(list []KiroModel) string {
	out := apiModelList{Data: make([]apiModel, 0, len(list))}
	for _, m := range list {
		name := m.ModelName
		if name == "" {
			name = m.ModelID
		}
		if isPreview(m) {
			name += " (preview)"
		}
		out.Data = append(out.Data, apiModel{
			Type:           "model",
			ID:             m.ModelID,
			DisplayName:    name,
			MaxInputTokens: m.TokenLimits.MaxInputTokens,
			MaxTokens:      m.TokenLimits.MaxOutputTokens,
		})
	}
	if len(out.Data) > 0 {
		first := out.Data[0].ID
		last := out.Data[len(out.Data)-1].ID
		out.FirstID = &first
		out.LastID = &last
	}
	b, err := json.Marshal(out)
	if err != nil {
		return `{"data":[],"has_more":false,"first_id":null,"last_id":null}`
	}
	return string(b)
}
