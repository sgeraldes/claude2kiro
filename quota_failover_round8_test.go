package main

import (
	"bytes"
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

	"github.com/sgeraldes/claude2kiro/cmd"
	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/tokenfile"
)

// Seventh verification pass of the quota failover: every writer of a token
// file takes a lock shared across processes, and a refresh holds it through
// its HTTP call. A login or a logout that arrives during a refresh, in this
// process or another, waits for the refresh's write and then wins: the
// refresh publishes nothing over it.

// N23: a login Y that arrives while a refresh of X waits for the provider
// ends on disk and in the cache, from this process or from a child.
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
				var loginDone func() error
				if child {
					loginDone = startChildLogin(t, y)
				} else {
					ch := make(chan error, 1)
					go func() { ch <- saveToken(&y) }()
					loginDone = func() error { return <-ch }
				}
				// the login waits for the refresh's lock: X is still there
				time.Sleep(300 * time.Millisecond)
				if disk := readIdentityToken(t, ""); disk.AccessToken != "user-X" {
					t.Fatalf("the login did not wait for the refresh in flight: %+v", disk)
				}
				close(release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if err := loginDone(); err != nil {
					t.Fatal(err)
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

// N20: a logout that arrives while a refresh waits for the provider is
// final: it waits for the refresh's write, removes the file, and nothing is
// published or served afterwards. From this process and from a child.
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
				var logoutDone func() error
				if child {
					// a `claude2kiro logout` in another process
					logoutDone = startChildLogout(t)
				} else {
					ch := make(chan error, 1)
					go func() { logoutCmd(); ch <- nil }()
					logoutDone = func() error { return <-ch }
				}
				// the logout waits for the refresh's lock: X is still there
				time.Sleep(300 * time.Millisecond)
				if disk := readIdentityToken(t, ""); disk.AccessToken != "user-X" {
					t.Fatalf("the logout did not wait for the refresh in flight: %+v", disk)
				}
				close(release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if err := logoutDone(); err != nil {
					t.Fatal(err)
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
	if _, ok := mergeProfileArn("", identityFile(""), x, "arn:X"); ok {
		t.Fatal("the merge wrote over a removed file")
	}
	if _, err := os.Stat(identityFile("")); err == nil {
		t.Fatal("the token file was recreated")
	}
}

// The file lock is taken and released, a second holder waits for the
// first, and one that waits past its deadline gets an error, never the lock.
func TestTokenFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kiro-auth-token.json")
	unlock, err := tokenfile.Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatal("lock file not created")
	}
	// a contender that gives up before the holder is done
	if _, err := tokenfile.LockFor(path, 100*time.Millisecond); !errors.Is(err, tokenfile.ErrBusy) {
		t.Fatalf("a second holder got the lock, or another error: %v", err)
	}
	started := time.Now()
	second := make(chan error, 1)
	go func() {
		u, err := tokenfile.Lock(path)
		if err == nil {
			u()
		}
		second <- err
	}()
	time.Sleep(200 * time.Millisecond)
	unlock()
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if time.Since(started) < 150*time.Millisecond {
		t.Fatal("the second holder did not wait for the first")
	}
	// the lock file is only ever a lock: an old one is not an obstacle
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path+".lock", old, old); err != nil {
		t.Fatal(err)
	}
	u, err := tokenfile.Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	u()
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
	if err := startChildLogin(t, tok)(); err != nil {
		t.Fatal(err)
	}
}

// startChildLogin starts the child login and returns the function that
// waits for it.
func startChildLogin(t *testing.T, tok TokenData) func() error {
	t.Helper()
	data, err := json.Marshal(tok)
	if err != nil {
		t.Fatal(err)
	}
	return startChildHelper(t, "^TestChildLoginHelper$", "CLAUDE2KIRO_TEST_CHILD_LOGIN="+string(data), "child-login-done")
}

// startChildLogout runs `logout` in a child process and returns the
// function that waits for it.
func startChildLogout(t *testing.T) func() error {
	t.Helper()
	return startChildHelper(t, "^TestChildLogoutHelper$", "CLAUDE2KIRO_TEST_CHILD_LOGOUT=1", "child-logout-done")
}

// startChildRefresh runs a forced refresh in a child process and returns
// the function that waits for it.
func startChildRefresh(t *testing.T) func() error {
	t.Helper()
	return startChildHelper(t, "^TestChildRefreshHelper$", "CLAUDE2KIRO_TEST_CHILD_REFRESH="+config.Get().Advanced.KiroRefreshEndpoint, "child-refresh-done")
}

func startChildHelper(t *testing.T, run, env, marker string) func() error {
	t.Helper()
	if os.Getenv("CLAUDE2KIRO_TEST_CHILD_LOGIN") != "" || os.Getenv("CLAUDE2KIRO_TEST_CHILD_LOGOUT") != "" || os.Getenv("CLAUDE2KIRO_TEST_CHILD_REFRESH") != "" {
		t.Skip("child")
	}
	cmd := exec.Command(os.Args[0], "-test.run", run)
	cmd.Env = append(os.Environ(), env, "HOME="+os.Getenv("HOME"), "USERPROFILE="+os.Getenv("USERPROFILE"))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return func() error {
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("child %s: %v\n%s", marker, err, out.String())
		}
		if !strings.Contains(out.String(), marker) {
			return fmt.Errorf("child %s did not run:\n%s", marker, out.String())
		}
		return nil
	}
}

// TestChildLogoutHelper is the body of the child logout above: the `logout`
// command's operation.
func TestChildLogoutHelper(t *testing.T) {
	if os.Getenv("CLAUDE2KIRO_TEST_CHILD_LOGOUT") == "" {
		t.Skip("not a child")
	}
	if _, _, err := cmd.RemoveLogin(cmd.LoginFiles()); err != nil {
		t.Fatal(err)
	}
	fmt.Println("child-logout-done")
}

// TestChildRefreshHelper is the body of the child refresh above: the
// `refresh` command's operation against the parent's fake provider.
func TestChildRefreshHelper(t *testing.T) {
	endpoint := os.Getenv("CLAUDE2KIRO_TEST_CHILD_REFRESH")
	if endpoint == "" {
		t.Skip("not a child")
	}
	cfg := *config.Get()
	cfg.Advanced.SSOOIDCTokenEndpoint = endpoint
	cfg.Advanced.KiroRefreshEndpoint = endpoint
	withConfig(t, &cfg)
	if err := renewToken(true); err != nil {
		t.Fatal(err)
	}
	fmt.Println("child-refresh-done")
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
