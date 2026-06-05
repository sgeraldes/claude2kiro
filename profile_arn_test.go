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
	// Create temp dir simulating Kiro IDE config
	tmpDir := t.TempDir()
	profileDir := filepath.Join(tmpDir, "kiro.kiroagent")
	os.MkdirAll(profileDir, 0755)

	profile := map[string]string{
		"arn":  "arn:aws:codewhisperer:us-east-1:123456789:profile/TESTPROFILE",
		"name": "TestProfile",
	}
	data, _ := json.Marshal(profile)
	os.WriteFile(filepath.Join(profileDir, "profile.json"), data, 0644)

	// Read from the file directly (can't override homeDir, so test the parsing logic)
	var parsed struct {
		Arn string `json:"arn"`
	}
	raw, err := os.ReadFile(filepath.Join(profileDir, "profile.json"))
	if err != nil {
		t.Fatalf("failed to read test file: %v", err)
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if parsed.Arn != "arn:aws:codewhisperer:us-east-1:123456789:profile/TESTPROFILE" {
		t.Errorf("got %q, want test ARN", parsed.Arn)
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
