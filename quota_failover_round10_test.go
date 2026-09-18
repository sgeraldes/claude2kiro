package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/cmd"
	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/profile"
)

// Ninth verification pass of the quota failover: a rejection after the
// identity's refresh looks at the file before retiring it; a retired
// identity comes back when someone logs it in again; a cache hit is checked
// against the file's content; a logout resolves both its files from one
// read of the active name; a request cannot retry on rejected bearers
// without end.

// N28: X was refreshed once (fresh-X); a login Y in another process landed
// before fresh-X's late rejection. The rejection adopts Y instead of
// retiring the identity; Y gets its own refresh if rejected too.
func TestLateRejectionAfterARefreshAdoptsANewLogin(t *testing.T) {
	for _, method := range []string{"IdC", "Social"} {
		t.Run(method, func(t *testing.T) {
			x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: method, ClientIdHash: "x", ProfileArn: "arn:primary", ExpiresAt: farFuture()}
			b := TokenData{AccessToken: "b-token", AuthMethod: "IdC", ProfileArn: "arn:b", ExpiresAt: farFuture()}
			home := withIdentities(t, map[string]TokenData{"": x, "b": b})
			withIdCRefresh(t, home, "unused")
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				io.WriteString(w, `{"accessToken":"fresh-X","refreshToken":"rotated-X","expiresIn":3600}`)
			}))
			t.Cleanup(provider.Close)
			cfg := *config.Get()
			cfg.Advanced.SSOOIDCTokenEndpoint = provider.URL
			cfg.Advanced.KiroRefreshEndpoint = provider.URL
			cfg.Auth.FallbackProfiles = []string{"b"}
			withConfig(t, &cfg)

			ident := currentIdentity()
			refreshedFor := map[string]bool{}
			// first rejection: X is refreshed
			got, _, moved, err := recoverFromInvalidBearer(ident, x, refreshedFor, true)
			if err != nil || moved || got.AccessToken != "fresh-X" || !refreshedFor["rotated-X"] {
				t.Fatalf("first recovery: %+v moved=%v err=%v refreshed=%v", got, moved, err, refreshedFor)
			}
			// a login in another process replaces fresh-X with Y
			y := TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: method, ClientIdHash: "x", ProfileArn: "arn:Y", ExpiresAt: farFuture()}
			loginFromChildProcess(t, y)
			// the late rejection of fresh-X
			got, id, moved, err := recoverFromInvalidBearer(ident, got, refreshedFor, true)
			if err != nil {
				t.Fatal(err)
			}
			if moved || id.Name != ident.Name || got.AccessToken != "user-Y" {
				t.Fatalf("login Y must be adopted on the same identity: moved=%v id=%+v tok=%+v", moved, id, got)
			}
			if calls.Load() != 1 {
				t.Fatalf("the provider was asked %d times; Y was never refreshed here", calls.Load())
			}
			identityMu.Lock()
			reason := identityFailures[ident.Name]
			identityMu.Unlock()
			if reason != "" {
				t.Fatalf("the identity was retired with login Y on disk: %q", reason)
			}
			if refreshedFor["ry"] {
				t.Fatal("login Y must keep its own refresh")
			}
			// Y rejected too: refreshed once, then retired
			got, _, moved, err = recoverFromInvalidBearer(ident, y, refreshedFor, true)
			if err != nil || moved || got.AccessToken != "fresh-X" || calls.Load() != 2 {
				t.Fatalf("Y's refresh: %+v moved=%v err=%v calls=%d", got, moved, err, calls.Load())
			}
		})
	}
}

