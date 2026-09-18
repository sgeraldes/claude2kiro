package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/profile"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// Third verification pass of the quota failover: a token read that overlapped
// a refresh is repeated instead of published, the IDE's profile is trusted
// only with authenticated membership, and a reserve whose refresh is rejected
// does not hide the next healthy one.

// H03/R01: a reader parked in discovery with the old bearer must not put that
// bearer back in the cache after a refresh replaced it, even when its
// discovery fails (no merge happens on that path).
func TestStaleReaderDoesNotRepublishAReplacedToken(t *testing.T) {
	old := TokenData{AccessToken: "old-token", RefreshToken: "old-refresh", AuthMethod: "IdC", ClientIdHash: "x",
		ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}
	home := withIdentities(t, map[string]TokenData{"": old})
	withIdCRefresh(t, home, "fresh-token")
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

	done := make(chan TokenData, 1)
	go func() { tok, _ := getToken(); done <- tok }()
	<-entered
	if err := tryRefreshToken(); err != nil {
		t.Fatal(err)
	}
	close(release)
	reader := <-done

	if reader.AccessToken != "fresh-token" {
		t.Fatalf("the parked reader returned the replaced bearer %q", reader.AccessToken)
	}
	if tok, _ := getToken(); tok.AccessToken != "fresh-token" || tok.ProfileArn != "arn:refreshed" {
		t.Fatalf("cache after the stale reader: %+v", tok)
	}
	if disk := readIdentityToken(t, ""); disk.AccessToken != "fresh-token" {
		t.Fatalf("disk: %+v", disk)
	}
}

// R03: the IDE's stored profile is accepted only when the API, asked with this
// bearer, lists it. An empty list or an API error leaves the ARN unresolved;
// it is never persisted on the strength of the IDE file alone.
func TestIDEProfileNeedsAuthenticatedMembership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"empty list", http.StatusOK, `{"profiles":[]}`},
		{"api error", http.StatusServiceUnavailable, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			t.Cleanup(profiles.Close)
			cfg := *config.Get()
			cfg.Advanced.ProfilesEndpoint = profiles.URL
			withConfig(t, &cfg)

			tok, err := getToken()
			if err != nil {
				t.Fatal(err)
			}
			if tok.ProfileArn != "" {
				t.Fatalf("an unverifiable IDE profile was adopted: %q", tok.ProfileArn)
			}
			if disk := readIdentityToken(t, ""); disk.ProfileArn != "" {
				t.Fatalf("an unverifiable IDE profile was persisted: %q", disk.ProfileArn)
			}
		})
	}
}

// N06: the first reserve holds an expired token whose refresh is rejected
// (revoked login). The switch must move on to the next reserve instead of
// handing the caller a bearer that can only answer 403.
func TestRevokedReserveDoesNotHideAHealthyReserve(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(map[bool]string{true: "stream", false: "non-stream"}[stream], func(t *testing.T) {
			bad := TokenData{AccessToken: "bad-stale", RefreshToken: "bad-refresh", AuthMethod: "IdC", ClientIdHash: "x",
				ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
			good := TokenData{AccessToken: "good-token", AuthMethod: "IdC", ProfileArn: "arn:good", ExpiresAt: farFuture()}
			home := withIdentities(t, map[string]TokenData{"": primaryToken(), "bad": bad, "good": good})
			reg := filepath.Join(home, ".aws", "sso", "cache", "x.json")
			if err := os.WriteFile(reg, []byte(`{"clientId":"c","clientSecret":"s"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body), "bad-refresh") {
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, `{"error":"invalid_grant"}`)
					return
				}
				io.WriteString(w, `{"accessToken":"unexpected","refreshToken":"r","expiresIn":3600}`)
			}))
			t.Cleanup(oidc.Close)
			backend := newFakeQuotaBackend(t, "primary-token")
			cfg := *config.Get()
			cfg.Advanced.CodeWhispererEndpoint = backend.server.URL
			cfg.Advanced.SSOOIDCTokenEndpoint = oidc.URL
			cfg.Auth.FallbackProfiles = []string{"bad", "good"}
			withConfig(t, &cfg)

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
			bearers, arns := backend.seen()
			if len(bearers) != 2 || bearers[1] != "good-token" || arns[1] != "arn:good" {
				t.Fatalf("the healthy reserve must serve the retry: bearers %v arns %v", bearers, arns)
			}
			if strings.Contains(body, "bad-stale") || strings.Contains(body, "invalid_grant") {
				t.Fatalf("the revoked reserve leaked into the answer:\n%s", body)
			}
			assertFallbackAnswer(t, body)
			if got := currentIdentity().Name; got != "good" {
				t.Fatalf("active identity after the switch: %q", got)
			}
			identityMu.Lock()
			reason := identityFailures["bad"]
			identityMu.Unlock()
			if !strings.Contains(reason, "refresh failed") {
				t.Fatalf("the revoked reserve must be remembered with its reason, got %q", reason)
			}
		})
	}
}

// N06, the other way round: with every reserve unusable the error names each
// one with its reason, so the operator knows which login to redo.
func TestExhaustedMessageNamesEveryUnusableIdentity(t *testing.T) {
	bad := TokenData{AccessToken: "bad-stale", RefreshToken: "bad-refresh", AuthMethod: "IdC", ClientIdHash: "x",
		ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	home := withIdentities(t, map[string]TokenData{"": primaryToken(), "bad": bad})
	reg := filepath.Join(home, ".aws", "sso", "cache", "x.json")
	if err := os.WriteFile(reg, []byte(`{"clientId":"c","clientSecret":"s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"invalid_grant"}`)
	}))
	t.Cleanup(oidc.Close)
	cfg := *config.Get()
	cfg.Advanced.SSOOIDCTokenEndpoint = oidc.URL
	cfg.Auth.FallbackProfiles = []string{"bad"}
	withConfig(t, &cfg)

	_, _, err := switchToFallbackIdentity(currentIdentity())
	if err == nil {
		t.Fatal("expected an error with nothing usable left")
	}
	msg := err.Error()
	for _, want := range []string{primaryLabel() + " (out of credits)", "bad (refresh failed"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error must name %q, got: %s", want, msg)
		}
	}
	if got := currentIdentity().Name; got != profile.Name() {
		t.Fatalf("the proxy must stay on the identity that failed, got %q", got)
	}
}

// N07 companion: the handlers read the token themselves, so a handler test
// with no token on disk sends nothing and says so, rather than depending on
// whatever the machine running the tests has logged in.
func TestHandlersWithoutAnyTokenOnDiskSendNothing(t *testing.T) {
	withIdentities(t, map[string]TokenData{})
	backend := newFakeQuotaBackend(t)
	failoverConfig(t, backend.server.URL)

	rec := httptest.NewRecorder()
	handleStreamRequestWithLogger(rec, testRequest(), logger.NewLogger(50), "sess", "req", nil)
	rec2 := httptest.NewRecorder()
	status := handleNonStreamRequest(rec2, testRequest(), nil, "sess", "req")

	if bearers, _ := backend.seen(); len(bearers) != 0 {
		t.Fatalf("nothing must reach the backend without a token: %v", bearers)
	}
	if !strings.Contains(rec.Body.String(), "Token unavailable") || status != http.StatusInternalServerError {
		t.Fatalf("stream: %s\nnon-stream status: %d", rec.Body.String(), status)
	}
}
