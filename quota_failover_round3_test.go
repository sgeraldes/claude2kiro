package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/models"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// Second verification pass of the quota failover: the request must be built
// from the (token, identity) pair, disk writes from discovery and refresh must
// merge, and everything cached per identity is dropped on a switch.

// withIdCRefresh points the IdC refresh at a local OIDC server and drops the
// client registration file the refresh reads.
func withIdCRefresh(t *testing.T, home, accessToken string) {
	t.Helper()
	reg := filepath.Join(home, ".aws", "sso", "cache", "x.json")
	if err := os.WriteFile(reg, []byte(`{"clientId":"c","clientSecret":"s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"accessToken":"`+accessToken+`","refreshToken":"fresh-refresh","expiresIn":3600}`)
	}))
	t.Cleanup(oidc.Close)
	cfg := *config.Get()
	cfg.Advanced.SSOOIDCTokenEndpoint = oidc.URL
	withConfig(t, &cfg)
}

// N01: the caller hands the handler a token of the previous identity (it was
// read before another request failed over). The first request must already go
// out with the current identity's bearer and profileArn.
func TestFirstRequestUsesTheCurrentIdentityNotTheCallersToken(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(map[bool]string{true: "stream", false: "non-stream"}[stream], func(t *testing.T) {
			withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": fallbackToken()})
			backend := newFakeQuotaBackend(t, "primary-token")
			failoverConfig(t, backend.server.URL, "kiro2")

			stale, err := getToken() // primary
			if err != nil {
				t.Fatal(err)
			}
			if _, next, err := switchToFallbackIdentity(currentIdentity()); err != nil || next.Name != "kiro2" {
				t.Fatalf("switch: %v %+v", err, next)
			}

			rec := httptest.NewRecorder()
			if stream {
				handleStreamRequestWithLogger(rec, testRequest(), stale, logger.NewLogger(50), "sess", "req", nil)
			} else {
				handleNonStreamRequest(rec, testRequest(), stale, nil, "sess", "req")
			}
			bearers, arns := backend.seen()
			if len(bearers) != 1 || bearers[0] != "kiro2-token" || arns[0] != "arn:kiro2" {
				t.Fatalf("first request must carry the current pair: bearers %v arns %v", bearers, arns)
			}
			assertFallbackAnswer(t, rec.Body.String())
			identityMu.Lock()
			exhausted := exhaustedIdentities["kiro2"]
			identityMu.Unlock()
			if exhausted {
				t.Fatal("the healthy fallback was marked exhausted")
			}
		})
	}
}

// N02: with no readable token for the active identity nothing is sent.
func TestUnreadableActiveTokenSendsNothing(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken()})
	backend := newFakeQuotaBackend(t)
	failoverConfig(t, backend.server.URL)
	stale, err := getToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFilePathFor(""), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalidateTokenCache()

	rec := httptest.NewRecorder()
	handleStreamRequestWithLogger(rec, testRequest(), stale, logger.NewLogger(50), "sess", "req", nil)
	rec2 := httptest.NewRecorder()
	status := handleNonStreamRequest(rec2, testRequest(), stale, nil, "sess", "req")

	if bearers, _ := backend.seen(); len(bearers) != 0 {
		t.Fatalf("nothing must reach the backend without a valid token: %v", bearers)
	}
	if !strings.Contains(rec.Body.String(), "Token unavailable") || status != http.StatusInternalServerError {
		t.Fatalf("stream body %q, non-stream status %d", rec.Body.String(), status)
	}
}

// N03: an expired IdC reserve without profileArn is refreshed on switch and
// then resolves its ARN with the fresh bearer before the retry.
func TestExpiredIdCFallbackResolvesItsArnAfterRefresh(t *testing.T) {
	stale := TokenData{AccessToken: "kiro2-stale", RefreshToken: "r", AuthMethod: "IdC", ClientIdHash: "x",
		ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	home := withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": stale})
	withIdCRefresh(t, home, "kiro2-token")
	var mu sync.Mutex
	var profileBearers []string
	profiles := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		profileBearers = append(profileBearers, b)
		mu.Unlock()
		if b != "kiro2-token" {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, invalidBearer)
			return
		}
		io.WriteString(w, `{"profiles":[{"arn":"arn:kiro2","profileName":"p"}]}`)
	}))
	t.Cleanup(profiles.Close)
	backend := newFakeQuotaBackend(t, "primary-token")
	cfg := *config.Get()
	cfg.Advanced.CodeWhispererEndpoint = backend.server.URL
	cfg.Advanced.ProfilesEndpoint = profiles.URL
	cfg.Auth.FallbackProfiles = []string{"kiro2"}
	withConfig(t, &cfg)

	rec := streamOnce(t)

	bearers, arns := backend.seen()
	if len(bearers) != 2 || bearers[1] != "kiro2-token" || arns[1] != "arn:kiro2" {
		t.Fatalf("retry must carry the refreshed bearer and its own ARN: bearers %v arns %v", bearers, arns)
	}
	mu.Lock()
	pb := append([]string(nil), profileBearers...)
	mu.Unlock()
	if len(pb) == 0 || pb[len(pb)-1] != "kiro2-token" {
		t.Fatalf("discovery must run with the fresh bearer: %v", pb)
	}
	assertFallbackAnswer(t, rec.Body.String())
	if got := readIdentityToken(t, "kiro2"); got.AccessToken != "kiro2-token" || got.ProfileArn != "arn:kiro2" {
		t.Fatalf("persisted reserve: %+v", got)
	}
}

