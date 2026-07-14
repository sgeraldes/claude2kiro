package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/models"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
	"github.com/sgeraldes/claude2kiro/parser"
)

// withStubCatalog temporarily replaces the package model catalog with one backed
// by a fixed model list, restoring the original when the test finishes.
func withStubCatalog(t *testing.T, list []models.KiroModel) {
	t.Helper()
	orig := modelCatalog
	modelCatalog = models.NewCatalog(time.Minute, func() ([]models.KiroModel, error) {
		return list, nil
	})
	t.Cleanup(func() { modelCatalog = orig })
}

func mk(id string) models.KiroModel {
	return models.KiroModel{ModelID: id, ModelName: id}
}

func stubList() []models.KiroModel {
	return []models.KiroModel{
		mk("auto"),
		mk("claude-opus-4.8"),
		mk("claude-opus-4.7"),
		mk("claude-opus-4.6"),
		mk("claude-sonnet-4.6"),
		mk("claude-sonnet-4"),
		mk("claude-haiku-4.5"),
		mk("deepseek-3.2"),
		mk("minimax-m2.5"),
		mk("glm-5"),
		mk("qwen3-coder-next"),
	}
}

func TestGetKiroModelID(t *testing.T) {
	withStubCatalog(t, stubList())

	cases := []struct {
		name string
		in   string
		want string
	}{
		// 1. Static map exact match.
		{"static opus 4.8 dash", "claude-opus-4-8", "claude-opus-4.8"},
		{"static opus 4.8 dot", "claude-opus-4.8", "claude-opus-4.8"},
		{"static opus 4.7", "claude-opus-4-7", "claude-opus-4.7"},
		{"static glm-5", "glm-5", "glm-5"},
		{"static legacy sonnet 3.7", "claude-3-7-sonnet-20250219", "claude-sonnet-4.5"},

		// 2. Unknown future version -> normalized form found in live catalog.
		{"dated opus 4.8", "claude-opus-4-8-20260101", "claude-opus-4.8"},

		// 4. Unknown version not in catalog -> family resolution picks highest available.
		{"future opus 4.9 via family", "claude-opus-4-9", "claude-opus-4.8"},
		{"unknown sonnet via family", "claude-sonnet-9-9", "claude-sonnet-4.6"},

		// 4b. Unknown Claude family Kiro has no equivalent for (e.g. Fable/Mythos)
		// -> best available Claude model, never a raw pass-through (Kiro rejects
		// unknown ids with 400 INVALID_MODEL_ID, which looped Claude Code forever).
		{"fable via best-claude fallback", "claude-fable-5", "claude-opus-4.8"},
		{"mythos via best-claude fallback", "claude-mythos-5", "claude-opus-4.8"},

		// 3/4. Raw kiro-style id passed straight through via catalog.
		{"raw glm via catalog", "GLM-5", "glm-5"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := getKiroModelID(c.in); got != c.want {
				t.Errorf("getKiroModelID(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestGetKiroModelIDStaticFallback verifies resolution still works (offline-safe)
// when the catalog is empty, via the static family fallback.
func TestGetKiroModelIDStaticFallback(t *testing.T) {
	orig := modelCatalog
	modelCatalog = models.NewCatalog(time.Minute, func() ([]models.KiroModel, error) {
		return nil, errStub
	})
	t.Cleanup(func() { modelCatalog = orig })

	cases := map[string]string{
		"claude-opus-4-9":   "claude-opus-4.6", // unknown version, no catalog -> static fallback
		"claude-sonnet-9-9": "claude-sonnet-4.6",
		"some-glm-thing":    "glm-5",
		"claude-fable-5":    "claude-opus-4.6", // unknown Claude family, no catalog -> newest known stable
	}
	for in, want := range cases {
		if got := getKiroModelID(in); got != want {
			t.Errorf("getKiroModelID(%q) = %q, want %q (static fallback)", in, got, want)
		}
	}
}

// TestGetKiroModelIDUnknownClaudeFamilyNoOpus verifies that an unknown Claude
// family falls through the live families in order (opus -> sonnet -> haiku)
// when the account's catalog doesn't expose opus at all.
func TestGetKiroModelIDUnknownClaudeFamilyNoOpus(t *testing.T) {
	withStubCatalog(t, []models.KiroModel{
		mk("claude-sonnet-4.6"),
		mk("claude-haiku-4.5"),
		mk("glm-5"),
	})

	if got := getKiroModelID("claude-fable-5"); got != "claude-sonnet-4.6" {
		t.Errorf("getKiroModelID(claude-fable-5) = %q, want claude-sonnet-4.6 (opus-less catalog)", got)
	}
}

func TestSanitizeToolName(t *testing.T) {
	// Short, valid names are unchanged.
	for _, n := range []string{"Bash", "read_file", "mcp__slack__send", "a-b_c"} {
		if got := sanitizeToolName(n); got != n {
			t.Errorf("sanitizeToolName(%q) = %q, want unchanged", n, got)
		}
	}

	// Long names are shortened to <= 64 chars, deterministically.
	long := "mcp__plugin_aws-serverless_aws-serverless-mcp__deploy_serverless_app_help" // 73 chars
	got := sanitizeToolName(long)
	if len(got) > maxToolNameLen {
		t.Errorf("sanitizeToolName(long) len = %d, want <= %d", len(got), maxToolNameLen)
	}
	if got != sanitizeToolName(long) {
		t.Error("sanitizeToolName must be deterministic")
	}
	if invalidToolNameChars.MatchString(got) {
		t.Errorf("sanitized name %q contains invalid chars", got)
	}

	// Different long names must not collide (hash suffix).
	a := sanitizeToolName("mcp__plugin_aws-serverless_aws-serverless-mcp__deploy_serverless_app_help")
	b := sanitizeToolName("mcp__plugin_aws-serverless_aws-serverless-mcp__deploy_serverless_app_other")
	if a == b {
		t.Error("distinct long names collided after sanitization")
	}

	// Invalid characters are replaced.
	if got := sanitizeToolName("weird name!"); invalidToolNameChars.MatchString(got) {
		t.Errorf("sanitizeToolName left invalid chars: %q", got)
	}
}

func TestBuildAndRestoreToolNames(t *testing.T) {
	long := "mcp__plugin_aws-serverless_aws-serverless-mcp__deploy_serverless_app_help"
	tools := []AnthropicTool{{Name: long}, {Name: "Bash"}}
	m := buildToolNameMap(tools)

	short := sanitizeToolName(long)
	if m[short] != long {
		t.Errorf("buildToolNameMap missing %q -> %q (got %v)", short, long, m)
	}
	if _, ok := m["Bash"]; ok {
		t.Error("unchanged name should not be in the map")
	}

	// A content_block_start tool_use event with the sanitized name is restored.
	events := []parser.SSEEvent{
		{Event: "content_block_start", Data: map[string]any{
			"type":  "content_block_start",
			"index": 1,
			"content_block": map[string]any{
				"type": "tool_use", "id": "tu_1", "name": short, "input": map[string]any{},
			},
		}},
	}
	restoreToolNames(events, m)
	cb := events[0].Data.(map[string]any)["content_block"].(map[string]any)
	if cb["name"] != long {
		t.Errorf("restoreToolNames did not restore name: got %v, want %q", cb["name"], long)
	}
}

func TestArnAccount(t *testing.T) {
	cases := map[string]string{
		"arn:aws:codewhisperer:us-east-1:908475551805:profile/P9VYA4W3X47Y": "908475551805",
		"arn:aws:sso::908475551805:application/ssoins-x/apl-y":              "908475551805",
		"not-an-arn": "",
	}
	for in, want := range cases {
		if got := arnAccount(in); got != want {
			t.Errorf("arnAccount(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSelectProfileArn(t *testing.T) {
	mkProfile := func(arn, oidc string) kiroProfile {
		var p kiroProfile
		p.Arn = arn
		p.IdentityDetails.SsoIdentityDetails.OidcClientId = oidc
		return p
	}
	// The real-world case: two profiles sharing one SSO instance (account
	// 908475551805). The enterprise profile lives in that same account; the other
	// is in a different account and must NOT be chosen (it lacks model access).
	qdevProfile := mkProfile(
		"arn:aws:codewhisperer:us-east-1:908475551805:profile/P9VYA4W3X47Y",
		"arn:aws:sso::908475551805:application/ssoins-72234c8a5f64721d/apl-dc6258b703a4c470")
	qdevGeraldes := mkProfile(
		"arn:aws:codewhisperer:us-east-1:202533512721:profile/XPYK9HCQKNRN",
		"arn:aws:sso::908475551805:application/ssoins-72234c8a5f64721d/apl-418be578436a8c29")

	if got := selectProfileArn([]kiroProfile{qdevGeraldes, qdevProfile}); got != qdevProfile.Arn {
		t.Errorf("selectProfileArn picked %q, want enterprise profile %q", got, qdevProfile.Arn)
	}

	// Single profile -> always chosen.
	if got := selectProfileArn([]kiroProfile{qdevGeraldes}); got != qdevGeraldes.Arn {
		t.Errorf("single profile: got %q, want %q", got, qdevGeraldes.Arn)
	}

	// Empty -> "".
	if got := selectProfileArn(nil); got != "" {
		t.Errorf("no profiles: got %q, want empty", got)
	}

	// Ambiguous (no account match) -> "" (never guess).
	a := mkProfile("arn:aws:codewhisperer:us-east-1:111111111111:profile/A", "arn:aws:sso::999999999999:application/x")
	b := mkProfile("arn:aws:codewhisperer:us-east-1:222222222222:profile/B", "arn:aws:sso::999999999999:application/y")
	if got := selectProfileArn([]kiroProfile{a, b}); got != "" {
		t.Errorf("ambiguous: got %q, want empty (no guess)", got)
	}
}

var errStub = stubErr("stub fetch failure")

type stubErr string

func (e stubErr) Error() string { return string(e) }

func TestIsInvalidBearerToken(t *testing.T) {
	invalid := []string{
		`{"message":"The bearer token included in the request is invalid.","reason":null}`,
		`{"message":"Token expired"}`,
		`{"error":"invalid_token"}`,
	}
	for _, b := range invalid {
		if !isInvalidBearerToken([]byte(b)) {
			t.Errorf("isInvalidBearerToken(%s) = false, want true", b)
		}
	}
	// 403s that are NOT auth failures (e.g. model not available for the
	// account) must not trigger the token-refresh path, or the user gets an
	// endless "Token refreshed, please retry" loop.
	denied := []string{
		`{"message":"User is not authorized to access this resource","__type":"AccessDeniedException"}`,
		`{"message":"You do not have access to the requested model"}`,
		``,
	}
	for _, b := range denied {
		if isInvalidBearerToken([]byte(b)) {
			t.Errorf("isInvalidBearerToken(%s) = true, want false", b)
		}
	}
}

// TestResolveModelInfo encodes the per-account availability scenario: Kiro
// exposes different model lists per account/plan, so a model in the static map
// but absent from this account's live catalog must be flagged, and family
// resolution must cap at the account's highest available version.
func TestResolveModelInfo(t *testing.T) {
	// Account that has Opus 4.8 (previews enabled).
	withStubCatalog(t, stubList())
	info := resolveModelInfo("claude-opus-4-8")
	if info.KiroModel != "claude-opus-4.8" || !info.InCatalog {
		t.Errorf("full catalog: got %+v, want kiro_model=claude-opus-4.8 in catalog", info)
	}

	// Account whose catalog tops out at Opus 4.6 and has no GLM.
	withStubCatalog(t, []models.KiroModel{
		mk("auto"), mk("claude-opus-4.6"), mk("claude-sonnet-4.6"), mk("claude-haiku-4.5"),
	})
	info = resolveModelInfo("claude-opus-4-8")
	if info.KiroModel != "claude-opus-4.8" || info.InCatalog || info.Note == "" {
		t.Errorf("limited catalog: got %+v, want static-mapped claude-opus-4.8 flagged as not in catalog", info)
	}
	info = resolveModelInfo("glm-5")
	if info.KiroModel != "glm-5" || info.InCatalog {
		t.Errorf("limited catalog: got %+v, want glm-5 flagged as not in catalog", info)
	}
	// A generic/unknown opus id resolves within the account's own catalog,
	// which is why such an account always lands on Opus 4.6.
	if got := getKiroModelID("claude-opus-4-9"); got != "claude-opus-4.6" {
		t.Errorf("family resolution on limited catalog = %q, want claude-opus-4.6", got)
	}
}

func TestResolveEndpoint(t *testing.T) {
	withStubCatalog(t, stubList())
	mux := buildServerMux(logger.NewLogger(10))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/resolve?model=glm-5", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /resolve status = %d, want 200", rec.Code)
	}
	var info modelResolveInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("response is not JSON: %v (body: %s)", err, rec.Body.String())
	}
	if info.KiroModel != "glm-5" || !info.InCatalog {
		t.Errorf("got %+v, want kiro_model=glm-5 in catalog", info)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/resolve", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET /resolve without model status = %d, want 400", rec.Code)
	}
}
