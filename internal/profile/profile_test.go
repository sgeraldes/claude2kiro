package profile

import (
	"os"
	"path/filepath"
	"testing"
)

// Name() caches the environment once per process, so the naming tests exercise
// suffixed()/paths through Validate-driven fixtures rather than the cache.
func withName(t *testing.T, n string, fn func()) {
	t.Helper()
	old := name
	name = n
	defer func() { name = old }()
	fn()
}

func TestValidateAcceptsSimpleNamesAndEmpty(t *testing.T) {
	for _, raw := range []string{"", "  ", "agentes", "kiro-2", "Seb_Ops", "a"} {
		if _, err := Validate(raw); err != nil {
			t.Errorf("%q: unexpected error %v", raw, err)
		}
	}
	if n, _ := Validate("  agentes "); n != "agentes" {
		t.Fatalf("trim: %q", n)
	}
}

func TestValidateRejectsAnythingThatCouldResolveToAnotherIdentity(t *testing.T) {
	for _, raw := range []string{"../", "a/b", "foo.bar", "evil name", "x\\y", ".", "..", "tab\tx", "ñandu", string(make([]byte, 65))} {
		if _, err := Validate(raw); err == nil {
			t.Errorf("%q: expected an error", raw)
		}
	}
}

func TestDefaultProfileKeepsHistoricalNames(t *testing.T) {
	withName(t, "", func() {
		if got := TokenFileName(); got != "kiro-auth-token.json" {
			t.Fatalf("token: %q", got)
		}
		if got := LoginConfigFileName(); got != "claude2kiro-login-config.json" {
			t.Fatalf("login config: %q", got)
		}
		if got := ProxyPortFileName(); got != "proxy.port" {
			t.Fatalf("proxy port: %q", got)
		}
		if got := CreditHistoryFileName(); got != "credit-history.jsonl" {
			t.Fatalf("credit history: %q", got)
		}
		if got := Label(); got != "default" {
			t.Fatalf("label: %q", got)
		}
		home := t.TempDir()
		shared := filepath.Join(home, ".claude2kiro", "config.yaml")
		if got := ConfigReadPath(home); got != shared {
			t.Fatalf("read: %q", got)
		}
		if got := ConfigSavePath(home); got != shared {
			t.Fatalf("save: %q", got)
		}
	})
}

func TestNamedProfileSuffixesEveryPerIdentityFile(t *testing.T) {
	withName(t, "agentes", func() {
		cases := map[string]string{
			TokenFileName():         "kiro-auth-token.agentes.json",
			LoginConfigFileName():   "claude2kiro-login-config.agentes.json",
			ProxyPortFileName():     "proxy.agentes.port",
			CreditHistoryFileName(): "credit-history.agentes.jsonl",
			Label():                 "agentes",
		}
		for got, want := range cases {
			if got != want {
				t.Errorf("got %q want %q", got, want)
			}
		}
	})
}

