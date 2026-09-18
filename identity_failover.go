package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/profile"
)

// Quota failover.
//
// One Kiro subscription is one Identity Center user and one monthly credit
// pool. When the pool is gone the backend answers 402 MONTHLY_REQUEST_COUNT to
// every request and nothing short of another subscribed identity helps. With
// auth.fallback_profiles configured (profiles logged in once with
// CLAUDE2KIRO_PROFILE=<name>), the proxy moves its active identity to the next
// fallback that has a token file, rebuilds the request with that identity's
// bearer and profileArn, and retries. The client never sees the 402.
//
// The switch is sticky for the life of the process: a pool that is empty today
// stays empty until its monthly reset, and the proxy has no way to learn that
// reset except by trying, which would put every request through a dead
// identity first. Restart the proxy (or the run) to go back to the primary.

var (
	identityMu sync.Mutex
	// exhaustedIdentities remembers which identities answered 402 in this
	// process, by profile name ("" = default), so they are never retried.
	exhaustedIdentities = map[string]bool{}
)

// isMonthlyQuotaExceeded reports whether an upstream answer means this
// identity cannot serve requests any more. Kiro uses 402 for the monthly credit
// pool (reason MONTHLY_REQUEST_COUNT) and for a missing subscription; neither
// can be fixed by retrying with the same token.
func isMonthlyQuotaExceeded(status int, body []byte) bool {
	return status == 402
}

// quotaReason extracts the backend's reason from a 402 body, for logs.
func quotaReason(body []byte) string {
	var parsed struct {
		Message string `json:"message"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Reason != "" {
		return parsed.Reason
	}
	return strings.TrimSpace(string(body))
}

// identityHasToken reports whether a profile was ever logged in on this
// machine, i.e. its token file exists.
func identityHasToken(name string) bool {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(homeDir, ".aws", "sso", "cache", profile.TokenFileNameFor(name)))
	return err == nil
}

// switchToFallbackIdentity marks the active identity exhausted and moves to the
// next configured fallback that is logged in and not exhausted. It returns the
// new identity's token, or an error naming every identity that is out of
// credits when none is left. Concurrent requests that hit 402 at the same time
// serialize here, so only the first one performs the switch; the others find
// the identity already moved and simply retry with the new token.
func switchToFallbackIdentity() (TokenData, error) {
	identityMu.Lock()
	defer identityMu.Unlock()

	cfg := config.Get()
	current := profile.Active()
	exhaustedIdentities[current] = true

	candidates := append([]string{profile.Name()}, cfg.Auth.FallbackProfiles...)
	for _, raw := range candidates {
		name, err := profile.Validate(raw)
		if err != nil {
			continue
		}
		if exhaustedIdentities[name] || !identityHasToken(name) {
			continue
		}
		if err := profile.SwitchTo(name); err != nil {
			continue
		}
		invalidateTokenCache()
		tok, err := getToken()
		if err != nil {
			// The file exists but cannot be read: do not leave the proxy on a
			// broken identity, try the next one.
			exhaustedIdentities[name] = true
			continue
		}
		return tok, nil
	}

	// Nothing left: stay on the current identity so the caller reports it.
	_ = profile.SwitchTo(current)
	invalidateTokenCache()
	return TokenData{}, fmt.Errorf("every configured identity is out of Kiro credits: %s", strings.Join(exhaustedLabels(), ", "))
}

// exhaustedLabels lists the identities that answered 402 in this process.
func exhaustedLabels() []string {
	names := make([]string, 0, len(exhaustedIdentities))
	for name := range exhaustedIdentities {
		if name == "" {
			names = append(names, profile.DefaultLabel)
		} else {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// invalidateTokenCache forgets the cached token so the next getToken reads the
// active identity's file.
func invalidateTokenCache() {
	tokenMutex.Lock()
	cachedToken = nil
	tokenMutex.Unlock()
}

// quotaExhaustedMessage is what the client sees when no identity can serve.
func quotaExhaustedMessage(reason string, err error) string {
	return fmt.Sprintf("Kiro credits exhausted (%s): %v. Log in another subscribed identity with "+
		"CLAUDE2KIRO_PROFILE=<name> claude2kiro login and list it under auth.fallback_profiles in ~/.claude2kiro/config.yaml, "+
		"or wait for the monthly reset (claude2kiro credits --all).", reason, err)
}

// resetIdentityState returns the proxy to the launched identity and forgets
// which pools were exhausted. Tests use it between cases.
func resetIdentityState() {
	identityMu.Lock()
	exhaustedIdentities = map[string]bool{}
	identityMu.Unlock()
	profile.ResetIdentity()
	invalidateTokenCache()
}
