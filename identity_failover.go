package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

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
// Every token a handler sends is paired with the identityRef it was read
// under. A response is only allowed to act on the identity that produced it:
// a late 402 or 403 from identity A, arriving after another request already
// moved the proxy to B, adopts B's token instead of touching B's state. The
// generation counter is what makes "already moved" detectable.
//
// The switch is sticky for the life of the process: a pool that is empty today
// stays empty until its monthly reset, and the proxy has no way to learn that
// reset except by trying, which would put every request through a dead
// identity first. Restart the proxy (or the run) to go back to the primary.

// identityRef names an identity at a point in time. Gen changes on every
// switch, so two refs with the same Name but different Gen are different
// eras of the proxy and a stale ref never acts on the current identity.
type identityRef struct {
	Name string
	Gen  uint64
}

var (
	identityMu  sync.Mutex
	identityGen uint64
	// exhaustedIdentities remembers which identities answered 402 in this
	// process, by profile name ("" = default), so they are never retried.
	exhaustedIdentities = map[string]bool{}
	// identityFailures remembers reserves the identity provider rejected for
	// good (refresh answered 400/401/403), with the reason. Everything else
	// that made a reserve unusable (unreadable file, empty token, a refresh
	// that failed for a passing reason) is tried again on the next failover.
	identityFailures = map[string]string{}
)

// currentIdentity is the identity the proxy is on right now.
func currentIdentity() identityRef {
	identityMu.Lock()
	defer identityMu.Unlock()
	return identityRef{Name: profile.Active(), Gen: identityGen}
}

// tokenForRequest reads the active identity's token and returns it together
// with the identity it belongs to. If a switch lands in the middle of the read
// the pair would be inconsistent, so the read is repeated until the identity
// observed before and after is the same.
func tokenForRequest() (TokenData, identityRef, error) {
	var lastErr error
	for range 3 {
		before := currentIdentity()
		tok, err := getToken()
		if err != nil {
			lastErr = err
			continue
		}
		if currentIdentity() == before {
			return tok, before, nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("identity kept changing while reading the token")
	}
	return TokenData{}, identityRef{}, lastErr
}

// isMonthlyQuotaExceeded reports whether an upstream answer means this
// identity cannot serve requests any more. Kiro answers 402 with reason
// MONTHLY_REQUEST_COUNT when the monthly credit pool is gone (verified against
// the DFX5 account on 2026-09-17); no retry with the same token can succeed.
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
	_, err := os.Stat(tokenFilePathFor(name))
	return err == nil
}

// tokenFilePathFor is the token file of a given profile name ("" = default).
func tokenFilePathFor(name string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".aws", "sso", "cache", profile.TokenFileNameFor(name))
}

// switchToFallbackIdentity is called with the identity whose token answered
// 402. If the proxy already moved past that identity (another request hit the
// 402 first), it returns the current identity's token without touching any
// state. Otherwise it marks the identity exhausted and moves to the next
// configured fallback that is logged in and not exhausted, refreshing its
// token when it is stale. With nothing left it returns an error naming every
// exhausted identity; the proxy stays where it was.
func switchToFallbackIdentity(failed identityRef) (TokenData, identityRef, error) {
	return switchAway(failed, "")
}

// retireIdentity is switchToFallbackIdentity for an identity whose bearer the
// backend rejects and whose refresh cannot fix (revoked login): it is not out
// of credits, it is unusable until someone logs it in again. The reason is
// remembered so the final error names it.
func retireIdentity(failed identityRef, reason string) (TokenData, identityRef, error) {
	return switchAway(failed, reason)
}

// recoverFromInvalidBearer is what a handler does with a 403 "invalid bearer"
// from identity ident. If the proxy already moved on, the current identity's
// pair is adopted. Otherwise the bearer is refreshed once, past the freshness
// shortcut (the backend just said it is no good): a refresh the provider
// rejects for good, or a bearer still rejected after a refresh, retires the
// identity and moves to the next reserve when canSwitch allows. moved tells
// the caller the pair belongs to another identity.
func recoverFromInvalidBearer(ident identityRef, refreshedFor map[string]bool, canSwitch bool) (TokenData, identityRef, bool, error) {
	if currentIdentity() != ident {
		tok, id, err := tokenForRequest()
		return tok, id, true, err
	}
	if !refreshedFor[ident.Name] {
		refreshedFor[ident.Name] = true
		err := renewToken(true)
		if err == nil {
			tok, id, terr := tokenForRequest()
			return tok, id, false, terr
		}
		if !isPermanentRefreshError(err) || !canSwitch {
			return TokenData{}, identityRef{}, false, err
		}
		tok, id, serr := retireIdentity(ident, "refresh rejected: "+err.Error())
		return tok, id, true, serr
	}
	if !canSwitch {
		return TokenData{}, identityRef{}, false, fmt.Errorf("bearer of identity %s still rejected after a refresh", ident.Name)
	}
	tok, id, serr := retireIdentity(ident, "bearer rejected after a refresh")
	return tok, id, true, serr
}

