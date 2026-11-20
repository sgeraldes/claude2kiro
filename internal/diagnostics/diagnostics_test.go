package diagnostics

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
)

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

const secretAccess = "aoaSECRETaccessVALUE"
const secretRefresh = "aorSECRETrefreshVALUE"
const secretArn = "arn:aws:codewhisperer:us-east-1:123456789012:profile/SECRETARN"

func tokenJSON(expiresAt, refresh string) []byte {
	return []byte(`{"accessToken":"` + secretAccess + `","refreshToken":"` + refresh + `","expiresAt":"` + expiresAt +
		`","authMethod":"IdC","provider":"Enterprise","region":"us-east-1","profileArn":"` + secretArn + `"}`)
}

func assertNoSecret(t *testing.T, s string) {
	t.Helper()
	for _, secret := range []string{secretAccess, secretRefresh, secretArn, "123456789012"} {
		if strings.Contains(s, secret) {
			t.Fatalf("output leaks a secret %q: %s", secret, s)
		}
	}
}

func TestTokenCheck(t *testing.T) {
	cases := []struct {
		name       string
		data       []byte
		readErr    error
		missing    bool
		fallback   bool
		want       Severity
		usable     bool
		wantDetail string
	}{
		{"valid", tokenJSON(now.Add(90*time.Minute).Format(time.RFC3339), secretRefresh), nil, false, false, SeverityOK, true, "valid for 1h30m"},
		{"expired with refresh token", tokenJSON(now.Add(-2*time.Hour).Format(time.RFC3339), secretRefresh), nil, false, false, SeverityWarn, false, "renews it on the next request"},
		{"fallback expired with refresh token is normal", tokenJSON(now.Add(-2*time.Hour).Format(time.RFC3339), secretRefresh), nil, false, true, SeverityOK, false, "failover renews it"},
		{"expired without refresh token", tokenJSON(now.Add(-time.Minute).Format(time.RFC3339), ""), nil, false, false, SeverityError, false, "no refresh token"},
		{"expired fallback profile without refresh", tokenJSON(now.Add(-time.Minute).Format(time.RFC3339), ""), nil, false, true, SeverityWarn, false, "no refresh token"},
		{"missing", nil, errors.New("open: no such file"), true, false, SeverityError, false, "claude2kiro login"},
		{"missing fallback", nil, errors.New("open: no such file"), true, true, SeverityWarn, false, "claude2kiro login"},
		{"unreadable", nil, errors.New("access denied"), false, false, SeverityError, false, "access denied"},
		{"invalid json", []byte(`{"accessToken":"` + secretAccess), nil, false, false, SeverityError, false, "not valid JSON"},
		{"no access token", []byte(`{"refreshToken":"x"}`), nil, false, false, SeverityError, false, "no access token"},
		{"unreadable expiry", tokenJSON("tomorrow", secretRefresh), nil, false, false, SeverityWarn, true, "expiry unreadable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, facts := TokenCheck("kiro_token", "Kiro token", tc.data, tc.readErr, tc.missing, now, tc.fallback)
			if c.Severity != tc.want {
				t.Fatalf("severity = %s, want %s (%s)", c.Severity, tc.want, c.Detail)
			}
			if facts.Usable != tc.usable {
				t.Fatalf("usable = %v, want %v", facts.Usable, tc.usable)
			}
			if !strings.Contains(c.Detail, tc.wantDetail) {
				t.Fatalf("detail %q does not contain %q", c.Detail, tc.wantDetail)
			}
			assertNoSecret(t, c.Detail)
		})
	}
}

func TestTokenCheckReportsPresenceOnly(t *testing.T) {
	c, facts := TokenCheck("kiro_token", "Kiro token", tokenJSON(now.Add(time.Hour).Format(time.RFC3339), secretRefresh), nil, false, now, false)
	for _, want := range []string{"auth IdC", "provider Enterprise", "region us-east-1", "profile ARN present", "refresh token present"} {
		if !strings.Contains(c.Detail, want) {
			t.Fatalf("detail %q lacks %q", c.Detail, want)
		}
	}
	if facts.AccessToken != secretAccess || facts.ProfileArn != secretArn {
		t.Fatal("facts must carry the token for the catalogue call")
	}
	r := Report{Version: "1", Profile: "default", Checks: []Check{c}}
	var text, js bytes.Buffer
	if err := RenderText(&text, r); err != nil {
		t.Fatal(err)
	}
	if err := RenderJSON(&js, r); err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, text.String())
	assertNoSecret(t, js.String())
}