// N28 through the handlers: a bearer rejected after a login landed in
// another process is retried with the login, and the identity stays.
func TestHandlerRetriesWithTheLoginThatReplacedARejectedBearer(t *testing.T) {
	for _, stream := range []bool{true, false} {
		name := map[bool]string{true: "stream", false: "non-stream"}[stream]
		t.Run(name, func(t *testing.T) {
			x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "Social", ProfileArn: "arn:primary", ExpiresAt: farFuture()}
			b := TokenData{AccessToken: "b-token", AuthMethod: "IdC", ProfileArn: "arn:b", ExpiresAt: farFuture()}
			withIdentities(t, map[string]TokenData{"": x, "b": b})
			backend := newFakeQuotaBackend(t)
			backend.rejectBearer("user-X")
			backend.rejectBearer("fresh-X")
			// the login lands while the backend holds fresh-X's answer: the
			// hold is armed by the refresh, which runs between the two requests
			landed := make(chan struct{})
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backend.mu.Lock()
				backend.hold = landed
				backend.mu.Unlock()
				io.WriteString(w, `{"accessToken":"fresh-X","refreshToken":"rotated-X","expiresIn":3600}`)
			}))
			t.Cleanup(provider.Close)
			cfg := *config.Get()
			cfg.Advanced.CodeWhispererEndpoint = backend.server.URL
			cfg.Advanced.KiroRefreshEndpoint = provider.URL
			cfg.Auth.FallbackProfiles = []string{"b"}
			withConfig(t, &cfg)

			go func() {
				backend.waitForRequests(t, 2) // user-X, then fresh-X
				loginFromChildProcess(t, TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: "Social", ProfileArn: "arn:Y", ExpiresAt: farFuture()})
				close(landed)
			}()
			var body string
			if stream {
				body = streamOnce(t).Body.String()
			} else {
				rec, status := nonStreamOnce(t)
				body = rec.Body.String()
				if status != http.StatusOK {
					t.Fatalf("status %d: %s", status, body)
				}
			}
			assertFallbackAnswer(t, body)
			bearers, _ := backend.seen()
			if bearers[len(bearers)-1] != "user-Y" {
				t.Fatalf("the login that replaced the rejected bearer must serve: %v", bearers)
			}
			if got := currentIdentity().Name; got != profile.Name() {
				t.Fatalf("the proxy moved to %q with login Y on disk", got)
			}
			identityMu.Lock()
			reason := identityFailures[profile.Name()]
			identityMu.Unlock()
			if reason != "" {
				t.Fatalf("retired: %q", reason)
			}
		})
	}
}

