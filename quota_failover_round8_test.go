package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/tokenfile"
)

// Seventh verification pass of the quota failover: every writer of a token
// file re-reads it under a lock shared across processes and writes only
// over the credentials it started from. A login that lands during a refresh
// or a discovery, in this process or another, wins; a logout is final.

// N23: a refresh started from X does not write over a login Y that landed
// while the provider was answering, from this process or from a child.
func TestRefreshDoesNotOverwriteALoginThatLandedMeanwhile(t *testing.T) {
	for _, method := range []string{"IdC", "Social"} {
		for _, child := range []bool{false, true} {
			t.Run(method+"/child="+map[bool]string{true: "yes", false: "no"}[child], func(t *testing.T) {
				x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: method, ClientIdHash: "x", ProfileArn: "arn:primary",
					ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
				home := withIdentities(t, map[string]TokenData{"": x})
				reg := filepath.Join(home, ".aws", "sso", "cache", "x.json")
				if err := os.WriteFile(reg, []byte(`{"clientId":"c","clientSecret":"s"}`), 0o600); err != nil {
					t.Fatal(err)
				}
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

				done := make(chan error, 1)
				go func() { done <- renewToken(true) }()
				<-entered
				y := TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: method, ProfileArn: "arn:Y", ExpiresAt: farFuture()}
				if child {
					loginFromChildProcess(t, y)
				} else if err := saveToken(&y); err != nil {
					t.Fatal(err)
				}
				close(release)
				if err := <-done; err == nil {
					t.Fatal("the refresh must report that the file changed hands")
				}
				if disk := readIdentityToken(t, ""); disk.AccessToken != "user-Y" || disk.RefreshToken != "ry" {
					t.Fatalf("login Y was overwritten by the refresh of X: %+v", disk)
				}
				if tok, err := getToken(); err != nil || tok.AccessToken != "user-Y" {
					t.Fatalf("the next read must serve Y: %+v %v", tok, err)
				}
			})
		}
	}
}

// N19: a discovery started with X's bearer does not attach its ARN to a
// login Y that landed meanwhile, even from a child process.
func TestDiscoveryDoesNotAttachItsArnToAChildProcessLogin(t *testing.T) {
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
	loginFromChildProcess(t, TokenData{AccessToken: "user-Y", RefreshToken: "ry", AuthMethod: "IdC", ExpiresAt: farFuture()})
	close(release)
	reader := <-done
	if reader.AccessToken == "user-Y" && reader.ProfileArn == "arn:X" {
		t.Fatalf("login Y was handed login X's ARN: %+v", reader)
	}
	if disk := readIdentityToken(t, ""); disk.AccessToken != "user-Y" || disk.ProfileArn == "arn:X" {
		t.Fatalf("disk: %+v", disk)
	}
	if tok, _ := getToken(); tok.AccessToken != "user-Y" || tok.ProfileArn != "arn:Y" {
		t.Fatalf("the next read must discover with Y's bearer: %+v", tok)
	}
}

// N20: a logout that lands while a refresh waits for the provider is final:
// the refresh writes nothing and nothing is served afterwards.
func TestRefreshDoesNotUndoALogout(t *testing.T) {
	for _, method := range []string{"IdC", "Social"} {
		for _, child := range []bool{false, true} {
			t.Run(method+"/other-process="+map[bool]string{true: "yes", false: "no"}[child], func(t *testing.T) {
				x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: method, ClientIdHash: "x", ProfileArn: "arn:primary",
					ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
				home := withIdentities(t, map[string]TokenData{"": x})
				reg := filepath.Join(home, ".aws", "sso", "cache", "x.json")
				if err := os.WriteFile(reg, []byte(`{"clientId":"c","clientSecret":"s"}`), 0o600); err != nil {
					t.Fatal(err)
				}
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

				done := make(chan error, 1)
				go func() { done <- renewToken(true) }()
				<-entered
				if child {
					// a `claude2kiro logout` in another process: the file goes while
					// the provider is still answering
					if err := os.Remove(identityFile("")); err != nil {
						t.Fatal(err)
					}
					close(release)
					if err := <-done; err == nil {
						t.Fatal("the refresh must report that the file is gone")
					}
				} else {
					logoutDone := make(chan struct{})
					go func() { logoutCmd(); close(logoutDone) }()
					// the logout waits for the refresh's mutex: release the provider
					// so the refresh finishes, then the logout runs
					close(release)
					<-done
					<-logoutDone
				}
				if _, err := os.Stat(identityFile("")); err == nil {
					t.Fatal("the token file exists after the logout")
				}
				if tok, err := getToken(); err == nil {
					t.Fatalf("credentials served after the logout: %+v", tok)
				}
			})
		}
	}
}

