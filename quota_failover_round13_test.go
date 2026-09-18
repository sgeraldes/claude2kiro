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
	"github.com/sgeraldes/claude2kiro/internal/tokenfile"
)

// Twelfth verification pass of the quota failover: a renewed record names
// the credentials it replaces and is applied only over those, once; a
// record that cannot be read is an error, not an absence; a promoted token
// is adopted, not this request's refresh.

// N35: a renewed record for X (no login id) is not applied over a login Y
// (no login id either) that a writer outside this proxy put in the file.
func TestARenewedRecordIsNotAppliedOverAnotherLogin(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "Social", ProfileArn: "arn:X", ExpiresAt: farFuture()}
	withIdentities(t, map[string]TokenData{"": x})
	renewed := TokenData{AccessToken: "fresh-X", RefreshToken: "rotated-X", AuthMethod: "Social", ProfileArn: "arn:X", ExpiresAt: farFuture()}
	if err := keepRenewed(identityFile(""), x, renewed); err != nil {
		t.Fatal(err)
	}
	// a writer that takes no lock (Kiro itself) puts Y in the file
	y := TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: "Social", ProfileArn: "arn:Y", ExpiresAt: farFuture()}
	writeIdentityToken(t, "", y)
	if tok, err := getToken(); err != nil || tok.AccessToken != "user-Y" {
		t.Fatalf("Y must serve, the record was for X: %+v %v", tok, err)
	}
	if disk := readIdentityToken(t, ""); disk.AccessToken != "user-Y" {
		t.Fatalf("Y was overwritten: %+v", disk)
	}
	if _, err := os.Stat(tokenfile.RenewedPath(identityFile(""))); err == nil {
		t.Fatal("a stale record was left behind")
	}
}

// N38: a record already applied only needs removing; a later rotation of
// the file is never overwritten by it.
func TestARenewedRecordIsAppliedOnceAndNeverOverALaterRotation(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "Social", ProfileArn: "arn:X", ExpiresAt: farFuture(), LoginID: "login-x"}
	withIdentities(t, map[string]TokenData{"": x})
	x2 := TokenData{AccessToken: "fresh-X2", RefreshToken: "rotated-X2", AuthMethod: "Social", ProfileArn: "arn:X", ExpiresAt: farFuture(), LoginID: "login-x"}
	if err := keepRenewed(identityFile(""), x, x2); err != nil {
		t.Fatal(err)
	}
	// the record could be applied but not removed (a program held it): the
	// file holds X2 and the record is still there
	unlock, err := tokenfile.Lock(identityFile(""))
	if err != nil {
		t.Fatal(err)
	}
	got, promoted, err := promoteRenewed(identityFile(""), x)
	unlock()
	if err != nil || !promoted || got.AccessToken != "fresh-X2" {
		t.Fatalf("first promotion: %+v %v %v", got, promoted, err)
	}
	if err := keepRenewed(identityFile(""), x, x2); err != nil { // the record is back, as if never removed
		t.Fatal(err)
	}
	// a later rotation lands in the file
	x3 := TokenData{AccessToken: "fresh-X3", RefreshToken: "rotated-X3", AuthMethod: "Social", ProfileArn: "arn:X", ExpiresAt: farFuture(), LoginID: "login-x"}
	writeIdentityToken(t, "", x3)
	if tok, err := getToken(); err != nil || tok.AccessToken != "fresh-X3" {
		t.Fatalf("the later rotation must serve: %+v %v", tok, err)
	}
	if disk := readIdentityToken(t, ""); disk.RefreshToken != "rotated-X3" {
		t.Fatalf("the later rotation was overwritten by the old record: %+v", disk)
	}
	if _, err := os.Stat(tokenfile.RenewedPath(identityFile(""))); err == nil {
		t.Fatal("the old record was left behind")
	}
	// a record whose token the file already holds: removed, nothing rewritten
	if err := keepRenewed(identityFile(""), x2, x3); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(identityFile(""))
	if tok, err := getToken(); err != nil || tok.AccessToken != "fresh-X3" {
		t.Fatalf("%+v %v", tok, err)
	}
	after, _ := os.Stat(identityFile(""))
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("an already applied record rewrote the file")
	}
	if _, err := os.Stat(tokenfile.RenewedPath(identityFile(""))); err == nil {
		t.Fatal("an already applied record was left behind")
	}
}

