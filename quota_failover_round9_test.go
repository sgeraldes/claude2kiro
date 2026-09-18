package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/profile"
	"github.com/sgeraldes/claude2kiro/internal/tokenfile"
)

// Eighth verification pass of the quota failover: the `refresh` and
// `logout` commands are the proxy's own writers; the token file lock is the
// operating system's, held through a refresh's HTTP call, and a writer that
// cannot take it writes nothing; a refresh publishes only what the file still
// holds; a bearer the file already moved past is adopted, not retired; a
// logout ends on the identity it started on.

// N20, N23 for the CLI: `refresh` run while a login or a logout arrives
// (from a child, the way a second terminal does it) leaves the login or the
// logout as the final state, for both auth methods.
func TestCLIRefreshYieldsToALoginOrALogout(t *testing.T) {
	for _, method := range []string{"IdC", "Social"} {
		for _, other := range []string{"login", "logout"} {
			t.Run(method+"/"+other, func(t *testing.T) {
				x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: method, ClientIdHash: "x", ProfileArn: "arn:primary",
					ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
				home := withIdentities(t, map[string]TokenData{"": x})
				withIdCRefresh(t, home, "unused")
				entered := make(chan struct{})
				release := make(chan struct{})
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					close(entered)
					<-release
					io.WriteString(w, `{"accessToken":"fresh-X","refreshToken":"rotated-X","expiresIn":3600}`)
				}))
				t.Cleanup(provider.Close)
				cfg := *config.Get()
				cfg.Advanced.SSOOIDCTokenEndpoint = provider.URL
				cfg.Advanced.KiroRefreshEndpoint = provider.URL
				withConfig(t, &cfg)

				done := make(chan struct{})
				go func() { refreshToken(); close(done) }() // the `refresh` command
				<-entered
				var otherDone func() error
				if other == "login" {
					otherDone = startChildLogin(t, TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: method, ProfileArn: "arn:Y", ExpiresAt: farFuture()})
				} else {
					otherDone = startChildLogout(t)
				}
				time.Sleep(300 * time.Millisecond)
				if disk := readIdentityToken(t, ""); disk.AccessToken != "user-X" {
					t.Fatalf("the %s did not wait for the refresh in flight: %+v", other, disk)
				}
				close(release)
				<-done
				if err := otherDone(); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(identityFile(""))
				switch other {
				case "login":
					if err != nil || !strings.Contains(string(data), `"user-Y"`) {
						t.Fatalf("login Y is not the final state: %s %v", data, err)
					}
					if tok, err := getToken(); err != nil || tok.AccessToken != "user-Y" {
						t.Fatalf("the next read must serve Y: %+v %v", tok, err)
					}
				case "logout":
					if err == nil {
						t.Fatalf("the refresh recreated the file after the logout: %s", data)
					}
					if tok, err := getToken(); err == nil {
						t.Fatalf("credentials served after the logout: %+v", tok)
					}
				}
			})
		}
	}
}

