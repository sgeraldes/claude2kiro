package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
)

// Fifth verification pass of the quota failover: a credit reading is filed
// under the identity it was read for, a reserve whose file cannot be read
// right now is looked at again, an empty refresh answer changes nothing on
// disk, the rollback returns a reference built under the lock, and a stray
// login callback never blocks.

// N14: a reserve whose token file is unreadable at one failover (another
// process replacing it, a damaged write) is not written off; once the file
// is whole again the next failover takes it.
func TestUnreadableReserveIsReconsideredOnTheNextFailover(t *testing.T) {
	good := TokenData{AccessToken: "good-token", AuthMethod: "IdC", ProfileArn: "arn:good", ExpiresAt: farFuture()}
	withIdentities(t, map[string]TokenData{"": primaryToken(), "b": good})
	if err := os.WriteFile(identityFile("b"), []byte(`{"accessToken":"go`), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := newFakeQuotaBackend(t, "primary-token")
	failoverConfig(t, backend.server.URL, "b")

	_, _, err := switchToFallbackIdentity(currentIdentity())
	if err == nil || !strings.Contains(err.Error(), "b (token file unreadable") {
		t.Fatalf("first selection must fail on the damaged file, got: %v", err)
	}
	identityMu.Lock()
	remembered := identityFailures["b"]
	identityMu.Unlock()
	if remembered != "" {
		t.Fatalf("a damaged file was written off: %q", remembered)
	}

	writeIdentityToken(t, "b", good)
	tok, next, err := switchToFallbackIdentity(currentIdentity())
	if err != nil || next.Name != "b" || tok.AccessToken != "good-token" {
		t.Fatalf("the repaired reserve must be taken: %v %+v %q", err, next, tok.AccessToken)
	}
}

// N16: a refresh that answers 200 with no access token is a failed refresh.
// The reserve's file keeps its credentials and the next reserve serves.
func TestEmptyRefreshAnswerLeavesTheReserveIntactAndMovesOn(t *testing.T) {
	for _, method := range []string{"IdC", "Social"} {
		t.Run(method, func(t *testing.T) {
			stale := TokenData{AccessToken: "b-stale", RefreshToken: "b-refresh", AuthMethod: method, ClientIdHash: "x",
				ProfileArn: "arn:b", ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
			good := TokenData{AccessToken: "good-token", AuthMethod: "IdC", ProfileArn: "arn:good", ExpiresAt: farFuture()}
			home := withIdentities(t, map[string]TokenData{"": primaryToken(), "b": stale, "good": good})
			reg := filepath.Join(home, ".aws", "sso", "cache", "x.json")
			if err := os.WriteFile(reg, []byte(`{"clientId":"c","clientSecret":"s"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{}`)
			}))
			t.Cleanup(empty.Close)
			backend := newFakeQuotaBackend(t, "primary-token")
			cfg := *config.Get()
			cfg.Advanced.CodeWhispererEndpoint = backend.server.URL
			cfg.Advanced.SSOOIDCTokenEndpoint = empty.URL
			cfg.Advanced.KiroRefreshEndpoint = empty.URL
			cfg.Auth.FallbackProfiles = []string{"b", "good"}
			withConfig(t, &cfg)

			rec, status := nonStreamOnce(t)
			if status != http.StatusOK {
				t.Fatalf("status %d: %s", status, rec.Body.String())
			}
			bearers, _ := backend.seen()
			if len(bearers) != 2 || bearers[1] != "good-token" {
				t.Fatalf("the next reserve must serve: %v", bearers)
			}
			if disk := readIdentityToken(t, "b"); disk.AccessToken != "b-stale" || disk.RefreshToken != "b-refresh" {
				t.Fatalf("an empty refresh answer must not touch the file: %+v", disk)
			}
		})
	}
}

// N16, the other half: a refresh that rotates the access token but omits the
// refresh token keeps the one that worked.
func TestRefreshWithoutRotationKeepsTheRefreshToken(t *testing.T) {
	stale := TokenData{AccessToken: "old", RefreshToken: "keep-me", AuthMethod: "IdC", ClientIdHash: "x",
		ProfileArn: "arn:p", ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	home := withIdentities(t, map[string]TokenData{"": stale})
	reg := filepath.Join(home, ".aws", "sso", "cache", "x.json")
	if err := os.WriteFile(reg, []byte(`{"clientId":"c","clientSecret":"s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"accessToken":"new","expiresIn":3600}`)
	}))
	t.Cleanup(oidc.Close)
	cfg := *config.Get()
	cfg.Advanced.SSOOIDCTokenEndpoint = oidc.URL
	withConfig(t, &cfg)

	if err := tryRefreshToken(); err != nil {
		t.Fatal(err)
	}
	if disk := readIdentityToken(t, ""); disk.AccessToken != "new" || disk.RefreshToken != "keep-me" {
		t.Fatalf("after a refresh without rotation: %+v", disk)
	}
}

// N17: the reference returned by a failed selection is built while the lock
// is held; concurrent selections cannot change its generation under it.
func TestRollbackReferenceIsStableUnderConcurrentSelections(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken()})
	backend := newFakeQuotaBackend(t, "primary-token")
	failoverConfig(t, backend.server.URL)

	var wg sync.WaitGroup
	var mismatches atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				ref := currentIdentity()
				_, back, err := switchToFallbackIdentity(ref)
				if err == nil {
					continue
				}
				// The returned reference is what the proxy is on after the
				// rollback; another rollback may have moved on since, but the
				// value itself must be a consistent (name, gen) pair.
				if back.Name != ref.Name {
					mismatches.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if mismatches.Load() != 0 {
		t.Fatalf("%d references named another identity", mismatches.Load())
	}
}

// N18: the login's failure channel holds one result; later failures are
// dropped instead of blocking their handler.
func TestLoginFailerNeverBlocks(t *testing.T) {
	errChan := make(chan error, 1)
	fail := loginFailer(&loginOutcome{}, errChan)
	done := make(chan struct{})
	go func() {
		fail(errors.New("first"))
		fail(errors.New("second"))
		fail(errors.New("third"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a repeated login failure blocked")
	}
	if err := <-errChan; err == nil || err.Error() != "first" {
		t.Fatalf("the first failure must be the login's outcome, got %v", err)
	}
	select {
	case err := <-errChan:
		t.Fatalf("a later failure leaked: %v", err)
	default:
	}
}
