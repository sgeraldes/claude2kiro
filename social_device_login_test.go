package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// pollServer answers /oauth/device/poll with the given bodies in order and
// records when each poll arrived.
func pollServer(t *testing.T, bodies ...string) (*httptest.Server, func() []time.Time) {
	t.Helper()
	var mu sync.Mutex
	var at []time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/device/poll" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		n := len(at)
		at = append(at, time.Now())
		mu.Unlock()
		if n >= len(bodies) {
			n = len(bodies) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodies[n]))
	}))
	t.Cleanup(server.Close)
	return server, func() []time.Time {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Time(nil), at...)
	}
}

func TestAwaitSocialDeviceTokenKeepsPollingUntilAuthorized(t *testing.T) {
	old := deviceSlowDownStep
	deviceSlowDownStep = 150 * time.Millisecond
	t.Cleanup(func() { deviceSlowDownStep = old })

	server, polls := pollServer(t,
		`{"accessToken":null,"refreshToken":null,"status":"authorization_pending"}`,
		`{"accessToken":null,"refreshToken":null,"status":"slow_down"}`,
		`{"accessToken":null,"refreshToken":null,"status":"slow_down"}`,
		`{"accessToken":"fake-access","refreshToken":"fake-refresh","profileArn":"arn:aws:codewhisperer:us-east-1:000000000000:profile/FAKE","status":"COMPLETE","expiresIn":1800}`,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	before := time.Now()
	token, err := awaitSocialDeviceToken(ctx, server.Client(), server.URL, "device-1", "Google", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("awaitSocialDeviceToken: %v", err)
	}
	if token.AccessToken != "fake-access" || token.RefreshToken != "fake-refresh" {
		t.Fatalf("token not taken from the poll response: %+v", token)
	}
	if token.AuthMethod != "social" || token.Provider != "Google" || !strings.HasSuffix(token.ProfileArn, "profile/FAKE") {
		t.Fatalf("token metadata = %+v", token)
	}
	expiresAt, err := time.Parse(time.RFC3339, token.ExpiresAt)
	if err != nil {
		t.Fatalf("ExpiresAt %q: %v", token.ExpiresAt, err)
	}
	if d := expiresAt.Sub(before); d < 29*time.Minute || d > 31*time.Minute {
		t.Fatalf("ExpiresAt is %v after the start, want about 30m from expiresIn", d)
	}

	at := polls()
	if len(at) != 4 {
		t.Fatalf("polled %d times, want 4", len(at))
	}
	// Each slow_down adds the step again: the gap after the second one is
	// at least two steps, not one.
	if gap := at[3].Sub(at[2]); gap < 2*deviceSlowDownStep {
		t.Fatalf("gap after the second slow_down = %v, want at least %v", gap, 2*deviceSlowDownStep)
	}
}

func TestAwaitSocialDeviceTokenStopsOnTerminalStatus(t *testing.T) {
	server, _ := pollServer(t, `{"accessToken":null,"refreshToken":null,"status":"expired_token"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := awaitSocialDeviceToken(ctx, server.Client(), server.URL, "device-1", "Github", 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "expired_token") {
		t.Fatalf("err = %v, want one naming expired_token", err)
	}
}

func TestAwaitSocialDeviceTokenTimesOut(t *testing.T) {
	server, _ := pollServer(t, `{"accessToken":null,"refreshToken":null,"status":"authorization_pending"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := awaitSocialDeviceToken(ctx, server.Client(), server.URL, "device-1", "Google", 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
}

func TestAwaitSocialDeviceTokenDefaultsExpiry(t *testing.T) {
	server, _ := pollServer(t, `{"accessToken":"fake-access","refreshToken":"fake-refresh","status":"COMPLETE"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	before := time.Now()
	token, err := awaitSocialDeviceToken(ctx, server.Client(), server.URL, "device-1", "Google", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("awaitSocialDeviceToken: %v", err)
	}
	expiresAt, err := time.Parse(time.RFC3339, token.ExpiresAt)
	if err != nil {
		t.Fatalf("ExpiresAt %q: %v", token.ExpiresAt, err)
	}
	if d := expiresAt.Sub(before); d < 59*time.Minute || d > 61*time.Minute {
		t.Fatalf("ExpiresAt is %v after the start, want the one-hour default", d)
	}
}

func TestSocialDeviceAuthorizationAndPolling(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/device/authorization", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("authorization method = %s, want POST", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"deviceCode":"device-1",
			"expiresInMilliseconds":300000,
			"intervalInMilliseconds":5000,
			"userCode":"ABCD-EFGH",
			"verificationUri":"https://app.kiro.dev/account/device",
			"verificationUriComplete":"https://app.kiro.dev/account/device?user_code=ABCD-EFGH&login_provider=Google"
		}`))
	})
	mux.HandleFunc("/oauth/device/poll", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode poll payload: %v", err)
		}
		if payload["clientId"] != "kiro-cli" || payload["deviceCode"] != "device-1" {
			t.Fatalf("unexpected poll payload: %#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"accessToken":null,
			"refreshToken":null,
			"status":"authorization_pending"
		}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	auth, err := startSocialDeviceAuthorization(server.Client(), server.URL, "Google")
	if err != nil {
		t.Fatalf("start device authorization: %v", err)
	}
	if auth.DeviceCode != "device-1" || auth.UserCode != "ABCD-EFGH" {
		t.Fatalf("unexpected authorization response: %+v", auth)
	}
	poll, err := pollSocialDeviceAuthorization(server.Client(), server.URL, auth.DeviceCode)
	if err != nil {
		t.Fatalf("poll device authorization: %v", err)
	}
	if poll.Status != "authorization_pending" {
		t.Fatalf("poll status = %q, want authorization_pending", poll.Status)
	}
}