// N20 from another process: a `claude2kiro logout` run elsewhere while a
// discovery merge is about to write sees the file gone and writes nothing.
func TestMergeDoesNotRecreateAFileAnotherProcessRemoved(t *testing.T) {
	x := TokenData{AccessToken: "user-X", RefreshToken: "rx", AuthMethod: "IdC", ExpiresAt: farFuture()}
	withIdentities(t, map[string]TokenData{"": x})
	// the file is removed between the reader's read and its merge, the way
	// another process's logout would do it
	if err := os.Remove(identityFile("")); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := mergeProfileArn(identityFile(""), x, "arn:X"); ok {
		t.Fatal("the merge wrote over a removed file")
	}
	if _, err := os.Stat(identityFile("")); err == nil {
		t.Fatal("the token file was recreated")
	}
}

// The file lock is taken and released, waits for another holder, and
// expires when a holder died with it.
func TestTokenFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kiro-auth-token.json")
	unlock := tokenfile.Lock(path)
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatal("lock file not created")
	}
	started := time.Now()
	second := make(chan struct{})
	go func() { u := tokenfile.Lock(path); u(); close(second) }()
	time.Sleep(200 * time.Millisecond)
	unlock()
	<-second
	if time.Since(started) < 150*time.Millisecond {
		t.Fatal("the second holder did not wait for the first")
	}
	if _, err := os.Stat(path + ".lock"); err == nil {
		t.Fatal("lock file left behind")
	}
	// a stale lock from a dead process
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(path+".lock", old, old); err != nil {
		t.Fatal(err)
	}
	u := tokenfile.Lock(path)
	u()
	if _, err := os.Stat(path + ".lock"); err == nil {
		t.Fatal("a stale lock was not taken over and released")
	}
}

// Observation of the sixth pass: success and failure no longer compete; the
// first terminal result of a login is the only one delivered.
func TestLoginOutcomeIsSettledOnce(t *testing.T) {
	for _, first := range []string{"code", "error"} {
		codeChan := make(chan string, 1)
		errChan := make(chan error, 1)
		o := &loginOutcome{}
		deliver := loginDeliverer(o, codeChan)
		fail := loginFailer(o, errChan)
		if first == "code" {
			deliver("code-1")
			fail(errors.New("late"))
		} else {
			fail(errors.New("first"))
			deliver("late-code")
		}
		gotCode := ""
		var gotErr error
		select {
		case gotCode = <-codeChan:
		default:
		}
		select {
		case gotErr = <-errChan:
		default:
		}
		if first == "code" && (gotCode != "code-1" || gotErr != nil) {
			t.Fatalf("code first: code=%q err=%v", gotCode, gotErr)
		}
		if first == "error" && (gotErr == nil || gotCode != "") {
			t.Fatalf("error first: code=%q err=%v", gotCode, gotErr)
		}
	}
}

// loginFromChildProcess writes tok to the primary identity's file the way a
// `claude2kiro login` in another process does: through the real saveToken,
// in a child of the test binary.
func loginFromChildProcess(t *testing.T, tok TokenData) {
	t.Helper()
	if os.Getenv("CLAUDE2KIRO_TEST_CHILD_LOGIN") != "" {
		t.Skip("child")
	}
	data, err := json.Marshal(tok)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^TestChildLoginHelper$")
	cmd.Env = append(os.Environ(), "CLAUDE2KIRO_TEST_CHILD_LOGIN="+string(data), "HOME="+os.Getenv("HOME"), "USERPROFILE="+os.Getenv("USERPROFILE"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child login: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "child-login-done") {
		t.Fatalf("child login did not run:\n%s", out)
	}
}

// TestChildLoginHelper is the body of the child process above.
func TestChildLoginHelper(t *testing.T) {
	raw := os.Getenv("CLAUDE2KIRO_TEST_CHILD_LOGIN")
	if raw == "" {
		t.Skip("not a child")
	}
	var tok TokenData
	if err := json.Unmarshal([]byte(raw), &tok); err != nil {
		t.Fatal(err)
	}
	if err := saveToken(&tok); err != nil {
		t.Fatal(err)
	}
	fmt.Println("child-login-done")
}
