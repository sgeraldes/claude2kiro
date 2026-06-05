package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestReadKiroProfileArn_FromFile(t *testing.T) {
	tmpDir := t.TempDir()

	data, _ := json.Marshal(map[string]string{
		"arn":  "arn:aws:codewhisperer:us-east-1:123456789:profile/TESTPROFILE",
		"name": "TestProfile",
	})

	// Create the expected path structures for all platforms
	paths := []string{
		filepath.Join(tmpDir, "Library", "Application Support", "Kiro", "User", "globalStorage", "kiro.kiroagent"),
		filepath.Join(tmpDir, ".config", "Kiro", "User", "globalStorage", "kiro.kiroagent"),
		filepath.Join(tmpDir, "AppData", "Roaming", "Kiro", "User", "globalStorage", "kiro.kiroagent"),
	}
	for _, p := range paths {
		os.MkdirAll(p, 0755)
		os.WriteFile(filepath.Join(p, "profile.json"), data, 0644)
	}

	// Override HOME so readKiroProfileArn finds our temp path
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	origUserProfile := os.Getenv("USERPROFILE")
	os.Setenv("USERPROFILE", tmpDir)
	defer func() {
		os.Setenv("HOME", origHome)
		os.Setenv("USERPROFILE", origUserProfile)
	}()

	got := readKiroProfileArn()
	expected := "arn:aws:codewhisperer:us-east-1:123456789:profile/TESTPROFILE"
	if got != expected {
		t.Errorf("readKiroProfileArn() = %q, want %q", got, expected)
	}
}

func TestFetchProfileArnFromAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ListAvailableProfiles" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]string{
				{"arn": "arn:aws:codewhisperer:us-east-1:111222333:profile/DISCOVERED"},
			},
		})
	}))
	defer server.Close()

	got := fetchProfileArnFromEndpoints("test-token", []string{server.URL + "/ListAvailableProfiles"})
	expected := "arn:aws:codewhisperer:us-east-1:111222333:profile/DISCOVERED"
	if got != expected {
		t.Errorf("fetchProfileArnFromEndpoints() = %q, want %q", got, expected)
	}
}

func TestFetchProfileArnFromAPI_MultiProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]string{
				{"arn": "arn:aws:codewhisperer:us-east-1:999999:profile/ZEBRA"},
				{"arn": "arn:aws:codewhisperer:us-east-1:111111:profile/ALPHA"},
				{"arn": "arn:aws:codewhisperer:us-east-1:555555:profile/MIDDLE"},
			},
		})
	}))
	defer server.Close()

	got := fetchProfileArnFromEndpoints("token", []string{server.URL + "/ListAvailableProfiles"})
	expected := "arn:aws:codewhisperer:us-east-1:111111:profile/ALPHA"
	if got != expected {
		t.Errorf("fetchProfileArnFromEndpoints() = %q, want %q", got, expected)
	}
}

func TestBuildCodeWhispererRequest_ProfileArn(t *testing.T) {
	tests := []struct {
		name       string
		token      TokenData
		wantArnSet bool
		wantArn    string
	}{
		{
			name:       "uses token profileArn when set",
			token:      TokenData{ProfileArn: "arn:aws:codewhisperer:us-east-1:999:profile/CUSTOM"},
			wantArnSet: true,
			wantArn:    "arn:aws:codewhisperer:us-east-1:999:profile/CUSTOM",
		},
		{
			name:       "falls back to consumer ARN when empty",
			token:      TokenData{},
			wantArnSet: true,
			wantArn:    "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRVQMUK",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := AnthropicRequest{
				Model:    "claude-opus-4-8",
				Messages: []AnthropicRequestMessage{{Role: "user", Content: "test"}},
			}
			cwReq := buildCodeWhispererRequest(req, tt.token)
			if cwReq.ProfileArn != tt.wantArn {
				t.Errorf("ProfileArn = %q, want %q", cwReq.ProfileArn, tt.wantArn)
			}
		})
	}
}
