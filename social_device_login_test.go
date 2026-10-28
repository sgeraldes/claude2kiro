package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