// N20 for the CLI: `logout` removes both files under the lock and reports
// what went; a second one says there was nothing.
func TestCLILogoutIsTheSameOperationAsTheTUI(t *testing.T) {
	withIdentities(t, map[string]TokenData{"": primaryToken()})
	configPath := getLoginConfigPath()
	if err := os.WriteFile(configPath, []byte(`{"provider":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	logout()
	for _, p := range []string{configPath, identityFile("")} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s still exists after logout", p)
		}
	}
	logout() // nothing left: not an error
}

// N24: a login in another process waits for a lock this process holds and
// writes only once it has it; one that cannot get it in time writes nothing.
func TestLoginWaitsForTheLockOfAnotherProcess(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "Social", ExpiresAt: farFuture()}
	withIdentities(t, map[string]TokenData{"": x})
	unlock, err := tokenfile.Lock(identityFile(""))
	if err != nil {
		t.Fatal(err)
	}
	loginDone := startChildLogin(t, TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: "Social", ExpiresAt: farFuture()})
	time.Sleep(500 * time.Millisecond)
	if disk := readIdentityToken(t, ""); disk.AccessToken != "user-X" {
		t.Fatalf("the child wrote while this process held the lock: %+v", disk)
	}
	unlock()
	if err := loginDone(); err != nil {
		t.Fatal(err)
	}
	if disk := readIdentityToken(t, ""); disk.AccessToken != "user-Y" {
		t.Fatalf("the child did not write after the lock was released: %+v", disk)
	}

	// a writer that gives up: nothing written, an error returned
	unlock, err = tokenfile.Lock(identityFile(""))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := tokenfile.LockFor(identityFile(""), 100*time.Millisecond); err == nil {
		t.Fatal("a second holder got the lock")
	}
}

// N26: two processes renewing the same file do it one after the other. The
// second reads what the first wrote and renews from the rotated refresh
// token, so the provider never sees the spent one.
func TestRefreshesInTwoProcessesRotateInTurn(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "Social", ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	withIdentities(t, map[string]TokenData{"": x})
	var mu sync.Mutex
	var seen []string
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refreshToken"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen = append(seen, body.RefreshToken)
		n := len(seen)
		mu.Unlock()
		if body.RefreshToken != "rx" && body.RefreshToken != "rotated-1" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		once.Do(func() { close(entered); <-release })
		fmt.Fprintf(w, `{"accessToken":"fresh-%d","refreshToken":"rotated-%d","expiresIn":3600}`, n, n)
	}))
	t.Cleanup(provider.Close)
	cfg := *config.Get()
	cfg.Advanced.KiroRefreshEndpoint = provider.URL
	withConfig(t, &cfg)

	done := make(chan error, 1)
	go func() { done <- renewToken(true) }()
	<-entered
	childDone := startChildRefresh(t)
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	calls := len(seen)
	mu.Unlock()
	if calls != 1 {
		t.Fatalf("the child renewed while this process was mid-refresh: provider saw %v", seen)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := childDone(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "rx" || got[1] != "rotated-1" {
		t.Fatalf("the second refresh must use the first one's rotated token: %v", got)
	}
	if disk := readIdentityToken(t, ""); disk.AccessToken != "fresh-2" {
		t.Fatalf("disk: %+v", disk)
	}
}

// N26: a 403 for a bearer the file no longer holds (another process logged
// in or renewed meanwhile) adopts what the file holds; the provider is not
// asked and the identity is not retired.
func TestRejectedBearerAlreadyReplacedIsAdoptedNotRetired(t *testing.T) {
	for _, method := range []string{"IdC", "Social"} {
		t.Run(method, func(t *testing.T) {
			old := TokenData{AccessToken: "old-X", RefreshToken: "rx", AuthMethod: method, ClientIdHash: "x", ProfileArn: "arn:primary", ExpiresAt: farFuture()}
			b := TokenData{AccessToken: "b-token", AuthMethod: "IdC", ProfileArn: "arn:b", ExpiresAt: farFuture()}
			home := withIdentities(t, map[string]TokenData{"": old, "b": b})
			withIdCRefresh(t, home, "unused")
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"invalid_grant"}`)
			}))
			t.Cleanup(provider.Close)
			backend := newFakeQuotaBackend(t)
			backend.rejectBearer("old-X")
			cfg := *config.Get()
			cfg.Advanced.CodeWhispererEndpoint = backend.server.URL
			cfg.Advanced.SSOOIDCTokenEndpoint = provider.URL
			cfg.Advanced.KiroRefreshEndpoint = provider.URL
			cfg.Auth.FallbackProfiles = []string{"b"}
			withConfig(t, &cfg)

			ident := currentIdentity()
			tok, err := getToken()
			if err != nil || tok.AccessToken != "old-X" {
				t.Fatalf("%+v %v", tok, err)
			}
			// another process rotated the credentials while the request was out
			fresh := TokenData{AccessToken: "fresh-X", RefreshToken: "rotated-X", AuthMethod: method, ClientIdHash: "x", ProfileArn: "arn:primary", ExpiresAt: farFuture()}
			if err := saveToken(&fresh); err != nil {
				t.Fatal(err)
			}
			refreshedFor := map[string]bool{}
			got, id, moved, err := recoverFromInvalidBearer(ident, tok, refreshedFor, true)
			if err != nil {
				t.Fatal(err)
			}
			if moved || id.Name != ident.Name || got.AccessToken != "fresh-X" {
				t.Fatalf("the replaced credentials must be adopted on the same identity: moved=%v id=%+v tok=%+v", moved, id, got)
			}
			if calls.Load() != 0 {
				t.Fatal("the provider was asked to refresh credentials that were already replaced")
			}
			if len(refreshedFor) != 0 {
				t.Fatalf("the adopted credentials must keep their one refresh: %v", refreshedFor)
			}
			identityMu.Lock()
			reason := identityFailures[ident.Name]
			identityMu.Unlock()
			if reason != "" {
				t.Fatalf("a healthy identity was retired: %q", reason)
			}
			if got := currentIdentity().Name; got != ident.Name {
				t.Fatalf("active identity moved to %q", got)
			}
		})
	}
}

