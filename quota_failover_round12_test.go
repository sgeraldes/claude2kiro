package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/profile"
	"github.com/sgeraldes/claude2kiro/internal/tokenfile"
)

// Eleventh verification pass of the quota failover: a 402 marks the login
// that made the request; a renewed token the file could not take is kept
// next to it and moved in by the next writer or reader, and the spent
// refresh token is never sent again.

// N34: a login Y lands in the slot while X's request is out; the late 402
// of X marks X's login, not Y's, and Y serves the next request.
func TestALate402MarksTheLoginThatMadeTheRequest(t *testing.T) {
	for _, stream := range []bool{true, false} {
		name := map[bool]string{true: "stream", false: "non-stream"}[stream]
		t.Run(name, func(t *testing.T) {
			x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "Social", ProfileArn: "arn:X", ExpiresAt: farFuture(), LoginID: "login-x"}
			b := TokenData{AccessToken: "b-token", AuthMethod: "IdC", ProfileArn: "arn:b", ExpiresAt: farFuture()}
			withIdentities(t, map[string]TokenData{"": x, "b": b})
			backend := newFakeQuotaBackend(t, "user-X", "b-token")
			landed := make(chan struct{})
			backend.hold = landed
			cfg := *config.Get()
			cfg.Advanced.CodeWhispererEndpoint = backend.server.URL
			cfg.Auth.FallbackProfiles = []string{"b"}
			withConfig(t, &cfg)

			go func() {
				backend.waitForRequests(t, 1) // X's request is out, its 402 held
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
			// the failover reconsiders the slot at once: it holds another
			// login now, and Y serves this very request
			assertFallbackAnswer(t, body)
			bearers, _ := backend.seen()
			if bearers[len(bearers)-1] != "user-Y" {
				t.Fatalf("login Y must serve: %v", bearers)
			}
			identityMu.Lock()
			key := exhaustedIdentities[profile.Name()]
			identityMu.Unlock()
			if key != "" {
				t.Fatalf("the slot stayed marked out of credits with key %q", key)
			}
			rec, status := nonStreamOnce(t)
			if status != http.StatusOK {
				t.Fatalf("next request: status %d: %s", status, rec.Body.String())
			}
		})
	}
}

