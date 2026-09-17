// Package profile scopes the per-identity files of claude2kiro so that two Kiro
// identities can be used on the same machine, at the same time.
//
// One Kiro subscription is one Identity Center user, and one user is one monthly
// credit pool. When the pool runs out (16-sep-2026: 9,963 of 10,000 Power credits
// gone in 14 days) the only way to keep working is a second user with its own
// subscription. Everything that identifies a session is keyed by the profile
// name taken from CLAUDE2KIRO_PROFILE:
//
//	token           ~/.aws/sso/cache/kiro-auth-token[.<profile>].json
//	login config    ~/.aws/sso/cache/claude2kiro-login-config[.<profile>].json
//	proxy port file ~/.claude2kiro/proxy[.<profile>].port
//	credit history  ~/.claude2kiro/credit-history[.<profile>].jsonl
//	config          ~/.claude2kiro/config.<profile>.yaml when it exists, else config.yaml
//
// The default profile (variable unset or empty) keeps the historical file names,
// so nothing changes for an existing install. The client registration cache is
// keyed by clientIdHash already and is shared on purpose.
package profile

import (
	"os"
	"path/filepath"
	"strings"
)

// EnvVar is the environment variable that selects the profile.
const EnvVar = "CLAUDE2KIRO_PROFILE"

// Name returns the active profile name, sanitized to [A-Za-z0-9_-], or "" for
// the default profile.
func Name() string {
	return sanitize(os.Getenv(EnvVar))
}

func sanitize(raw string) string {
	raw = strings.TrimSpace(raw)
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// suffixed inserts ".<profile>" before the extension of base when a profile is
// active: "kiro-auth-token.json" -> "kiro-auth-token.agentes.json".
func suffixed(base string) string {
	name := Name()
	if name == "" {
		return base
	}
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext) + "." + name + ext
}

// TokenFileName is the file name of the Kiro token for the active profile.
func TokenFileName() string { return suffixed("kiro-auth-token.json") }

// LoginConfigFileName is the file name of the saved login choice for the active profile.
func LoginConfigFileName() string { return suffixed("claude2kiro-login-config.json") }

// ProxyPortFileName is the file name of the live-proxy port marker for the active profile.
func ProxyPortFileName() string { return suffixed("proxy.port") }

// CreditHistoryFileName is the file name of the credit history for the active profile.
func CreditHistoryFileName() string { return suffixed("credit-history.jsonl") }

// ConfigFilePath returns ~/.claude2kiro/config.<profile>.yaml if the active
// profile has its own config, otherwise the shared ~/.claude2kiro/config.yaml.
// A per-profile config is how a second identity gets its own server.port.
func ConfigFilePath(homeDir string) string {
	shared := filepath.Join(homeDir, ".claude2kiro", "config.yaml")
	name := Name()
	if name == "" {
		return shared
	}
	own := filepath.Join(homeDir, ".claude2kiro", "config."+name+".yaml")
	if _, err := os.Stat(own); err == nil {
		return own
	}
	return shared
}