// N25: a login that lands during the discovery call of a refresh is what
// the following reads serve; the refresh does not reinstall its own token.
func TestRefreshDoesNotPublishOverALaterLogin(t *testing.T) {
	for _, arn := range []string{"", "arn:X"} {
		for _, child := range []bool{false, true} {
			t.Run("arn="+arn+"/child="+map[bool]string{true: "yes", false: "no"}[child], func(t *testing.T) {
				x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "IdC", ClientIdHash: "x",
					ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
				home := withIdentities(t, map[string]TokenData{"": x})
				withIdCRefresh(t, home, "unused")
				entered := make(chan struct{})
				release := make(chan struct{})
				oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					io.WriteString(w, `{"accessToken":"fresh-X","refreshToken":"rotated-X","expiresIn":3600}`)
				}))
				t.Cleanup(oidc.Close)
				profiles := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == "fresh-X" {
						close(entered)
						<-release
						if arn == "" {
							io.WriteString(w, `{"profiles":[]}`)
							return
						}
						io.WriteString(w, `{"profiles":[{"arn":"arn:X","profileName":"p"}]}`)
						return
					}
					io.WriteString(w, `{"profiles":[{"arn":"arn:Y","profileName":"p"}]}`)
				}))
				t.Cleanup(profiles.Close)
				cfg := *config.Get()
				cfg.Advanced.SSOOIDCTokenEndpoint = oidc.URL
				cfg.Advanced.ProfilesEndpoint = profiles.URL
				withConfig(t, &cfg)

				done := make(chan error, 1)
				go func() { done <- renewToken(true) }()
				<-entered
				y := TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: "IdC", ExpiresAt: farFuture()}
				if child {
					loginFromChildProcess(t, y)
				} else if err := saveToken(&y); err != nil {
					t.Fatal(err)
				}
				close(release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				for i := range 3 {
					tok, err := getToken()
					if err != nil || tok.AccessToken != "user-Y" {
						t.Fatalf("read %d after login Y served %+v %v", i, tok, err)
					}
				}
				if disk := readIdentityToken(t, ""); disk.AccessToken != "user-Y" || disk.ProfileArn == "arn:X" {
					t.Fatalf("disk: %+v", disk)
				}
			})
		}
	}
}

// N27: a logout started on identity A while a writer is busy ends on A,
// even if a failover moved the active identity to B during the wait.
func TestLogoutEndsOnTheIdentityItStartedOn(t *testing.T) {
	a := TokenData{AccessToken: "a-token", AuthMethod: "IdC", ProfileArn: "arn:a", ExpiresAt: farFuture()}
	b := TokenData{AccessToken: "b-token", AuthMethod: "IdC", ProfileArn: "arn:b", ExpiresAt: farFuture()}
	withIdentities(t, map[string]TokenData{"": a, "b": b})
	cfg := *config.Get()
	cfg.Auth.FallbackProfiles = []string{"b"}
	withConfig(t, &cfg)

	tokenRefreshMutex.Lock() // a writer in flight
	done := make(chan struct{})
	go func() { logoutCmd(); close(done) }()
	time.Sleep(200 * time.Millisecond)
	// a failover lands while the logout waits
	if err := profile.SwitchTo("b"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = profile.SwitchTo(profile.Name()) })
	tokenRefreshMutex.Unlock()
	<-done
	if _, err := os.Stat(identityFile("")); err == nil {
		t.Fatal("A, the identity the logout started on, still has its file")
	}
	if _, err := os.Stat(identityFile("b")); err != nil {
		t.Fatal("B, a reserve the logout never targeted, lost its file")
	}
}

// A writer that cannot take the lock writes nothing and says so: login,
// refresh, discovery merge and logout alike.
func TestWritersFailWithoutTheLock(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "IdC", ExpiresAt: farFuture()}
	withIdentities(t, map[string]TokenData{"": x})
	unlock, err := tokenfile.Lock(identityFile(""))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := tokenfile.LockFor(identityFile(""), 50*time.Millisecond); err == nil {
		t.Fatal("a second holder got the lock")
	}
	// the merge (discovery) path under a held lock: it does not proceed
	// without it, so the caller must not see a merge
	before := readIdentityToken(t, "")
	if _, ok := publishFromFileFor(t, identityFile(""), before, "arn:X", 50*time.Millisecond); ok {
		t.Fatal("the merge published without the lock")
	}
	if after := readIdentityToken(t, ""); after.ProfileArn != "" {
		t.Fatalf("the merge wrote without the lock: %+v", after)
	}
}

// publishFromFileFor is publishFromFile with a short lock wait, for tests
// that hold the lock themselves.
func publishFromFileFor(t *testing.T, tokenPath string, expect TokenData, arn string, wait time.Duration) (TokenData, bool) {
	t.Helper()
	unlock, err := tokenfile.LockFor(tokenPath, wait)
	if err != nil {
		return TokenData{}, false
	}
	defer unlock()
	current, err := readTokenFile(tokenPath)
	if err != nil || !sameCredentials(current, expect) {
		return TokenData{}, false
	}
	if arn != "" && current.ProfileArn == "" {
		current.ProfileArn = arn
		if err := writeTokenLocked(tokenPath, &current); err != nil {
			return TokenData{}, false
		}
	}
	publishWrittenToken("", current)
	return current, true
}

// A token file replaced by another process is noticed on the next request,
// not at the cache TTL: a hit is checked against the file's content.
func TestCacheNoticesAFileWrittenByAnotherProcess(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "Social", ExpiresAt: farFuture()}
	withIdentities(t, map[string]TokenData{"": x})
	if tok, err := getToken(); err != nil || tok.AccessToken != "user-X" {
		t.Fatalf("%+v %v", tok, err)
	}
	loginFromChildProcess(t, TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: "Social", ExpiresAt: farFuture()})
	if tok, err := getToken(); err != nil || tok.AccessToken != "user-Y" {
		t.Fatalf("the read after a child login served %+v %v", tok, err)
	}
	if err := startChildLogout(t)(); err != nil {
		t.Fatal(err)
	}
	if tok, err := getToken(); err == nil {
		t.Fatalf("credentials served after a child logout: %+v", tok)
	}
}