// N04: four transient failures, then a 403 from the old identity that arrives
// after the switch: the request still goes out once more with the fallback.
func TestLate403OnTheLastAttemptStillAdoptsTheFallback(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": fallbackToken()})
	var mu sync.Mutex
	var bearers []string
	hold := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		bearers = append(bearers, b)
		n := len(bearers)
		mu.Unlock()
		switch {
		case n <= 4:
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"message":"unavailable"}`)
		case n == 5:
			<-hold
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, invalidBearer)
		default:
			w.WriteHeader(http.StatusOK)
			w.Write(cwFrame(`{"content":"` + fallbackContent + `"}`))
		}
	}))
	t.Cleanup(backend.Close)
	failoverConfig(t, backend.URL, "kiro2")

	ident := currentIdentity()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- streamOnce(t) }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		mu.Lock()
		n := len(bearers)
		mu.Unlock()
		if n >= 5 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, next, err := switchToFallbackIdentity(ident); err != nil || next.Name != "kiro2" {
		t.Fatalf("switch: %v %+v", err, next)
	}
	close(hold)
	rec := <-done

	mu.Lock()
	got := append([]string(nil), bearers...)
	mu.Unlock()
	if len(got) != 6 || got[5] != "kiro2-token" {
		t.Fatalf("expected a sixth request on the fallback: %v", got)
	}
	assertFallbackAnswer(t, rec.Body.String())
}

// R01: a refresh that lands while a discovery for the same identity is in
// flight must keep its new tokens on disk; discovery only adds the ARN.
func TestDiscoveryDoesNotRestoreTokensARefreshReplaced(t *testing.T) {
	old := TokenData{AccessToken: "old-token", RefreshToken: "old-refresh", AuthMethod: "IdC", ClientIdHash: "x",
		ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}
	home := withIdentities(t, map[string]TokenData{"": old})
	withIdCRefresh(t, home, "fresh-token")
	// Discovery with the old bearer is parked until the test releases it; the
	// refreshed bearer resolves at once (that is the discovery the refresh runs).
	releaseDiscovery := make(chan struct{})
	profiles := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == "old-token" {
			<-releaseDiscovery
			io.WriteString(w, `{"profiles":[{"arn":"arn:stale-discovery","profileName":"p"}]}`)
			return
		}
		io.WriteString(w, `{"profiles":[{"arn":"arn:refreshed","profileName":"p"}]}`)
	}))
	t.Cleanup(profiles.Close)
	cfg := *config.Get()
	cfg.Advanced.ProfilesEndpoint = profiles.URL
	withConfig(t, &cfg)

	// Discovery for the old token is parked; the refresh runs to completion.
	done := make(chan TokenData, 1)
	go func() { tok, _ := getToken(); done <- tok }()
	time.Sleep(50 * time.Millisecond)
	if err := tryRefreshToken(); err != nil {
		t.Fatal(err)
	}
	close(releaseDiscovery)
	<-done

	disk := readIdentityToken(t, "")
	if disk.AccessToken != "fresh-token" || disk.RefreshToken != "fresh-refresh" {
		t.Fatalf("discovery restored old credentials: %+v", disk)
	}
	if disk.ProfileArn != "arn:refreshed" {
		t.Fatalf("the ARN resolved with the fresh bearer must survive the stale discovery: %+v", disk)
	}
	if tok, _ := getToken(); tok.AccessToken != "fresh-token" {
		t.Fatalf("cache: %q", tok.AccessToken)
	}
}

// R03: for the launched identity the IDE's stored profile is used only when
// the API, asked with this bearer, lists it.
func TestLaunchedIdentityDoesNotTrustAnIDEProfileTheAPIDoesNotList(t *testing.T) {
	primary := primaryToken()
	primary.ProfileArn = ""
	home := withIdentities(t, map[string]TokenData{"": primary})
	for _, ide := range kiroProfileFilePaths() {
		if !strings.HasPrefix(ide, home) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(ide), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ide, []byte(`{"arn":"arn:someone-else"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	profiles := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"profiles":[{"arn":"arn:primary","profileName":"p"}]}`)
	}))
	t.Cleanup(profiles.Close)
	cfg := *config.Get()
	cfg.Advanced.ProfilesEndpoint = profiles.URL
	withConfig(t, &cfg)

	tok, err := getToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok.ProfileArn != "arn:primary" {
		t.Fatalf("ARN must come from the API when the IDE's is not listed for this bearer: %q", tok.ProfileArn)
	}
}

// R04: the model catalog of one identity is not reused for another.
func TestCatalogIsInvalidatedOnIdentitySwitch(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": fallbackToken()})
	backend := newFakeQuotaBackend(t, "primary-token")
	failoverConfig(t, backend.server.URL, "kiro2")
	var fetches int32
	seeded := models.NewCatalog(10*time.Minute, func() ([]models.KiroModel, error) {
		fetches++
		return []models.KiroModel{{ModelID: "only-A"}}, nil
	})
	seeded.Warm()
	orig := modelCatalog
	modelCatalog = seeded
	t.Cleanup(func() { modelCatalog = orig })
	modelCatalog.Models()
	if fetches != 1 {
		t.Fatalf("precondition: one fetch, got %d", fetches)
	}

	if _, _, err := switchToFallbackIdentity(currentIdentity()); err != nil {
		t.Fatal(err)
	}
	modelCatalog.Models()
	if fetches != 2 {
		t.Fatalf("the catalog must be refetched for the new identity, fetches=%d", fetches)
	}
}