func TestNamedProfileReadsSharedUntilItSavesItsOwn(t *testing.T) {
	withName(t, "agentes", func() {
		home := t.TempDir()
		dir := filepath.Join(home, ".claude2kiro")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		own := filepath.Join(dir, "config.agentes.yaml")
		shared := filepath.Join(dir, "config.yaml")
		if got := ConfigReadPath(home); got != shared {
			t.Fatalf("read without own file: %q", got)
		}
		// saving never targets the shared file under a named profile
		if got := ConfigSavePath(home); got != own {
			t.Fatalf("save: %q", got)
		}
		if err := os.WriteFile(own, []byte("server:\n  port: \"8081\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := ConfigReadPath(home); got != own {
			t.Fatalf("read with own file: %q", got)
		}
	})
}

func TestNameFromEnvIsValidated(t *testing.T) {
	// Name() is process-cached; exercise the same validation the cache runs.
	t.Setenv(EnvVar, "ops-1")
	if n, err := Validate(os.Getenv(EnvVar)); err != nil || n != "ops-1" {
		t.Fatalf("env name: %q %v", n, err)
	}
}

// Identity switching: when the launched profile's credit pool is exhausted the
// proxy moves its token, login config and credit history to a fallback
// identity, while the port marker and config stay with the launched profile so
// `run` still attaches to the right proxy.

func TestSwitchToMovesOnlyTheIdentityFiles(t *testing.T) {
	withName(t, "", func() {
		t.Cleanup(ResetIdentity)
		if err := SwitchTo("kiro2"); err != nil {
			t.Fatal(err)
		}
		if got := TokenFileName(); got != "kiro-auth-token.kiro2.json" {
			t.Fatalf("token: %q", got)
		}
		if got := LoginConfigFileName(); got != "claude2kiro-login-config.kiro2.json" {
			t.Fatalf("login config: %q", got)
		}
		if got := CreditHistoryFileName(); got != "credit-history.kiro2.jsonl" {
			t.Fatalf("credit history: %q", got)
		}
		if got := ProxyPortFileName(); got != "proxy.port" {
			t.Fatalf("port marker must follow the launched profile: %q", got)
		}
		home := t.TempDir()
		if got := ConfigSavePath(home); got != filepath.Join(home, ".claude2kiro", "config.yaml") {
			t.Fatalf("config must follow the launched profile: %q", got)
		}
		if got := Label(); got != "default" {
			t.Fatalf("label must stay the launched profile: %q", got)
		}
		if got := Active(); got != "kiro2" {
			t.Fatalf("active: %q", got)
		}
		if got := ActiveLabel(); got != "kiro2" {
			t.Fatalf("active label: %q", got)
		}
	})
}

func TestSwitchToTheLaunchedProfileIsANoOp(t *testing.T) {
	withName(t, "agentes", func() {
		t.Cleanup(ResetIdentity)
		if err := SwitchTo("agentes"); err != nil {
			t.Fatal(err)
		}
		if got := TokenFileName(); got != "kiro-auth-token.agentes.json" {
			t.Fatalf("token: %q", got)
		}
		if got := ActiveLabel(); got != "agentes" {
			t.Fatalf("active label: %q", got)
		}
	})
}

func TestSwitchToDefaultFromNamedProfile(t *testing.T) {
	withName(t, "agentes", func() {
		t.Cleanup(ResetIdentity)
		if err := SwitchTo(""); err != nil {
			t.Fatal(err)
		}
		if got := TokenFileName(); got != "kiro-auth-token.json" {
			t.Fatalf("token: %q", got)
		}
		if got := ActiveLabel(); got != DefaultLabel {
			t.Fatalf("active label: %q", got)
		}
		if got := ProxyPortFileName(); got != "proxy.agentes.port" {
			t.Fatalf("port marker: %q", got)
		}
	})
}

func TestSwitchToRejectsInvalidNamesAndKeepsTheCurrentIdentity(t *testing.T) {
	withName(t, "", func() {
		t.Cleanup(ResetIdentity)
		if err := SwitchTo("kiro2"); err != nil {
			t.Fatal(err)
		}
		if err := SwitchTo("../other"); err == nil {
			t.Fatal("expected an error")
		}
		if got := Active(); got != "kiro2" {
			t.Fatalf("active after rejected switch: %q", got)
		}
	})
}

func TestResetIdentityReturnsToTheLaunchedProfile(t *testing.T) {
	withName(t, "", func() {
		if err := SwitchTo("kiro2"); err != nil {
			t.Fatal(err)
		}
		ResetIdentity()
		if got := Active(); got != "" {
			t.Fatalf("active: %q", got)
		}
		if got := TokenFileName(); got != "kiro-auth-token.json" {
			t.Fatalf("token: %q", got)
		}
	})
}

func TestTokenFileNameFor(t *testing.T) {
	if got := TokenFileNameFor(""); got != "kiro-auth-token.json" {
		t.Fatalf("default: %q", got)
	}
	if got := TokenFileNameFor("kiro2"); got != "kiro-auth-token.kiro2.json" {
		t.Fatalf("named: %q", got)
	}
}
