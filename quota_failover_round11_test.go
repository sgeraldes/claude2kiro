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
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/profile"
)

// Tenth verification pass of the quota failover: the refresh budget of a
// request follows the credentials it refreshed, not the identity; a login
// carries an id that its refreshes keep, so an exhausted or retired slot is
// looked at again only for another login; a cache hit whose file cannot be
// read is a miss; readers share delete access and writers wait out a
// reader's hold.

// N30: a login Y lands between the refresh of X and the token the handler
// gets back (during X's discovery call). Y is handed out with its own
// refresh still available: rejected, it is refreshed, not retired.
func TestLoginDuringTheRefreshDiscoveryKeepsItsRefresh(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "IdC", ClientIdHash: "x", ExpiresAt: farFuture()}
	b := TokenData{AccessToken: "b-token", AuthMethod: "IdC", ProfileArn: "arn:b", ExpiresAt: farFuture()}
	home := withIdentities(t, map[string]TokenData{"": x, "b": b})
	withIdCRefresh(t, home, "unused")
	var refreshes []string
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refreshToken"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		refreshes = append(refreshes, body.RefreshToken)
		io.WriteString(w, `{"accessToken":"fresh-`+body.RefreshToken+`","refreshToken":"rotated-`+body.RefreshToken+`","expiresIn":3600}`)
	}))
	t.Cleanup(oidc.Close)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once atomic.Bool
	profiles := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if once.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		io.WriteString(w, `{"profiles":[]}`)
	}))
	t.Cleanup(profiles.Close)
	cfg := *config.Get()
	cfg.Advanced.SSOOIDCTokenEndpoint = oidc.URL
	cfg.Advanced.ProfilesEndpoint = profiles.URL
	cfg.Auth.FallbackProfiles = []string{"b"}
	withConfig(t, &cfg)

	ident := currentIdentity()
	refreshed := map[string]bool{}
	type result struct {
		tok   TokenData
		moved bool
		err   error
	}
	done := make(chan result, 1)
	go func() {
		tok, _, moved, err := recoverFromInvalidBearer(ident, x, refreshed, true)
		done <- result{tok, moved, err}
	}()
	<-entered // X was refreshed and written; its discovery is out
	y := TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: "IdC", ClientIdHash: "x", ProfileArn: "arn:Y", ExpiresAt: farFuture()}
	loginFromChildProcess(t, y)
	close(release)
	r := <-done
	if r.err != nil || r.moved || r.tok.AccessToken != "user-Y" {
		t.Fatalf("the handler must get login Y: %+v moved=%v err=%v", r.tok, r.moved, r.err)
	}
	// Y rejected: its own refresh, not a retirement
	got, _, moved, err := recoverFromInvalidBearer(ident, r.tok, refreshed, true)
	if err != nil || moved || got.AccessToken != "fresh-ry" {
		t.Fatalf("Y must be refreshed: %+v moved=%v err=%v refreshes=%v", got, moved, err, refreshes)
	}
	identityMu.Lock()
	reason := identityFailures[ident.Name]
	identityMu.Unlock()
	if reason != "" {
		t.Fatalf("login Y was retired: %q", reason)
	}
	// fresh-Y rejected too: now the identity is retired (its refresh was used)
	_, id, moved, err := recoverFromInvalidBearer(ident, got, refreshed, true)
	if err != nil || !moved || id.Name != "b" {
		t.Fatalf("after Y's refresh the identity must be retired: id=%+v moved=%v err=%v", id, moved, err)
	}
}

