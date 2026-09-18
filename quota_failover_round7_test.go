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

	"github.com/sgeraldes/claude2kiro/cmd"
	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/profile"
	"github.com/sgeraldes/claude2kiro/internal/tui"
)

// Sixth verification pass of the quota failover: a discovery belongs to the
// login it was made for, a logout is final, a rejected bearer moves the proxy
// to the next reserve between requests, and a login's outcome is delivered
// once.

// N19: a discovery started with login X's bearer must not attach X's ARN to
// login Y's credentials that replaced the file meanwhile.
func TestDiscoveryDoesNotAttachItsArnToAnotherLogin(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "IdC", ExpiresAt: farFuture()}
	withIdentities(t, map[string]TokenData{"": x})
	entered := make(chan struct{})
	release := make(chan struct{})
	profiles := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == "user-X" {
			close(entered)
			<-release
			io.WriteString(w, `{"profiles":[{"arn":"arn:X","profileName":"p"}]}`)
			return
		}
		io.WriteString(w, `{"profiles":[{"arn":"arn:Y","profileName":"p"}]}`)
	}))
	t.Cleanup(profiles.Close)
	cfg := *config.Get()
	cfg.Advanced.ProfilesEndpoint = profiles.URL
	withConfig(t, &cfg)

	done := make(chan TokenData, 1)
	go func() { tok, _ := getToken(); done <- tok }()
	<-entered
	y := TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: "IdC", ExpiresAt: farFuture()}
	if err := saveToken(&y); err != nil {
		t.Fatal(err)
	}
	close(release)
	reader := <-done

	if reader.AccessToken == "user-Y" && reader.ProfileArn == "arn:X" {
		t.Fatalf("login Y was handed login X's ARN: %+v", reader)
	}
	if disk := readIdentityToken(t, ""); disk.AccessToken != "user-Y" || disk.ProfileArn == "arn:X" {
		t.Fatalf("disk after the stale discovery: %+v", disk)
	}
	if tok, _ := getToken(); tok.AccessToken != "user-Y" || tok.ProfileArn != "arn:Y" {
		t.Fatalf("the next read must discover with Y's bearer: %+v", tok)
	}
}

// N20: a discovery that finishes after a logout does not recreate the token
// file, and nothing of the logged-out credentials is served afterwards.
func TestDiscoveryDoesNotRecreateATokenAfterLogout(t *testing.T) {
	primary := primaryToken()
	primary.ProfileArn = ""
	withIdentities(t, map[string]TokenData{"": primary})
	entered := make(chan struct{})
	release := make(chan struct{})
	profiles := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		io.WriteString(w, `{"profiles":[{"arn":"arn:primary","profileName":"p"}]}`)
	}))
	t.Cleanup(profiles.Close)
	cfg := *config.Get()
	cfg.Advanced.ProfilesEndpoint = profiles.URL
	withConfig(t, &cfg)

	done := make(chan error, 1)
	go func() { _, err := getToken(); done <- err }()
	<-entered
	if msg, ok := logoutCmd().(cmd.StatusMsg); !ok || msg.IsError {
		t.Fatalf("logout: %+v", msg)
	}
	close(release)
	<-done

	if _, err := os.Stat(identityFile("")); err == nil {
		t.Fatal("the token file was recreated after the logout")
	}
	if tok, err := getToken(); err == nil {
		t.Fatalf("credentials served after the logout: %+v", tok)
	}
}

// N21: a reserve whose bearer the backend rejects, and whose refresh the
// provider rejects, is retired on that very request and the next reserve
// serves, in both handlers and whatever its recorded expiry says.
func TestRejectedBearerMovesToTheNextReserve(t *testing.T) {
	for _, expiry := range []string{"", farFuture()} {
		for _, stream := range []bool{true, false} {
			name := map[bool]string{true: "stream", false: "non-stream"}[stream] + "/expiry=" + map[bool]string{true: "future", false: "none"}[expiry != ""]
			t.Run(name, func(t *testing.T) {
				revoked := TokenData{AccessToken: "revoked-B", RefreshToken: "b-refresh", AuthMethod: "IdC", ClientIdHash: "x", ProfileArn: "arn:b", ExpiresAt: expiry}
				good := TokenData{AccessToken: "good-token", AuthMethod: "IdC", ProfileArn: "arn:good", ExpiresAt: farFuture()}
				home := withIdentities(t, map[string]TokenData{"": primaryToken(), "b": revoked, "good": good})
				reg := filepath.Join(home, ".aws", "sso", "cache", "x.json")
				if err := os.WriteFile(reg, []byte(`{"clientId":"c","clientSecret":"s"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, `{"error":"invalid_grant"}`)
				}))
				t.Cleanup(oidc.Close)
				backend := newFakeQuotaBackend(t, "primary-token")
				backend.rejectBearer("revoked-B")
				cfg := *config.Get()
				cfg.Advanced.CodeWhispererEndpoint = backend.server.URL
				cfg.Advanced.SSOOIDCTokenEndpoint = oidc.URL
				cfg.Auth.FallbackProfiles = []string{"b", "good"}
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
				bearers, _ := backend.seen()
				if bearers[len(bearers)-1] != "good-token" {
					t.Fatalf("the healthy reserve must serve in the end: %v", bearers)
				}
				assertFallbackAnswer(t, body)
				if got := currentIdentity().Name; got != "good" {
					t.Fatalf("active identity: %q", got)
				}
				identityMu.Lock()
				reason := identityFailures["b"]
				identityMu.Unlock()
				if !strings.Contains(reason, "rejected") {
					t.Fatalf("the revoked reserve must be retired with its reason, got %q", reason)
				}
			})
		}
	}
}

// N18: repeated successful callbacks do not block the login either.
func TestLoginDelivererNeverBlocks(t *testing.T) {
	codeChan := make(chan string, 1)
	deliver := loginDeliverer(codeChan)
	done := make(chan struct{})
	go func() {
		deliver("first")
		deliver("second")
		deliver("third")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a repeated login callback blocked")
	}
	if code := <-codeChan; code != "first" {
		t.Fatalf("the first code must be the login's outcome, got %q", code)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Start()
	finished := make(chan struct{})
	go func() { shutdownLoginServer(server.Config); close(finished) }()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown of the callback server did not return")
	}
}

// N22 companion: the identity the TUI is on is what its login child gets,
// whatever the launch profile was.
func TestTUILoginEnvironmentNamesTheActiveIdentity(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": fallbackToken()})
	backend := newFakeQuotaBackend(t, "primary-token")
	failoverConfig(t, backend.server.URL, "kiro2")
	if _, next, err := switchToFallbackIdentity(currentIdentity()); err != nil || next.Name != "kiro2" {
		t.Fatalf("switch: %v %+v", err, next)
	}
	if got := profile.Active(); got != "kiro2" {
		t.Fatalf("active: %q", got)
	}
	env := tui.LoginChildEnvironment()
	var seen []string
	for _, kv := range env {
		if strings.HasPrefix(kv, profile.EnvVar+"=") {
			seen = append(seen, kv)
		}
	}
	if len(seen) != 1 || seen[0] != profile.EnvVar+"=kiro2" {
		t.Fatalf("the login child must be told the active identity once: %v", seen)
	}
}