func TestProxyCheck(t *testing.T) {
	cases := []struct {
		name    string
		probe   ProxyProbe
		want    Severity
		running bool
		detail  string
	}{
		{"no port file", ProxyProbe{PortFile: "proxy.port", PortFileMissing: true}, SeverityOK, false, "not running"},
		{"empty port file", ProxyProbe{PortFile: "proxy.port"}, SeverityWarn, false, "is empty"},
		{"stale", ProxyProbe{PortFile: "proxy.port", Port: "8080", ProbeErr: errors.New("connection refused")}, SeverityWarn, false, "stale port file"},
		{"http 500", ProxyProbe{PortFile: "proxy.port", Port: "8080", Status: 500}, SeverityWarn, false, "HTTP 500"},
		{"default, header-less old binary", ProxyProbe{PortFile: "proxy.port", Port: "8080", Status: 200, ExpectedProfile: "default", DefaultProfile: true}, SeverityOK, true, "running at http://127.0.0.1:8080"},
		{"named profile, header-less", ProxyProbe{PortFile: "proxy.work.port", Port: "8081", Status: 200, ExpectedProfile: "work"}, SeverityWarn, false, "does not declare a profile"},
		{"other profile", ProxyProbe{PortFile: "proxy.port", Port: "8080", Status: 200, ServedProfile: "work", ExpectedProfile: "default", DefaultProfile: true}, SeverityWarn, false, "proxy of profile work"},
		{"same profile", ProxyProbe{PortFile: "proxy.work.port", Port: "8081", Status: 200, ServedProfile: "work", ExpectedProfile: "work"}, SeverityOK, true, "running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, running := ProxyCheck(tc.probe)
			if c.Severity != tc.want || running != tc.running {
				t.Fatalf("got %s running=%v, want %s running=%v (%s)", c.Severity, running, tc.want, tc.running, c.Detail)
			}
			if !strings.Contains(c.Detail, tc.detail) {
				t.Fatalf("detail %q lacks %q", c.Detail, tc.detail)
			}
		})
	}
}

func TestCatalogCheck(t *testing.T) {
	if c := CatalogCheck("no usable access token", nil, nil); c.Severity != SeverityWarn || !strings.Contains(c.Detail, "not checked: no usable access token") {
		t.Fatalf("skipped: %+v", c)
	}
	if c := CatalogCheck(CatalogOffline, nil, nil); c.Severity != SeverityOK {
		t.Fatalf("a skip the user asked for is not a warning: %+v", c)
	}
	if c := CatalogCheck("", nil, errors.New("ListAvailableModels status 403")); c.Severity != SeverityError {
		t.Fatalf("error: %+v", c)
	}
	if c := CatalogCheck("", []string{"auto", "claude-opus-4.8"}, nil); c.Severity != SeverityOK || c.Detail != "2 models available, including auto" {
		t.Fatalf("ok: %+v", c)
	}
	if c := CatalogCheck("", []string{"claude-opus-4.8"}, nil); c.Severity != SeverityWarn || !strings.Contains(c.Detail, "auto is not among them") {
		t.Fatalf("no auto: %+v", c)
	}
}

func TestRequestSettingsCheck(t *testing.T) {
	def := config.Default().Advanced
	c := RequestSettingsCheck(def)
	if c.Severity != SeverityOK || c.Detail != "history full, tools full, thinking client" {
		t.Fatalf("defaults: %+v", c)
	}

	changed := def
	changed.HistoryMode = "recent"
	changed.HistoryRecentTurns = 3
	changed.ToolMode = "compact"
	changed.StableConversationID = false
	changed.ThinkingMode = config.ThinkingModeBackend
	c = RequestSettingsCheck(changed)
	if c.Severity != SeverityWarn {
		t.Fatalf("changed settings must warn: %+v", c)
	}
	for _, want := range []string{"history recent (3 turns)", "tools compact", "thinking backend", "history_mode", "tool_mode", "stable_conversation_id", "thinking_mode"} {
		if !strings.Contains(c.Detail, want) {
			t.Fatalf("detail %q lacks %q", c.Detail, want)
		}
	}

	typo := def
	typo.HistoryMode = "all"
	typo.ToolMode = "compact"
	c = RequestSettingsCheck(typo)
	if c.Severity != SeverityWarn || !strings.Contains(c.Detail, "history full,") || !strings.Contains(c.Detail, `history_mode "all" is not a known value and works as full`) {
		t.Fatalf("an unknown history mode must be reported as what the proxy does: %+v", c)
	}
	if strings.Contains(c.Detail, "changed from the defaults: history_mode") {
		t.Fatalf("an unknown value works as the default; it is not a change: %+v", c)
	}

	direct := def
	direct.AnthropicDirect = true
	direct.AnthropicApiKey = "sk-ant-SECRET"
	c = RequestSettingsCheck(direct)
	if c.Severity != SeverityWarn || strings.Contains(c.Detail, "sk-ant-SECRET") {
		t.Fatalf("direct mode: %+v", c)
	}
}

func TestConfigCheck(t *testing.T) {
	if c := ConfigCheck("/h/.claude2kiro/config.yaml", false, nil, nil); c.Severity != SeverityOK || !strings.Contains(c.Detail, "defaults in use") {
		t.Fatalf("missing: %+v", c)
	}
	if c := ConfigCheck("/h/.claude2kiro/config.yaml", true, nil, errors.New("yaml: line 3")); c.Severity != SeverityError {
		t.Fatalf("parse error: %+v", c)
	}
	if c := ConfigCheck("/h/.claude2kiro/config.yaml", true, nil, nil); c.Severity != SeverityOK || c.Detail != "/h/.claude2kiro/config.yaml" {
		t.Fatalf("ok: %+v", c)
	}
}

func TestWorst(t *testing.T) {
	r := Report{Checks: []Check{{Severity: SeverityOK}, {Severity: SeverityWarn}}}
	if r.Worst() != SeverityWarn {
		t.Fatal("want warn")
	}
	r.Checks = append(r.Checks, Check{Severity: SeverityError}, Check{Severity: SeverityWarn})
	if r.Worst() != SeverityError {
		t.Fatal("want error")
	}
	if (Report{}).Worst() != SeverityOK {
		t.Fatal("empty report is ok")
	}
}
