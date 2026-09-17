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
//	config          read: ~/.claude2kiro/config.<profile>.yaml when it exists, else config.yaml
//	                save: always ~/.claude2kiro/config.<profile>.yaml
//
// The default profile (variable unset or empty) keeps the historical file names,
// so nothing changes for an existing install. The client registration cache is
// keyed by clientIdHash already and is shared on purpose.
//
// A name is either valid ([A-Za-z0-9_-], up to 64 chars) or fatal: silently
// dropping characters would let "../" or "a/b" resolve to another identity's
// files, which is worse than refusing to start.
package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// EnvVar is the environment variable that selects the profile.
const EnvVar = "CLAUDE2KIRO_PROFILE"

// DefaultLabel is how the default profile identifies itself where a name is
// needed (the /health header, messages).
const DefaultLabel = "default"

var validName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var (
	once sync.Once
	name string
)

// Validate returns the profile name a raw value denotes, or an error when the
// value is not a valid name. Whitespace around the value is ignored; an empty
// value is the default profile ("").
func Validate(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !validName.MatchString(raw) {
		return "", fmt.Errorf("%s=%q is not a valid profile name: use letters, digits, '-' or '_' (max 64)", EnvVar, raw)
	}
	return raw, nil
}

// Name returns the active profile name, or "" for the default profile. An
// invalid CLAUDE2KIRO_PROFILE is fatal: the process exits with code 2 and the
// reason on stderr, before any file of another identity can be touched.
func Name() string {
	once.Do(func() {
		n, err := Validate(os.Getenv(EnvVar))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		name = n
	})
	return name
}

// Label is the name to show: the profile name, or "default".
func Label() string {
	if n := Name(); n != "" {
		return n
	}
	return DefaultLabel
}

// suffixed inserts ".<profile>" before the extension of base when a profile is
// active: "kiro-auth-token.json" -> "kiro-auth-token.agentes.json".
func suffixed(base string) string {
	n := Name()
	if n == "" {
		return base
	}
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext) + "." + n + ext
}

// TokenFileName is the file name of the Kiro token for the active profile.
func TokenFileName() string { return suffixed("kiro-auth-token.json") }

// LoginConfigFileName is the file name of the saved login choice for the active profile.
func LoginConfigFileName() string { return suffixed("claude2kiro-login-config.json") }

// ProxyPortFileName is the file name of the live-proxy port marker for the active profile.
func ProxyPortFileName() string { return suffixed("proxy.port") }

// CreditHistoryFileName is the file name of the credit history for the active profile.
func CreditHistoryFileName() string { return suffixed("credit-history.jsonl") }

// ConfigSavePath is where the active profile's configuration is written:
// config.<profile>.yaml for a named profile, config.yaml for the default. A
// named profile never writes the shared file, so a Settings change under one
// identity cannot leak into the other.
func ConfigSavePath(homeDir string) string {
	return filepath.Join(homeDir, ".claude2kiro", suffixed("config.yaml"))
}

// ConfigReadPath is where the active profile's configuration is read from: its
// own config.<profile>.yaml when that file exists, otherwise the shared
// config.yaml, so a new profile inherits the machine's settings until it saves.
func ConfigReadPath(homeDir string) string {
	own := ConfigSavePath(homeDir)
	if Name() == "" {
		return own
	}
	if _, err := os.Stat(own); err == nil {
		return own
	}
	return filepath.Join(homeDir, ".claude2kiro", "config.yaml")
}
