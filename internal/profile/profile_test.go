package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultProfileKeepsHistoricalNames(t *testing.T) {
	t.Setenv(EnvVar, "")
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
	home := t.TempDir()
	if got := ConfigFilePath(home); got != filepath.Join(home, ".claude2kiro", "config.yaml") {
		t.Fatalf("config: %q", got)
	}
}

func TestNamedProfileSuffixesEveryPerIdentityFile(t *testing.T) {
	t.Setenv(EnvVar, "agentes")
	cases := map[string]string{
		TokenFileName():         "kiro-auth-token.agentes.json",
		LoginConfigFileName():   "claude2kiro-login-config.agentes.json",
		ProxyPortFileName():     "proxy.agentes.port",
		CreditHistoryFileName(): "credit-history.agentes.jsonl",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}

func TestNameIsSanitized(t *testing.T) {
	t.Setenv(EnvVar, " ../evil name!/ ")
	if got := Name(); got != "evilname" {
		t.Fatalf("sanitized name: %q", got)
	}
	if got := TokenFileName(); got != "kiro-auth-token.evilname.json" {
		t.Fatalf("token: %q", got)
	}
}

func TestConfigFallsBackToSharedUnlessProfileHasItsOwn(t *testing.T) {
	t.Setenv(EnvVar, "agentes")
	home := t.TempDir()
	dir := filepath.Join(home, ".claude2kiro")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ConfigFilePath(home); got != filepath.Join(dir, "config.yaml") {
		t.Fatalf("without own config: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.agentes.yaml"), []byte("server:\n  port: \"8081\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ConfigFilePath(home); got != filepath.Join(dir, "config.agentes.yaml") {
		t.Fatalf("with own config: %q", got)
	}
}
