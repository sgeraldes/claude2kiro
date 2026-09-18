package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/cmd"
	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/models"
	"github.com/sgeraldes/claude2kiro/internal/profile"
)

// Fourth verification pass of the quota failover: cache publication and the
// write generation move as one step, every writer of a token file is the same
// writer, a reserve is rejected for good only when the identity provider
// rejects it, and a rejected candidate leaves nothing behind.

func stalePrimaryHome(t *testing.T) string {
	t.Helper()
	old := TokenData{AccessToken: "old-token", RefreshToken: "old-refresh", AuthMethod: "IdC", ClientIdHash: "x",
		ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}
	home := withIdentities(t, map[string]TokenData{"": old})
	withIdCRefresh(t, home, "fresh-token")
	return home
}

// H03: a reader that checked the generation and then lost the CPU cannot
// install its old read after a refresh published a newer token: the check
// and the install are one step under the cache lock.
func TestPublishAtRefusesAnOlderReadAfterAWrite(t *testing.T) {
	stalePrimaryHome(t)
	old, err := getToken()
	if err != nil {
		t.Fatal(err)
	}
	gen := tokenWriteGen.Load()
	if err := tryRefreshToken(); err != nil {
		t.Fatal(err)
	}
	if publishTokenAt(profile.Active(), old, gen) {
		t.Fatal("an older read was installed over a refreshed token")
	}
	if tok, _ := getToken(); tok.AccessToken != "fresh-token" {
		t.Fatalf("cache: %q", tok.AccessToken)
	}
}

// R01: the TUI's refresh is the proxy's refresh: it moves the write
// generation, so a reader parked in discovery with the old bearer re-reads.
func TestTUIRefreshMovesTheWriteGeneration(t *testing.T) {
	stalePrimaryHome(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	profiles := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == "old-token" {
			close(entered)
			<-release
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, `{"profiles":[{"arn":"arn:refreshed","profileName":"p"}]}`)
	}))
	t.Cleanup(profiles.Close)
	cfg := *config.Get()
	cfg.Advanced.ProfilesEndpoint = profiles.URL
	withConfig(t, &cfg)

	before := tokenWriteGen.Load()
	done := make(chan TokenData, 1)
	go func() { tok, _ := getToken(); done <- tok }()
	<-entered
	msg := refreshTokenCmd()
	if r, ok := msg.(cmd.RefreshResultMsg); !ok || !r.Success {
		t.Fatalf("refresh: %+v", msg)
	}
	close(release)
	reader := <-done
	if tokenWriteGen.Load() == before {
		t.Fatal("the TUI refresh did not move the write generation")
	}
	if reader.AccessToken != "fresh-token" {
		t.Fatalf("the parked reader returned %q after the TUI refresh", reader.AccessToken)
	}
	if tok, _ := getToken(); tok.AccessToken != "fresh-token" {
		t.Fatalf("cache: %q", tok.AccessToken)
	}
}