// switchAway moves the proxy off the identity that failed: out of credits
// when reason is empty, unusable for the given reason otherwise.
func switchAway(failed identityRef, reason string) (TokenData, identityRef, error) {
	identityMu.Lock()
	if failed.Gen != identityGen || failed.Name != profile.Active() {
		// Someone else already switched: adopt the current identity.
		identityMu.Unlock()
		return tokenForRequest()
	}
	if reason == "" {
		exhaustedIdentities[failed.Name] = true
	} else {
		identityFailures[failed.Name] = reason
	}

	cfg := config.Get()
	candidates := append([]string{profile.Name()}, cfg.Auth.FallbackProfiles...)
	var chosen *identityRef
	transient := map[string]string{}
	for _, raw := range candidates {
		name, err := profile.Validate(raw)
		if err != nil || exhaustedIdentities[name] || identityFailures[name] != "" || !identityHasToken(name) {
			continue
		}
		if err := profile.SwitchTo(name); err != nil {
			continue
		}
		identityGen++
		invalidateTokenCache()
		modelCatalog.Invalidate()
		tok, err := getToken()
		if err != nil {
			// The file cannot be read right now (another process may be
			// replacing it, or it is damaged): skip it this time, look again
			// on the next failover.
			transient[name] = fmt.Sprintf("token file unreadable: %v", err)
			continue
		}
		if tok.AccessToken == "" {
			// Parseable JSON is not a credential; a login may fix it later.
			transient[name] = "token file has no access token"
			continue
		}
		// A reserve that was logged in days ago may hold an expired access
		// token; renew it before choosing it. A reserve whose refresh is
		// rejected for good (revoked login) is remembered and skipped; one
		// whose refresh failed for a passing reason (network, 5xx) is skipped
		// this time and tried again on the next failover.
		if tokenIsStale(tok) {
			if err := tryRefreshToken(); err != nil {
				if isPermanentRefreshError(err) {
					identityFailures[name] = fmt.Sprintf("refresh failed: %v", err)
				} else {
					transient[name] = fmt.Sprintf("refresh failed for now: %v", err)
				}
				continue
			}
		}
		chosen = &identityRef{Name: name, Gen: identityGen}
		break
	}
	if chosen == nil {
		// Nothing left: back to the identity that failed so the caller reports
		// it. This is a switch like any other: every cache filled for a
		// candidate that was tried and rejected is dropped.
		_ = profile.SwitchTo(failed.Name)
		identityGen++
		invalidateTokenCache()
		modelCatalog.Invalidate()
		back := identityRef{Name: failed.Name, Gen: identityGen}
		err := fmt.Errorf("every configured identity is unavailable: %s", strings.Join(exhaustedLabels(transient), ", "))
		identityMu.Unlock()
		return TokenData{}, back, err
	}
	identityMu.Unlock()
	return tokenForRequest()
}

// isPermanentRefreshError tells a refresh the identity provider rejected
// (invalid or revoked grant, bad client: HTTP 400, 401, 403) from one that
// failed for a passing reason (network error, timeout, 5xx, 429).
func isPermanentRefreshError(err error) bool {
	if err == nil {
		return false
	}
	m := refreshStatusPattern.FindStringSubmatch(err.Error())
	if m == nil {
		return false
	}
	switch m[1] {
	case "400", "401", "403":
		return true
	}
	return false
}

var refreshStatusPattern = regexp.MustCompile(`status(?: code)?:? (\d{3})`)

// tokenIsStale reports whether the access token is expired or about to be.
func tokenIsStale(tok TokenData) bool {
	if tok.ExpiresAt == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, tok.ExpiresAt)
	if err != nil {
		return false
	}
	return time.Until(expiresAt) <= 5*time.Minute
}

// exhaustedLabels lists the identities that answered 402 in this process and
// the reserves that failed for another reason, with that reason; extra holds
// the failures of this one selection that are not remembered. Caller holds
// identityMu.
func exhaustedLabels(extra map[string]string) []string {
	label := func(name string) string {
		if name == "" {
			return profile.DefaultLabel
		}
		return name
	}
	names := make([]string, 0, len(exhaustedIdentities)+len(identityFailures)+len(extra))
	for name := range exhaustedIdentities {
		names = append(names, label(name)+" (out of credits)")
	}
	for name, reason := range identityFailures {
		names = append(names, label(name)+" ("+reason+")")
	}
	for name, reason := range extra {
		names = append(names, label(name)+" ("+reason+")")
	}
	sort.Strings(names)
	return names
}

// invalidateTokenCache forgets the cached token so the next getToken reads the
// active identity's file.
func invalidateTokenCache() {
	tokenMutex.Lock()
	cachedToken = nil
	cachedIdentity = ""
	tokenMutex.Unlock()
}

// quotaExhaustedMessage is what the client sees when no identity can serve.
func quotaExhaustedMessage(reason string, err error) string {
	return fmt.Sprintf("Kiro credits exhausted (%s): %v. Log in another subscribed identity with "+
		"CLAUDE2KIRO_PROFILE=<name> claude2kiro login and list it under auth.fallback_profiles in the config file "+
		"this proxy reads, or wait for the monthly reset (claude2kiro credits --all).", reason, err)
}

// resetIdentityState returns the proxy to the launched identity and forgets
// which pools were exhausted. Tests use it between cases.
func resetIdentityState() {
	identityMu.Lock()
	exhaustedIdentities = map[string]bool{}
	identityFailures = map[string]string{}
	identityGen++
	identityMu.Unlock()
	profile.ResetIdentity()
	invalidateTokenCache()
}