// N32: a slot out of credits is looked at again when another login (another
// account) takes it; a refresh of the exhausted login is not another login.
func TestAnExhaustedSlotServesAgainWithAnotherLogin(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "Social", ProfileArn: "arn:X", ExpiresAt: farFuture(), LoginID: "login-x"}
	b := TokenData{AccessToken: "b-token", AuthMethod: "IdC", ProfileArn: "arn:b", ExpiresAt: farFuture()}
	withIdentities(t, map[string]TokenData{"": x, "b": b})
	backend := newFakeQuotaBackend(t, "user-X", "b-token", "rotated-X")
	cfg := *config.Get()
	cfg.Advanced.CodeWhispererEndpoint = backend.server.URL
	cfg.Auth.FallbackProfiles = []string{"b"}
	withConfig(t, &cfg)

	rec, _ := nonStreamOnce(t)
	if !strings.Contains(rec.Body.String(), "every configured identity is unavailable") {
		t.Fatalf("expected every identity out of credits, got: %s", rec.Body.String())
	}
	// a rotation of the same login: still out of credits, not even asked
	rotated := TokenData{AccessToken: "rotated-X", RefreshToken: "rx2", AuthMethod: "Social", ProfileArn: "arn:X", ExpiresAt: farFuture(), LoginID: "login-x"}
	writeIdentityToken(t, "", rotated)
	before, _ := backend.seen()
	rec, _ = nonStreamOnce(t)
	if !strings.Contains(rec.Body.String(), "every configured identity is unavailable") {
		t.Fatalf("a rotation must not restore credits: %s", rec.Body.String())
	}
	after, _ := backend.seen()
	for _, bearer := range after[len(before):] {
		if bearer == "rotated-X" {
			t.Fatalf("the rotated token of the exhausted login was tried: %v", after)
		}
	}
	// another login in the slot (a child `claude2kiro login`): served
	y := TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: "Social", ProfileArn: "arn:Y", ExpiresAt: farFuture()}
	loginFromChildProcess(t, y)
	rec, status := nonStreamOnce(t)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, rec.Body.String())
	}
	assertFallbackAnswer(t, rec.Body.String())
	bearers, _ := backend.seen()
	if bearers[len(bearers)-1] != "user-Y" {
		t.Fatalf("the new login must serve: %v", bearers)
	}
	if got := currentIdentity().Name; got != profile.Name() {
		t.Fatalf("active identity: %q", got)
	}
}

// A login gets an id, a refresh keeps it, and an id-less file (written by
// Kiro itself) is told apart by its access token.
func TestLoginIDsFollowTheLogin(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "Social", ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	withIdentities(t, map[string]TokenData{"": x})
	if loginKey(x) != "user-X" {
		t.Fatalf("an id-less token is known by its access token: %q", loginKey(x))
	}
	if err := saveToken(&x); err != nil {
		t.Fatal(err)
	}
	saved := readIdentityToken(t, "")
	if saved.LoginID == "" || loginKey(saved) != saved.LoginID {
		t.Fatalf("a login must get an id: %+v", saved)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"accessToken":"fresh-X","refreshToken":"rotated-X","expiresIn":3600}`)
	}))
	t.Cleanup(provider.Close)
	cfg := *config.Get()
	cfg.Advanced.KiroRefreshEndpoint = provider.URL
	withConfig(t, &cfg)
	if err := renewToken(true); err != nil {
		t.Fatal(err)
	}
	if fresh := readIdentityToken(t, ""); fresh.AccessToken != "fresh-X" || fresh.LoginID != saved.LoginID {
		t.Fatalf("a refresh must keep the login id: %+v", fresh)
	}
	y := TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: "Social", ExpiresAt: farFuture()}
	if err := saveToken(&y); err != nil {
		t.Fatal(err)
	}
	if again := readIdentityToken(t, ""); again.LoginID == "" || again.LoginID == saved.LoginID {
		t.Fatalf("a new login must get its own id: %+v", again)
	}
}

// N33: a reader of this proxy never blocks a login or a refresh in another
// process: the token file is read with delete sharing.
func TestReadersDoNotBlockAnotherProcessWriting(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "Social", ExpiresAt: farFuture()}
	withIdentities(t, map[string]TokenData{"": x})
	stop := make(chan struct{})
	readerDone := make(chan int)
	go func() {
		n := 0
		for {
			select {
			case <-stop:
				readerDone <- n
				return
			default:
				_, _ = readTokenFile(identityFile(""))
				n++
			}
		}
	}()
	for i := range 5 {
		y := TokenData{AccessToken: "user-Y" + string(rune('0'+i)), RefreshToken: "ry", AuthMethod: "Social", ExpiresAt: farFuture()}
		if err := startChildLogin(t, y)(); err != nil {
			t.Fatalf("login %d under a reader: %v", i, err)
		}
	}
	close(stop)
	if n := <-readerDone; n == 0 {
		t.Fatal("the reader did not run")
	}
	if disk := readIdentityToken(t, ""); disk.AccessToken != "user-Y4" {
		t.Fatalf("disk: %+v", disk)
	}
}

// N33: a writer waits out a short hold of the file by a reader that does
// not share delete access (an editor, Kiro itself) instead of failing.
func TestAWriterWaitsOutAReadersHold(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "Social", ExpiresAt: farFuture()}
	withIdentities(t, map[string]TokenData{"": x})
	f, err := os.Open(identityFile(""))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = f.Close()
	}()
	y := TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: "Social", ExpiresAt: farFuture()}
	started := time.Now()
	if err := saveToken(&y); err != nil {
		t.Fatalf("the login failed instead of waiting for the reader: %v (after %s)", err, time.Since(started))
	}
	if disk := readIdentityToken(t, ""); disk.AccessToken != "user-Y" {
		t.Fatalf("disk: %+v", disk)
	}
}