// N09: a reserve whose refresh failed for a passing reason (5xx) is not
// written off; the next failover tries it again and succeeds once the
// provider is back.
func TestTransientRefreshFailureIsRetriedOnTheNextFailover(t *testing.T) {
	stale := TokenData{AccessToken: "b-stale", RefreshToken: "b-refresh", AuthMethod: "IdC", ClientIdHash: "x",
		ProfileArn: "arn:b", ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	home := withIdentities(t, map[string]TokenData{"": primaryToken(), "b": stale})
	reg := filepath.Join(home, ".aws", "sso", "cache", "x.json")
	if err := os.WriteFile(reg, []byte(`{"clientId":"c","clientSecret":"s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var down atomic.Bool
	down.Store(true)
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, `{"accessToken":"b-fresh","refreshToken":"r","expiresIn":3600}`)
	}))
	t.Cleanup(oidc.Close)
	cfg := *config.Get()
	cfg.Advanced.SSOOIDCTokenEndpoint = oidc.URL
	cfg.Auth.FallbackProfiles = []string{"b"}
	withConfig(t, &cfg)

	_, _, err := switchToFallbackIdentity(currentIdentity(), primaryToken())
	if err == nil || !strings.Contains(err.Error(), "b (refresh failed for now") {
		t.Fatalf("first selection must fail and name the passing failure, got: %v", err)
	}
	identityMu.Lock()
	remembered := identityFailures["b"]
	identityMu.Unlock()
	if remembered != "" {
		t.Fatalf("a passing failure was written off: %q", remembered)
	}

	down.Store(false)
	tok, next, err := switchToFallbackIdentity(currentIdentity(), primaryToken())
	if err != nil || next.Name != "b" || tok.AccessToken != "b-fresh" {
		t.Fatalf("second selection must reach the recovered reserve: %v %+v %q", err, next, tok.AccessToken)
	}
	if calls.Load() != 2 {
		t.Fatalf("refresh calls=%d, want 2", calls.Load())
	}
}

// N10: a token file that parses but holds no access token is not a reserve;
// the next one is taken and the empty one is named.
func TestEmptyReserveDoesNotHideTheHealthyOne(t *testing.T) {
	good := TokenData{AccessToken: "good-token", AuthMethod: "IdC", ProfileArn: "arn:good", ExpiresAt: farFuture()}
	withIdentities(t, map[string]TokenData{"": primaryToken(), "empty": {}, "good": good})
	backend := newFakeQuotaBackend(t, "primary-token")
	failoverConfig(t, backend.server.URL, "empty", "good")

	rec, status := nonStreamOnce(t)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, rec.Body.String())
	}
	bearers, _ := backend.seen()
	if len(bearers) != 2 || bearers[1] != "good-token" {
		t.Fatalf("the empty reserve must be skipped: %v", bearers)
	}
	identityMu.Lock()
	remembered := identityFailures["empty"]
	identityMu.Unlock()
	if remembered != "" {
		t.Fatalf("an empty token file is not a permanent rejection; a login may fill it later, got %q", remembered)
	}
}

// N11/R04: when every candidate is rejected and the proxy returns to the
// identity that failed, the caches filled for the candidates are gone.
func TestRollbackAfterRejectedCandidatesDropsTheirCaches(t *testing.T) {
	bad := TokenData{AccessToken: "bad-stale", RefreshToken: "bad-refresh", AuthMethod: "IdC", ClientIdHash: "x",
		ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	home := withIdentities(t, map[string]TokenData{"": primaryToken(), "bad": bad})
	reg := filepath.Join(home, ".aws", "sso", "cache", "x.json")
	if err := os.WriteFile(reg, []byte(`{"clientId":"c","clientSecret":"s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int32
	seeded := models.NewCatalog(10*time.Minute, func() ([]models.KiroModel, error) {
		fetches.Add(1)
		return []models.KiroModel{{ModelID: "only-" + profile.Active()}}, nil
	})
	orig := modelCatalog
	modelCatalog = seeded
	t.Cleanup(func() { modelCatalog = orig })
	// While the candidate is being refreshed it is the active identity; a
	// model fetch that runs then (the refresher, a request) fills the
	// catalog with the candidate's list.
	var seenDuringCandidate []models.KiroModel
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenDuringCandidate = modelCatalog.Models()
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"invalid_grant"}`)
	}))
	t.Cleanup(oidc.Close)
	cfg := *config.Get()
	cfg.Advanced.SSOOIDCTokenEndpoint = oidc.URL
	cfg.Auth.FallbackProfiles = []string{"bad"}
	withConfig(t, &cfg)

	before := currentIdentity()
	_, after, err := switchToFallbackIdentity(before, readIdentityToken(t, ""))
	if err == nil {
		t.Fatal("expected an error with nothing usable left")
	}
	if after.Name != before.Name || after.Gen == before.Gen {
		t.Fatalf("the rollback must be a new generation of the same identity: %+v -> %+v", before, after)
	}
	if len(seenDuringCandidate) != 1 || seenDuringCandidate[0].ModelID != "only-bad" {
		t.Fatalf("precondition: the candidate's list was fetched while it was active: %+v", seenDuringCandidate)
	}
	if got := modelCatalog.Models(); len(got) != 1 || got[0].ModelID != "only-"+profile.Name() {
		t.Fatalf("the catalog after the rollback must be the primary's, got %+v (fetches=%d)", got, fetches.Load())
	}
	if fetches.Load() != 2 {
		t.Fatalf("the candidate's list must be dropped and refetched: fetches=%d", fetches.Load())
	}
}

// R04: after an identity switch a failed fetch for the new identity leaves
// the catalog empty rather than serving the previous identity's list.
func TestCatalogDoesNotServeThePreviousIdentityAfterAFailedFetch(t *testing.T) {
	var calls atomic.Int32
	c := models.NewCatalog(10*time.Minute, func() ([]models.KiroModel, error) {
		if calls.Add(1) == 1 {
			return []models.KiroModel{{ModelID: "only-A"}}, nil
		}
		return nil, io.ErrUnexpectedEOF // the new identity's fetch fails
	})
	if got := c.Models(); len(got) != 1 {
		t.Fatalf("precondition: %+v", got)
	}
	c.Invalidate()
	if c.Has("only-A") || len(c.Models()) != 0 {
		t.Fatal("the previous identity's list is still served after Invalidate")
	}
}

// N12: an IdC token without its ARN is cached only briefly, so discovery is
// retried soon after the profiles API recovers.
func TestUnresolvedArnIsCachedBriefly(t *testing.T) {
	primary := primaryToken()
	primary.ProfileArn = ""
	withIdentities(t, map[string]TokenData{"": primary})
	var calls atomic.Int32
	profiles := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, `{"profiles":[{"arn":"arn:recovered","profileName":"p"}]}`)
	}))
	t.Cleanup(profiles.Close)
	cfg := *config.Get()
	cfg.Advanced.ProfilesEndpoint = profiles.URL
	withConfig(t, &cfg)

	if tok, _ := getToken(); tok.ProfileArn != "" {
		t.Fatalf("first read: %+v", tok)
	}
	tokenMutex.Lock()
	ttl := cachedTTL
	tokenMutex.Unlock()
	if ttl != unresolvedArnCacheTTL {
		t.Fatalf("an unresolved ARN must be cached for %v, got %v", unresolvedArnCacheTTL, ttl)
	}
	// Age the cache past the short TTL instead of sleeping.
	tokenMutex.Lock()
	cachedTokenTime = time.Now().Add(-unresolvedArnCacheTTL)
	tokenMutex.Unlock()
	if tok, _ := getToken(); tok.ProfileArn != "arn:recovered" {
		t.Fatalf("discovery must be retried after the short TTL: %+v (calls=%d)", tok, calls.Load())
	}
}

// N13: whoever reads the token for a request waits for a failover in
// progress, so a candidate that is being checked is never observed.
func TestTokenForRequestWaitsForASelectionInProgress(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken()})
	identityMu.Lock()
	done := make(chan struct{})
	go func() { _, _, _ = tokenForRequest(); close(done) }()
	select {
	case <-done:
		t.Fatal("tokenForRequest returned while the identity was being switched")
	case <-time.After(100 * time.Millisecond):
	}
	identityMu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tokenForRequest did not resume after the switch completed")
	}
}

// N14: a token file is replaced atomically: a reader sees the previous or the
// new content, never a truncated file, and no temporary file is left behind.
func TestTokenFileWritesAreAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kiro-auth-token.json")
	if err := writeFileAtomic(path, []byte(`{"accessToken":"one"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte(`{"accessToken":"two"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != `{"accessToken":"two"}` {
		t.Fatalf("content after replace: %q %v", data, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("temporary files left behind: %v", names)
	}
}

// N15: a zero HTTP timeout would let a stuck identity provider hold the
// failover lock for good; the configuration replaces it with the default.
func TestZeroHTTPTimeoutIsReplacedByTheDefault(t *testing.T) {
	cfg := *config.Get()
	cfg.Network.HTTPTimeout = 0
	withConfig(t, &cfg)
	if got := config.Get().Network.HTTPTimeout; got <= 0 {
		t.Fatalf("HTTPTimeout after Set: %v", got)
	}
}