// N37: a token promoted from a record another refresh left is adopted by
// the request, which keeps its own refresh for it: rejected, it is refreshed
// with the rotated refresh token, not retired.
func TestAPromotedTokenKeepsItsRefreshInTheRequest(t *testing.T) {
	for _, method := range []string{"IdC", "Social"} {
		t.Run(method, func(t *testing.T) {
			x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: method, ClientIdHash: "x", ProfileArn: "arn:X", ExpiresAt: farFuture(), LoginID: "login-x"}
			b := TokenData{AccessToken: "b-token", AuthMethod: "IdC", ProfileArn: "arn:b", ExpiresAt: farFuture()}
			home := withIdentities(t, map[string]TokenData{"": x, "b": b})
			withIdCRefresh(t, home, "unused")
			var refreshes []string
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					RefreshToken string `json:"refreshToken"`
				}
				_ = jsonDecodeBody(r, &req)
				refreshes = append(refreshes, req.RefreshToken)
				if req.RefreshToken != "rotated-X2" {
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, `{"error":"invalid_grant"}`)
					return
				}
				io.WriteString(w, `{"accessToken":"fresh-X3","refreshToken":"rotated-X3","expiresIn":3600}`)
			}))
			t.Cleanup(provider.Close)
			cfg := *config.Get()
			cfg.Advanced.SSOOIDCTokenEndpoint = provider.URL
			cfg.Advanced.KiroRefreshEndpoint = provider.URL
			cfg.Auth.FallbackProfiles = []string{"b"}
			withConfig(t, &cfg)

			// another refresh left X2 waiting for the file
			x2 := TokenData{AccessToken: "fresh-X2", RefreshToken: "rotated-X2", AuthMethod: method, ClientIdHash: "x", ProfileArn: "arn:X", ExpiresAt: farFuture(), LoginID: "login-x"}
			if err := keepRenewed(identityFile(""), x, x2); err != nil {
				t.Fatal(err)
			}
			ident := currentIdentity()
			budget := map[string]bool{}
			got, _, moved, err := recoverFromInvalidBearer(ident, x, budget, true)
			if err != nil || moved || got.AccessToken != "fresh-X2" || len(refreshes) != 0 {
				t.Fatalf("the waiting token must be adopted without a provider call: %+v moved=%v err=%v refreshes=%v", got, moved, err, refreshes)
			}
			// X2 rejected: its refresh is still this request's to make
			got, _, moved, err = recoverFromInvalidBearer(ident, got, budget, true)
			if err != nil || moved || got.AccessToken != "fresh-X3" || len(refreshes) != 1 || refreshes[0] != "rotated-X2" {
				t.Fatalf("X2 must be refreshed with its own refresh token: %+v moved=%v err=%v refreshes=%v", got, moved, err, refreshes)
			}
			identityMu.Lock()
			reason := identityFailures[ident.Name]
			identityMu.Unlock()
			if reason != "" {
				t.Fatalf("retired: %q", reason)
			}
		})
	}
}

// N36: a renewed record that exists but cannot be read is an error for the
// refresh, the recovery and the reader; the file's spent refresh token is
// never sent to the provider, and the identity is not retired.
func TestAnUnreadableRenewedRecordStopsTheRefresh(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "Social", ProfileArn: "arn:X", ExpiresAt: farFuture(), LoginID: "login-x"}
	b := TokenData{AccessToken: "b-token", AuthMethod: "IdC", ProfileArn: "arn:b", ExpiresAt: farFuture()}
	withIdentities(t, map[string]TokenData{"": x, "b": b})
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"invalid_grant"}`)
	}))
	t.Cleanup(provider.Close)
	cfg := *config.Get()
	cfg.Advanced.KiroRefreshEndpoint = provider.URL
	cfg.Auth.FallbackProfiles = []string{"b"}
	withConfig(t, &cfg)
	x2 := TokenData{AccessToken: "fresh-X2", RefreshToken: "rotated-X2", AuthMethod: "Social", ProfileArn: "arn:X", ExpiresAt: farFuture(), LoginID: "login-x"}
	if err := keepRenewed(identityFile(""), x, x2); err != nil {
		t.Fatal(err)
	}
	release := holdFileUnreadable(t, tokenfile.RenewedPath(identityFile("")))
	if release == nil {
		t.Skip("this platform cannot make a file unreadable by holding it")
	}
	if err := renewToken(true); err == nil || !strings.Contains(err.Error(), "cannot be read right now") {
		t.Fatalf("the refresh must stop on the unreadable record: %v", err)
	}
	ident := currentIdentity()
	if _, _, _, err := recoverFromInvalidBearer(ident, x, map[string]bool{}, true); err == nil || !strings.Contains(err.Error(), "cannot be read right now") {
		t.Fatalf("the recovery must stop on the unreadable record: %v", err)
	}
	if _, err := getToken(); err == nil || !strings.Contains(err.Error(), "cannot be read right now") {
		t.Fatalf("the reader must stop on the unreadable record: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("the spent refresh token was sent to the provider")
	}
	identityMu.Lock()
	reason := identityFailures[ident.Name]
	identityMu.Unlock()
	if reason != "" {
		t.Fatalf("retired: %q", reason)
	}
	release()
	// readable again: the record is applied
	if tok, err := getToken(); err != nil || tok.AccessToken != "fresh-X2" {
		t.Fatalf("after the hold: %+v %v", tok, err)
	}
	if calls.Load() != 0 {
		t.Fatal("the provider was called")
	}
}

// A damaged record (truncated, no token) has nothing to apply: removed, and
// the refresh goes on with the file.
func TestADamagedRenewedRecordIsDiscarded(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "Social", ProfileArn: "arn:X", ExpiresAt: farFuture(), LoginID: "login-x"}
	withIdentities(t, map[string]TokenData{"": x})
	for _, body := range []string{"", "{", `{"from":{},"token":{}}`} {
		if err := os.WriteFile(tokenfile.RenewedPath(identityFile("")), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if tok, err := getToken(); err != nil || tok.AccessToken != "user-X" {
			t.Fatalf("record %q: %+v %v", body, tok, err)
		}
		if _, err := os.Stat(tokenfile.RenewedPath(identityFile(""))); err == nil {
			t.Fatalf("record %q was left behind", body)
		}
	}
}

func jsonDecodeBody(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}
