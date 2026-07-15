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

		// Unknown ids are NOT substituted for a different model — resolution
		// yields a candidate (normalized Kiro-form, else raw) and the servability
		// gate (modelServable) decides whether to serve or catch. An unknown
		// version keeps its own version; an unknown Claude family keeps its name.
		{"future opus 4.9 keeps its version", "claude-opus-4-9", "claude-opus-4.9"},
		{"unknown sonnet keeps its version", "claude-sonnet-9-9", "claude-sonnet-9.9"},
		{"fable is not substituted", "claude-fable-5", "claude-fable-5"},
		{"mythos is not substituted", "claude-mythos-5", "claude-mythos-5"},

		// 3. Raw kiro-style id passed straight through via catalog.
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

// TestGetKiroModelIDStaticFallback verifies that the curated static map still
// resolves the common current ids offline (catalog unreachable), and that
// unknown ids pass through as a best-effort candidate rather than being
// substituted for a different model.
func TestGetKiroModelIDStaticFallback(t *testing.T) {
	orig := modelCatalog
	modelCatalog = models.NewCatalog(time.Minute, func() ([]models.KiroModel, error) {
		return nil, errStub
	})
	t.Cleanup(func() { modelCatalog = orig })

	cases := map[string]string{
		// Static map entries still resolve with no catalog.
		"claude-opus-4-8":            "claude-opus-4.8",
		"glm-5":                      "glm-5",
		"claude-3-7-sonnet-20250219": "claude-sonnet-4.5", // curated legacy remap
		// Unknown ids: normalized Kiro-form (if it's a versioned Claude id) or
		// raw, never a cross-family substitute. The servability gate + backend
		// 400 backstop handle these; the proxy does not invent a replacement.
		"claude-opus-4-9":   "claude-opus-4.9",
		"claude-sonnet-9-9": "claude-sonnet-9.9",
		"some-glm-thing":    "some-glm-thing",
		"claude-fable-5":    "claude-fable-5",
	}
	for in, want := range cases {
		if got := getKiroModelID(in); got != want {
			t.Errorf("getKiroModelID(%q) = %q, want %q (offline)", in, got, want)
		}
	}
}

// TestModelServable verifies the servability gate that decides whether a
// resolved model is sent or caught pre-flight.
func TestModelServable(t *testing.T) {
	t.Run("warm catalog gates on membership", func(t *testing.T) {
		withStubCatalog(t, stubList())
		if !modelServable("claude-opus-4.8") {
			t.Error("claude-opus-4.8 is in the catalog; want servable")
		}
		if modelServable("claude-fable-5") {
			t.Error("claude-fable-5 is not in the catalog; want NOT servable")
		}
	})
	t.Run("auto is always servable", func(t *testing.T) {
		// A catalog that does not list "auto" as a model.
		withStubCatalog(t, []models.KiroModel{mk("glm-5"), mk("deepseek-3.2")})
		if !modelServable("auto") {
			t.Error("auto must always be servable (Kiro picks per task)")
		}
	})
	t.Run("cold catalog allows through (best effort)", func(t *testing.T) {
		orig := modelCatalog
		modelCatalog = models.NewCatalog(time.Minute, func() ([]models.KiroModel, error) {
			return nil, errStub
		})
		t.Cleanup(func() { modelCatalog = orig })
		if !modelServable("claude-fable-5") {
			t.Error("with no catalog to verify against, allow through and let the backend 400 be the backstop")
		}
	})
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
	// An unknown opus version is NOT substituted for the account's newest opus;
	// it keeps its own version as the candidate and is flagged not-in-catalog so
	// the caller surfaces it instead of silently serving a different model.
	info = resolveModelInfo("claude-opus-4-9")
	if info.KiroModel != "claude-opus-4.9" || info.InCatalog {
		t.Errorf("limited catalog: got %+v, want candidate claude-opus-4.9 flagged not in catalog", info)
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