// A retired identity whose file someone logged in again is tried on the
// next failover instead of being skipped for the rest of the process.
func TestARetiredIdentityComesBackWithANewLogin(t *testing.T) {
	revoked := TokenData{AccessToken: "revoked-B", RefreshToken: "b-refresh", AuthMethod: "IdC", ClientIdHash: "x", ProfileArn: "arn:b", ExpiresAt: farFuture()}
	good := TokenData{AccessToken: "good-token", AuthMethod: "IdC", ProfileArn: "arn:good", ExpiresAt: farFuture()}
	home := withIdentities(t, map[string]TokenData{"": primaryToken(), "b": revoked, "good": good})
	withIdCRefresh(t, home, "unused")
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"invalid_grant"}`)
	}))
	t.Cleanup(oidc.Close)
	backend := newFakeQuotaBackend(t, "primary-token", "good-token")
	backend.rejectBearer("revoked-B")
	cfg := *config.Get()
	cfg.Advanced.CodeWhispererEndpoint = backend.server.URL
	cfg.Advanced.SSOOIDCTokenEndpoint = oidc.URL
	cfg.Auth.FallbackProfiles = []string{"b", "good"}
	withConfig(t, &cfg)

	// primary out of credits, b's bearer revoked and its refresh rejected,
	// good out of credits: nothing left
	rec, _ := nonStreamOnce(t)
	if !containsAll(rec.Body.String(), "every configured identity is unavailable", "b (") {
		t.Fatalf("expected every identity unavailable, got: %s", rec.Body.String())
	}
	identityMu.Lock()
	retired := identityFailures["b"]
	identityMu.Unlock()
	if retired == "" {
		t.Fatal("b must be retired")
	}
	// b is logged in again (another process): a new bearer the backend accepts
	writeIdentityToken(t, "b", TokenData{AccessToken: "new-B", RefreshToken: "b2", AuthMethod: "IdC", ProfileArn: "arn:b", ExpiresAt: farFuture()})
	rec, status := nonStreamOnce(t)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, rec.Body.String())
	}
	assertFallbackAnswer(t, rec.Body.String())
	bearers, _ := backend.seen()
	if bearers[len(bearers)-1] != "new-B" {
		t.Fatalf("the new login of b must serve: %v", bearers)
	}
	if got := currentIdentity().Name; got != "b" {
		t.Fatalf("active identity: %q", got)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

// N29: a file another process wrote with the same size and modification
// time as the cached one is still noticed: the hit is checked by content.
func TestCacheHitIsCheckedByContentNotByStamp(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "Social", ProfileArn: "arn:X", ExpiresAt: farFuture()}
	withIdentities(t, map[string]TokenData{"": x})
	if err := saveToken(&x); err != nil { // written as a login writes it, so Y below has the same size
		t.Fatal(err)
	}
	if tok, err := getToken(); err != nil || tok.AccessToken != "user-X" {
		t.Fatalf("%+v %v", tok, err)
	}
	info, err := os.Stat(identityFile(""))
	if err != nil {
		t.Fatal(err)
	}
	// the same size, the same time: a child login of Y, with the time put back
	y := TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: "Social", ProfileArn: "arn:Y", ExpiresAt: x.ExpiresAt}
	loginFromChildProcess(t, y)
	if err := os.Chtimes(identityFile(""), info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(identityFile(""))
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		t.Fatalf("the fixture must collide: before %d/%v after %d/%v", info.Size(), info.ModTime(), after.Size(), after.ModTime())
	}
	for i := range 3 {
		if tok, err := getToken(); err != nil || tok.AccessToken != "user-Y" {
			t.Fatalf("read %d served %+v %v", i, tok, err)
		}
	}
	// a merged ARN in another process is noticed the same way
	withArn := y
	withArn.ProfileArn = "arn:Y2"
	loginFromChildProcess(t, withArn)
	if tok, _ := getToken(); tok.ProfileArn != "arn:Y2" {
		t.Fatalf("the file's ARN must be served: %+v", tok)
	}
}

// N27 residual: a logout's config and token paths come from one read of
// the active name; a failover between the two reads cannot split them.
func TestLogoutFilesComeFromOneIdentity(t *testing.T) {
	a := TokenData{AccessToken: "a-token", AuthMethod: "IdC", ProfileArn: "arn:a", ExpiresAt: farFuture()}
	b := TokenData{AccessToken: "b-token", AuthMethod: "IdC", ProfileArn: "arn:b", ExpiresAt: farFuture()}
	withIdentities(t, map[string]TokenData{"": a, "b": b})
	configA, tokenA := cmd.LoginFilesFor(profile.Name())
	configB, tokenB := cmd.LoginFilesFor("b")
	for _, p := range []string{configA, configB} {
		if err := os.WriteFile(p, []byte(`{"provider":"x"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if tokenA != identityFile("") || tokenB != identityFile("b") {
		t.Fatalf("token paths: %s %s", tokenA, tokenB)
	}
	if err := profile.SwitchTo("b"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = profile.SwitchTo(profile.Name()) })
	// the paths resolved for A stay A's, whatever is active now
	if _, _, err := cmd.RemoveLogin(configA, tokenA); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{configA, tokenA} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s still exists", p)
		}
	}
	for _, p := range []string{configB, tokenB} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s of identity b was removed", p)
		}
	}
}

// A file that keeps changing under a request with credentials the backend
// keeps rejecting cannot keep the request retrying: both handlers stop.
func TestRejectedBearersDoNotRetryWithoutEnd(t *testing.T) {
	for _, stream := range []bool{true, false} {
		name := map[bool]string{true: "stream", false: "non-stream"}[stream]
		t.Run(name, func(t *testing.T) {
			x := TokenData{AccessToken: "user-0", RefreshToken: "r0", AuthMethod: "Social", ProfileArn: "arn:primary", ExpiresAt: farFuture()}
			withIdentities(t, map[string]TokenData{"": x})
			var n atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// every bearer is rejected, and another "process" writes a new
				// one before each answer
				k := n.Add(1)
				next := TokenData{AccessToken: "user-" + string(rune('0'+k%10)) + string(rune('a'+k/10)), RefreshToken: "r", AuthMethod: "Social", ProfileArn: "arn:primary", ExpiresAt: farFuture()}
				if err := saveToken(&next); err != nil {
					t.Error(err)
				}
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, invalidBearer)
			}))
			t.Cleanup(backend.Close)
			cfg := *config.Get()
			cfg.Advanced.CodeWhispererEndpoint = backend.URL
			withConfig(t, &cfg)
			done := make(chan struct{})
			go func() {
				defer close(done)
				if stream {
					streamOnce(t)
				} else {
					nonStreamOnce(t)
				}
			}()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("the request did not finish")
			}
			if n.Load() > 10 {
				t.Fatalf("the request retried %d times", n.Load())
			}
		})
	}
}
