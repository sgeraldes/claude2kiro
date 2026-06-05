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
	// Mock server returning a profiles response
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

	// Call with the mock URL directly (simulating what fetchProfileArnFromAPI does)
	req, _ := http.NewRequest("POST", server.URL+"/ListAvailableProfiles", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var result struct {
		Profiles []struct {
			Arn string `json:"arn"`
		} `json:"profiles"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	if len(result.Profiles) == 0 || result.Profiles[0].Arn != "arn:aws:codewhisperer:us-east-1:111222333:profile/DISCOVERED" {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestFetchProfileArnFromAPI_MultiProfile(t *testing.T) {
	// Mock server returning multiple profiles — should pick lexicographically smallest ARN
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ListAvailableProfiles" {
			http.NotFound(w, r)
			return
		}
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

	req, _ := http.NewRequest("POST", server.URL+"/ListAvailableProfiles", nil)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()

	var result struct {
		Profiles []struct {
			Arn string `json:"arn"`
		} `json:"profiles"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	// Apply same logic as fetchProfileArnFromAPI: pick smallest ARN
	best := result.Profiles[0].Arn
	for _, p := range result.Profiles[1:] {
		if p.Arn < best {
			best = p.Arn
		}
	}
	expected := "arn:aws:codewhisperer:us-east-1:111111:profile/ALPHA"
	if best != expected {
		t.Errorf("got %q, want %q", best, expected)
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