// N33: a refresh whose write fails (a program holds the file for longer
// than the writer waits) keeps the renewed token next to the file; the next
// request moves it in and serves it, and the spent refresh token is never
// sent to the provider again.
func TestARenewedTokenTheFileCouldNotTakeIsKeptAndMovedInLater(t *testing.T) {
	for _, method := range []string{"IdC", "Social"} {
		t.Run(method, func(t *testing.T) {
			x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: method, ClientIdHash: "x", ProfileArn: "arn:primary", ExpiresAt: farFuture(), LoginID: "login-x"}
			home := withIdentities(t, map[string]TokenData{"": x})
			withIdCRefresh(t, home, "unused")
			var refreshes []string
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					RefreshToken string `json:"refreshToken"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				refreshes = append(refreshes, req.RefreshToken)
				if req.RefreshToken != "rx" {
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, `{"error":"invalid_grant"}`)
					return
				}
				io.WriteString(w, `{"accessToken":"fresh-X","refreshToken":"rotated-X","expiresIn":3600}`)
			}))
			t.Cleanup(provider.Close)
			cfg := *config.Get()
			cfg.Advanced.SSOOIDCTokenEndpoint = provider.URL
			cfg.Advanced.KiroRefreshEndpoint = provider.URL
			withConfig(t, &cfg)

			// a program holds the file without delete sharing for longer than
			// the writer waits
			f, err := os.Open(identityFile(""))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(identityFile("")+".probe", []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := tokenfile.Replace(identityFile("")+".probe", identityFile("")); err == nil {
				_ = f.Close()
				writeIdentityToken(t, "", x)
				t.Skip("this platform replaces a file that is held open; nothing to keep")
			}
			_ = os.Remove(identityFile("") + ".probe")
			err = renewToken(true)
			_ = f.Close()
			if err == nil {
				t.Fatal("the refresh must report that the file could not be replaced")
			}
			if !strings.Contains(err.Error(), "kept in") {
				t.Fatalf("the error must say the renewed token is kept: %v", err)
			}
			if _, err := os.Stat(tokenfile.RenewedPath(identityFile(""))); err != nil {
				t.Fatal("the renewed token was not kept next to the file")
			}
			if disk := readIdentityToken(t, ""); disk.AccessToken != "user-X" {
				t.Fatalf("disk: %+v", disk)
			}
			// the next request reads the renewed token, moved into place
			if tok, err := getToken(); err != nil || tok.AccessToken != "fresh-X" || tok.RefreshToken != "rotated-X" {
				t.Fatalf("the renewed token must serve: %+v %v", tok, err)
			}
			if disk := readIdentityToken(t, ""); disk.AccessToken != "fresh-X" || disk.LoginID != "login-x" {
				t.Fatalf("disk after the move: %+v", disk)
			}
			if _, err := os.Stat(tokenfile.RenewedPath(identityFile(""))); err == nil {
				t.Fatal("the renewed token file was left behind")
			}
			if len(refreshes) != 1 {
				t.Fatalf("the spent refresh token was sent again: %v", refreshes)
			}
		})
	}
}

// N33: a refresh that finds a renewed token waiting moves it in instead of
// spending the file's refresh token; a login or a logout discards it.
func TestAWaitingRenewedTokenIsUsedByTheNextRefreshAndDiscardedByALogin(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "Social", ProfileArn: "arn:primary", ExpiresAt: farFuture(), LoginID: "login-x"}
	withIdentities(t, map[string]TokenData{"": x})
	renewed := TokenData{AccessToken: "fresh-X", RefreshToken: "rotated-X", AuthMethod: "Social", ProfileArn: "arn:primary", ExpiresAt: farFuture(), LoginID: "login-x"}
	if err := keepRenewed(identityFile(""), x, renewed); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"invalid_grant"}`)
	}))
	t.Cleanup(provider.Close)
	cfg := *config.Get()
	cfg.Advanced.KiroRefreshEndpoint = provider.URL
	withConfig(t, &cfg)

	// a forced refresh: the waiting token goes in, the provider is not asked
	if err := renewToken(true); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("the file's spent refresh token was sent to the provider")
	}
	if disk := readIdentityToken(t, ""); disk.AccessToken != "fresh-X" {
		t.Fatalf("disk: %+v", disk)
	}
	// a 403 recovery with a waiting token adopts it the same way
	fresh := readIdentityToken(t, "")
	if err := keepRenewed(identityFile(""), fresh, TokenData{AccessToken: "fresh-X2", RefreshToken: "rotated-X2", AuthMethod: "Social", ProfileArn: "arn:primary", ExpiresAt: farFuture(), LoginID: "login-x"}); err != nil {
		t.Fatal(err)
	}
	budget := map[string]bool{}
	got, _, moved, err := recoverFromInvalidBearer(currentIdentity(), fresh, budget, false)
	if err != nil || moved || got.AccessToken != "fresh-X2" || calls.Load() != 0 {
		t.Fatalf("recovery: %+v moved=%v err=%v calls=%d", got, moved, err, calls.Load())
	}
	if len(budget) != 0 {
		t.Fatalf("a promoted token is adopted, not this request's refresh: %v", budget)
	}
	// a renewed token that replaces other credentials than the file's is
	// stale: discarded, not moved in
	if err := keepRenewed(identityFile(""), x, TokenData{AccessToken: "stale", RefreshToken: "stale", AuthMethod: "Social", LoginID: "login-x"}); err != nil {
		t.Fatal(err)
	}
	if tok, err := getToken(); err != nil || tok.AccessToken != "fresh-X2" {
		t.Fatalf("a stale renewed token was served: %+v %v", tok, err)
	}
	if _, err := os.Stat(tokenfile.RenewedPath(identityFile(""))); err == nil {
		t.Fatal("a stale renewed token was left behind")
	}
	// a login discards a waiting renewed token
	if err := keepRenewed(identityFile(""), readIdentityToken(t, ""), TokenData{AccessToken: "fresh-X3", RefreshToken: "rotated-X3", AuthMethod: "Social", LoginID: "login-x"}); err != nil {
		t.Fatal(err)
	}
	y := TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: "Social", ExpiresAt: farFuture()}
	if err := saveToken(&y); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tokenfile.RenewedPath(identityFile(""))); err == nil {
		t.Fatal("a login must discard the renewed token waiting for the file")
	}
	if tok, err := getToken(); err != nil || tok.AccessToken != "user-Y" {
		t.Fatalf("%+v %v", tok, err)
	}
	// and so does a logout
	if err := keepRenewed(identityFile(""), readIdentityToken(t, ""), TokenData{AccessToken: "fresh-Y", RefreshToken: "ry2", AuthMethod: "Social", LoginID: readIdentityToken(t, "").LoginID}); err != nil {
		t.Fatal(err)
	}
	logout()
	if _, err := os.Stat(tokenfile.RenewedPath(identityFile(""))); err == nil {
		t.Fatal("a logout must discard the renewed token waiting for the file")
	}
	if _, err := getToken(); err == nil {
		t.Fatal("credentials served after the logout")
	}
}
