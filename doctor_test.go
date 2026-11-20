package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/diagnostics"
)

const (
	doctorSecretAccess = "aoaDOCTORsecretACCESS"
	doctorSecretArn    = "arn:aws:codewhisperer:us-east-1:210987654321:profile/DOCTORARN"
)

// doctorHome makes a temporary home with a token, and returns it.
func doctorHome(t *testing.T, expiresAt time.Time) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	writeDoctorFile(t, filepath.Join(home, ".aws", "sso", "cache", "kiro-auth-token.json"),
		`{"accessToken":"`+doctorSecretAccess+`","refreshToken":"r","expiresAt":"`+expiresAt.Format(time.RFC3339)+
			`","authMethod":"IdC","provider":"Enterprise","region":"us-east-1","profileArn":"`+doctorSecretArn+`"}`)
	return home
}

func writeDoctorFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// snapshot lists every path under root, with size and modification time for files.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// A directory's time moves when NTFS settles the writes the test
			// itself just made; a created or removed entry still shows up as
			// a changed set of paths.
			out[path] = "dir"
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[path] = fmt.Sprintf("%s/%d", info.ModTime(), info.Size())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func checkByID(t *testing.T, r diagnostics.Report, id string) diagnostics.Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %q in %+v", id, r.Checks)
	return diagnostics.Check{}
}

func testDoctorOptions(now time.Time) (doctorOptions, *int) {
	calls := 0
	return doctorOptions{
		Now:          now,
		LocalAppData: "",
		HealthClient: &http.Client{Timeout: 2 * time.Second},
		FindClaude:   func() (string, error) { return "/bin/claude", nil },
		FetchModelIDs: func(cfg *config.Config, accessToken, profileArn string) ([]string, error) {
			calls++
			if accessToken != doctorSecretAccess || profileArn != doctorSecretArn {
				return nil, errors.New("wrong token passed")
			}
			return []string{"auto", "claude-opus-4.8"}, nil
		},
	}, &calls
}

func TestDoctorReportHealthyAndReadOnly(t *testing.T) {
	now := time.Now()
	home := doctorHome(t, now.Add(2*time.Hour))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("doctor called %s; only /health is allowed", r.URL.Path)
		}
		w.Header().Set(profileHeader, "default")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	writeDoctorFile(t, filepath.Join(home, ".claude2kiro", "proxy.port"), srv.URL[strings.LastIndex(srv.URL, ":")+1:])

	before := snapshot(t, home)
	opts, calls := testDoctorOptions(now)
	r := collectDoctorReport(opts)
	if after := snapshot(t, home); len(after) != len(before) {
		t.Fatalf("doctor changed the home directory: before %d entries, after %d", len(before), len(after))
	} else {
		for k, v := range before {
			if after[k] != v {
				t.Fatalf("doctor modified %s: %s -> %s", k, v, after[k])
			}
		}
	}

	if *calls != 1 {
		t.Fatalf("catalogue fetched %d times, want 1", *calls)
	}
	for _, id := range []string{"config", "kiro_token", "model_catalog", "proxy", "claude_code", "request_settings"} {
		if c := checkByID(t, r, id); c.Severity != diagnostics.SeverityOK {
			t.Fatalf("%s: %+v", id, c)
		}
	}
	if c := checkByID(t, r, "proxy"); !strings.Contains(c.Detail, "running at") {
		t.Fatalf("proxy: %+v", c)
	}
	if r.Worst() == diagnostics.SeverityError {
		t.Fatalf("healthy report has an error: %+v", r.Checks)
	}

	var js bytes.Buffer
	if err := diagnostics.RenderJSON(&js, r); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{doctorSecretAccess, doctorSecretArn, "210987654321"} {
		if strings.Contains(js.String(), secret) {
			t.Fatalf("report leaks %q", secret)
		}
	}
}

func TestDoctorOfflineAndExpiredTokenSkipCatalogue(t *testing.T) {
	now := time.Now()
	doctorHome(t, now.Add(-time.Hour))
	opts, calls := testDoctorOptions(now)
	r := collectDoctorReport(opts)
	if *calls != 0 {
		t.Fatal("an expired token must not be sent to the catalogue")
	}
	if c := checkByID(t, r, "model_catalog"); !strings.Contains(c.Detail, "no usable access token") {
		t.Fatalf("catalogue: %+v", c)
	}
	if c := checkByID(t, r, "kiro_token"); c.Severity != diagnostics.SeverityWarn {
		t.Fatalf("expired token with refresh token should warn: %+v", c)
	}

	doctorHome(t, now.Add(time.Hour))
	opts, calls = testDoctorOptions(now)
	opts.Offline = true
	r = collectDoctorReport(opts)
	if *calls != 0 {
		t.Fatal("--offline must not call the catalogue")
	}
	if c := checkByID(t, r, "model_catalog"); !strings.Contains(c.Detail, "--offline") {
		t.Fatalf("catalogue: %+v", c)
	}
	if c := checkByID(t, r, "proxy"); c.Severity != diagnostics.SeverityOK || !strings.Contains(c.Detail, "not running") {
		t.Fatalf("no port file: %+v", c)
	}
}

func TestDoctorReadsStagedRenewedTokenWithoutMovingIt(t *testing.T) {
	now := time.Now()
	home := doctorHome(t, now.Add(-time.Hour))
	tokenPath := filepath.Join(home, ".aws", "sso", "cache", "kiro-auth-token.json")
	renewedPath := tokenPath + ".renewed"
	writeDoctorFile(t, renewedPath, `{"accessToken":"`+doctorSecretAccess+`","refreshToken":"r","expiresAt":"`+
		now.Add(time.Hour).Format(time.RFC3339)+`","profileArn":"`+doctorSecretArn+`"}`)
	before := snapshot(t, home)
	opts, calls := testDoctorOptions(now)
	r := collectDoctorReport(opts)
	c := checkByID(t, r, "kiro_token")
	if c.Severity != diagnostics.SeverityOK || !strings.Contains(c.Detail, "renewed token waiting") {
		t.Fatalf("staged renewed token not reported: %+v", c)
	}
	if *calls != 1 {
		t.Fatal("the renewed token is usable, so the catalogue should be checked with it")
	}
	after := snapshot(t, home)
	if after[renewedPath] != before[renewedPath] || after[tokenPath] != before[tokenPath] || len(after) != len(before) {
		t.Fatal("doctor moved or changed the renewed token")
	}
}

func TestDoctorMissingTokenIsAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	opts, _ := testDoctorOptions(time.Now())
	r := collectDoctorReport(opts)
	if c := checkByID(t, r, "kiro_token"); c.Severity != diagnostics.SeverityError {
		t.Fatalf("missing token: %+v", c)
	}
	if r.Worst() != diagnostics.SeverityError {
		t.Fatal("missing token must make doctor fail")
	}
}

func TestDoctorFallbackProfilesAndCatalogueErrorRedacted(t *testing.T) {
	now := time.Now()
	home := doctorHome(t, now.Add(time.Hour))
	writeDoctorFile(t, filepath.Join(home, ".claude2kiro", "config.yaml"), "auth:\n  fallback_profiles: [reserve]\n")
	opts, _ := testDoctorOptions(now)
	opts.FetchModelIDs = func(cfg *config.Config, accessToken, profileArn string) ([]string, error) {
		return nil, errors.New(`Get "https://q.example/ListAvailableModels?profileArn=` + profileArn + `": timeout`)
	}
	r := collectDoctorReport(opts)
	fb := checkByID(t, r, "kiro_token_reserve")
	if fb.Severity != diagnostics.SeverityWarn || !strings.Contains(fb.Detail, "no token file") {
		t.Fatalf("missing fallback token should warn: %+v", fb)
	}
	cat := checkByID(t, r, "model_catalog")
	if cat.Severity != diagnostics.SeverityError || strings.Contains(cat.Detail, doctorSecretArn) || !strings.Contains(cat.Detail, "<redacted>") {
		t.Fatalf("catalogue error must be reported with the ARN redacted: %+v", cat)
	}
}

func TestDoctorPersistentRoutingWithoutProxyWarns(t *testing.T) {
	now := time.Now()
	home := doctorHome(t, now.Add(time.Hour))
	writeDoctorFile(t, filepath.Join(home, ".claude", "settings.json"), `{"env":{"ANTHROPIC_BASE_URL":"http://localhost:8080"}}`)
	opts, _ := testDoctorOptions(now)
	opts.Offline = true
	r := collectDoctorReport(opts)
	c := checkByID(t, r, "claude_code")
	if c.Severity != diagnostics.SeverityWarn || !strings.Contains(c.Detail, "claude2kiro disable") {
		t.Fatalf("persistent routing with no proxy should warn: %+v", c)
	}
}

func TestDoctorStalePortFileWarns(t *testing.T) {
	now := time.Now()
	home := doctorHome(t, now.Add(time.Hour))
	srv := httptest.NewServer(http.NotFoundHandler())
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	srv.Close() // nothing listens on the port any more
	writeDoctorFile(t, filepath.Join(home, ".claude2kiro", "proxy.port"), port)
	opts, _ := testDoctorOptions(now)
	opts.Offline = true
	r := collectDoctorReport(opts)
	if c := checkByID(t, r, "proxy"); c.Severity != diagnostics.SeverityWarn || !strings.Contains(c.Detail, "stale port file") {
		t.Fatalf("stale port: %+v", c)
	}
}
