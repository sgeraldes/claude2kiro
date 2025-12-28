package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bestk/kiro2cc/internal/tui/dashboard"
	"github.com/bestk/kiro2cc/internal/tui/logger"
)

// TokenData represents the token file structure
type TokenData struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
	AuthMethod   string `json:"authMethod,omitempty"`
	Provider     string `json:"provider,omitempty"`
	ProfileArn   string `json:"profileArn,omitempty"`
	ClientIdHash string `json:"clientIdHash,omitempty"`
	Region       string `json:"region,omitempty"`
	StartUrl     string `json:"startUrl,omitempty"`
}

// RefreshRequest represents the token refresh request structure
type RefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// RefreshResponse represents the token refresh response structure
type RefreshResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
}

// SSOClientRegistration represents cached client registration
type SSOClientRegistration struct {
	ClientId     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	ExpiresAt    string `json:"expiresAt"`
}

// SSOCreateTokenRequest represents SSO OIDC create token request
type SSOCreateTokenRequest struct {
	ClientId     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	GrantType    string `json:"grantType"`
	RefreshToken string `json:"refreshToken,omitempty"`
}

// SSOCreateTokenResponse represents SSO OIDC create token response
type SSOCreateTokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
	TokenType    string `json:"tokenType"`
}

const kiroVersion = "0.6.0"

// GetTokenFilePath returns the cross-platform token file path
func GetTokenFilePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".aws", "sso", "cache", "kiro-auth-token.json")
}

// GetClientRegistrationPath returns the path for cached client registration
func GetClientRegistrationPath(clientIdHash string) string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".aws", "sso", "cache", clientIdHash+".json")
}

// GetToken retrieves the current token
func GetToken() (TokenData, error) {
	tokenPath := GetTokenFilePath()

	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return TokenData{}, fmt.Errorf("failed to read token file: %v", err)
	}

	var token TokenData
	if err := json.Unmarshal(data, &token); err != nil {
		return TokenData{}, fmt.Errorf("failed to parse token file: %v", err)
	}

	return token, nil
}

// GetTokenExpiry returns the token expiry time
func GetTokenExpiry() time.Time {
	token, err := GetToken()
	if err != nil {
		return time.Time{}
	}

	if token.ExpiresAt == "" {
		return time.Time{}
	}

	expiresAt, err := time.Parse(time.RFC3339, token.ExpiresAt)
	if err != nil {
		return time.Time{}
	}

	return expiresAt
}

// HasToken returns true if a valid token file exists
func HasToken() bool {
	_, err := GetToken()
	return err == nil
}

// ReadClientRegistration reads cached client registration
func ReadClientRegistration(clientIdHash string) (*SSOClientRegistration, error) {
	path := GetClientRegistrationPath(clientIdHash)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var reg SSOClientRegistration
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, err
	}
	return &reg, nil
}

// RefreshTokenIdC refreshes token using AWS SSO OIDC
func RefreshTokenIdC(currentToken TokenData) (TokenData, error) {
	clientReg, err := ReadClientRegistration(currentToken.ClientIdHash)
	if err != nil {
		return TokenData{}, fmt.Errorf("failed to read client registration: %v (try logging in again)", err)
	}

	region := currentToken.Region
	if region == "" {
		region = "us-east-1"
	}

	tokenUrl := fmt.Sprintf("https://oidc.%s.amazonaws.com/token", region)

	reqBody := SSOCreateTokenRequest{
		ClientId:     clientReg.ClientId,
		ClientSecret: clientReg.ClientSecret,
		GrantType:    "refresh_token",
		RefreshToken: currentToken.RefreshToken,
	}

	reqJson, err := json.Marshal(reqBody)
	if err != nil {
		return TokenData{}, fmt.Errorf("failed to marshal request: %v", err)
	}

	req, err := http.NewRequest("POST", tokenUrl, bytes.NewBuffer(reqJson))
	if err != nil {
		return TokenData{}, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("KiroIDE-%s-%s", kiroVersion, runtime.GOOS))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return TokenData{}, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return TokenData{}, fmt.Errorf("token refresh failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp SSOCreateTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return TokenData{}, fmt.Errorf("failed to parse response: %v", err)
	}

	expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	return TokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		AuthMethod:   currentToken.AuthMethod,
		Provider:     currentToken.Provider,
		ClientIdHash: currentToken.ClientIdHash,
		Region:       currentToken.Region,
		StartUrl:     currentToken.StartUrl,
	}, nil
}

// RefreshTokenSocial refreshes token using Kiro's social auth endpoint
func RefreshTokenSocial(currentToken TokenData) (TokenData, error) {
	refreshReq := RefreshRequest{
		RefreshToken: currentToken.RefreshToken,
	}

	reqBody, err := json.Marshal(refreshReq)
	if err != nil {
		return TokenData{}, fmt.Errorf("failed to serialize request: %v", err)
	}

	resp, err := http.Post(
		"https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken",
		"application/json",
		bytes.NewBuffer(reqBody),
	)
	if err != nil {
		return TokenData{}, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return TokenData{}, fmt.Errorf("status code: %d, response: %s", resp.StatusCode, string(body))
	}

	var refreshResp RefreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&refreshResp); err != nil {
		return TokenData{}, fmt.Errorf("failed to parse response: %v", err)
	}

	return TokenData{
		AccessToken:  refreshResp.AccessToken,
		RefreshToken: refreshResp.RefreshToken,
		ExpiresAt:    refreshResp.ExpiresAt,
		AuthMethod:   currentToken.AuthMethod,
		Provider:     currentToken.Provider,
		ProfileArn:   currentToken.ProfileArn,
	}, nil
}

// TryRefreshToken attempts to refresh the token without exiting on failure
func TryRefreshToken() error {
	tokenPath := GetTokenFilePath()

	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return fmt.Errorf("failed to read token file: %v", err)
	}

	var currentToken TokenData
	if err := json.Unmarshal(data, &currentToken); err != nil {
		return fmt.Errorf("failed to parse token file: %v", err)
	}

	var newToken TokenData

	if currentToken.AuthMethod == "IdC" {
		newToken, err = RefreshTokenIdC(currentToken)
	} else {
		newToken, err = RefreshTokenSocial(currentToken)
	}

	if err != nil {
		return err
	}

	newData, err := json.MarshalIndent(newToken, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize new token: %v", err)
	}

	if err := os.WriteFile(tokenPath, newData, 0600); err != nil {
		return fmt.Errorf("failed to write token file: %v", err)
	}

	return nil
}

// Message types for TUI commands

// LoginResultMsg carries the result of a login attempt
type LoginResultMsg struct {
	Success bool
	Err     error
}

// RefreshResultMsg carries the result of a token refresh
type RefreshResultMsg struct {
	Success   bool
	ExpiresAt time.Time
	Err       error
}

// TokenInfoMsg carries token information
type TokenInfoMsg struct {
	Token TokenData
	Err   error
}

// StatusMsg displays a temporary status message
type StatusMsg struct {
	Message string
	IsError bool
}

// TUI Command functions - these return tea.Msg directly

// LoginCmd returns a function that triggers login
// Note: Login is complex (needs browser), so for now we just return a placeholder
func LoginCmd() tea.Msg {
	// TODO: Implement browser-based login that works with TUI
	return StatusMsg{
		Message: "Login requires running 'kiro2cc login' separately for now",
		IsError: false,
	}
}

// RefreshTokenCmd returns a function that refreshes the token
func RefreshTokenCmd() tea.Msg {
	err := TryRefreshToken()
	if err != nil {
		return RefreshResultMsg{
			Success: false,
			Err:     err,
		}
	}

	return RefreshResultMsg{
		Success:   true,
		ExpiresAt: GetTokenExpiry(),
	}
}

// ViewTokenCmd returns a function that fetches token info
func ViewTokenCmd() tea.Msg {
	token, err := GetToken()
	return TokenInfoMsg{
		Token: token,
		Err:   err,
	}
}

// ExportEnvCmd returns a function that shows env export info
func ExportEnvCmd() tea.Msg {
	token, err := GetToken()
	if err != nil {
		return StatusMsg{
			Message: fmt.Sprintf("Failed to read token: %v", err),
			IsError: true,
		}
	}

	// Return instructions
	var msg string
	if runtime.GOOS == "windows" {
		msg = fmt.Sprintf("CMD: set ANTHROPIC_BASE_URL=http://localhost:8080 && set ANTHROPIC_API_KEY=%s", token.AccessToken[:20]+"...")
	} else {
		msg = fmt.Sprintf("export ANTHROPIC_BASE_URL=http://localhost:8080 ANTHROPIC_API_KEY=%s...", token.AccessToken[:20])
	}

	return StatusMsg{
		Message: msg,
		IsError: false,
	}
}

// LogoutCmd returns a function that logs out
func LogoutCmd() tea.Msg {
	configPath := filepath.Join(filepath.Dir(GetTokenFilePath()), "kiro2cc-login-config.json")
	tokenPath := GetTokenFilePath()

	os.Remove(configPath)
	os.Remove(tokenPath)

	return StatusMsg{
		Message: "Logged out successfully",
		IsError: false,
	}
}

// ConfigureClaudeCmd creates launch scripts for Claude Code with kiro2cc proxy
func ConfigureClaudeCmd() tea.Msg {
	if runtime.GOOS == "windows" {
		// Windows: put scripts next to kiro2cc executable (should be in PATH)
		exePath, err := os.Executable()
		if err != nil {
			return StatusMsg{
				Message: fmt.Sprintf("Failed to get executable path: %v", err),
				IsError: true,
			}
		}
		exeDir := filepath.Dir(exePath)

		// Create CMD batch file
		batPath := filepath.Join(exeDir, "claude-kiro.bat")
		batScript := `@echo off
set ANTHROPIC_BASE_URL=http://localhost:8080
set ANTHROPIC_API_KEY=kiro2cc
claude %*
`
		if err := os.WriteFile(batPath, []byte(batScript), 0755); err != nil {
			return StatusMsg{
				Message: fmt.Sprintf("Failed to create batch script: %v", err),
				IsError: true,
			}
		}

		// Create PowerShell script
		ps1Path := filepath.Join(exeDir, "claude-kiro.ps1")
		ps1Script := `$env:ANTHROPIC_BASE_URL = "http://localhost:8080"
$env:ANTHROPIC_API_KEY = "kiro2cc"
claude @args
`
		if err := os.WriteFile(ps1Path, []byte(ps1Script), 0755); err != nil {
			return StatusMsg{
				Message: fmt.Sprintf("Failed to create PowerShell script: %v", err),
				IsError: true,
			}
		}

		return StatusMsg{
			Message: fmt.Sprintf("Created scripts in %s", exeDir),
			IsError: false,
		}
	}

	// Unix (Linux/macOS): create shell script in ~/.local/bin
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return StatusMsg{
			Message: fmt.Sprintf("Failed to get home directory: %v", err),
			IsError: true,
		}
	}

	binDir := filepath.Join(homeDir, ".local", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return StatusMsg{
			Message: fmt.Sprintf("Failed to create ~/.local/bin: %v", err),
			IsError: true,
		}
	}

	scriptPath := filepath.Join(binDir, "claude-kiro")
	script := `#!/bin/bash
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_API_KEY=kiro2cc
claude "$@"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		return StatusMsg{
			Message: fmt.Sprintf("Failed to create script: %v", err),
			IsError: true,
		}
	}
	return StatusMsg{
		Message: fmt.Sprintf("Created %s", scriptPath),
		IsError: false,
	}
}

// StartServerCmd returns a function that starts the server
// This is a special command that needs to send log messages to the TUI
func StartServerCmd(port string, lg *logger.Logger, program *tea.Program) tea.Msg {
	// The actual server start will be done in main.go
	// Here we just signal that it should start
	return dashboard.ServerStartedMsg{Port: port}
}

// UsageLimitsResponse represents the response from getUsageLimits API
type UsageLimitsResponse struct {
	DaysUntilReset     int `json:"daysUntilReset"`
	UsageBreakdownList []struct {
		CurrentUsage              float64 `json:"currentUsage"`
		CurrentUsageWithPrecision float64 `json:"currentUsageWithPrecision"`
		UsageLimit                float64 `json:"usageLimit"`
		UsageLimitWithPrecision   float64 `json:"usageLimitWithPrecision"`
	} `json:"usageBreakdownList"`
	SubscriptionInfo struct {
		SubscriptionTitle string `json:"subscriptionTitle"`
	} `json:"subscriptionInfo"`
}

// CreditsInfo holds credit information for display
type CreditsInfo struct {
	CreditsUsed      float64
	CreditsLimit     float64
	CreditsRemaining float64
	DaysUntilReset   int
	SubscriptionName string
	Error            error
}

// GetCreditsInfo fetches credit information from Kiro API
func GetCreditsInfo() CreditsInfo {
	token, err := GetToken()
	if err != nil {
		return CreditsInfo{Error: fmt.Errorf("no token: %v", err)}
	}

	// Build URL with query params
	baseURL := "https://codewhisperer.us-east-1.amazonaws.com/getUsageLimits"
	req, err := http.NewRequest("GET", baseURL, nil)
	if err != nil {
		return CreditsInfo{Error: fmt.Errorf("failed to create request: %v", err)}
	}

	// Add query parameters
	q := req.URL.Query()
	if token.ProfileArn != "" {
		q.Add("profileArn", token.ProfileArn)
	}
	q.Add("origin", "CHAT")
	req.URL.RawQuery = q.Encode()

	// Add headers
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("KiroIDE-%s-%s", kiroVersion, runtime.GOOS))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return CreditsInfo{Error: fmt.Errorf("request failed: %v", err)}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return CreditsInfo{Error: fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))}
	}

	var usageResp UsageLimitsResponse
	if err := json.Unmarshal(body, &usageResp); err != nil {
		return CreditsInfo{Error: fmt.Errorf("failed to parse response: %v", err)}
	}

	// Extract credit info from response
	info := CreditsInfo{
		DaysUntilReset:   usageResp.DaysUntilReset,
		SubscriptionName: usageResp.SubscriptionInfo.SubscriptionTitle,
	}

	if len(usageResp.UsageBreakdownList) > 0 {
		breakdown := usageResp.UsageBreakdownList[0]
		info.CreditsUsed = breakdown.CurrentUsageWithPrecision
		if info.CreditsUsed == 0 {
			info.CreditsUsed = breakdown.CurrentUsage
		}
		info.CreditsLimit = breakdown.UsageLimitWithPrecision
		if info.CreditsLimit == 0 {
			info.CreditsLimit = breakdown.UsageLimit
		}
		info.CreditsRemaining = info.CreditsLimit - info.CreditsUsed
	}

	return info
}

// ViewCreditsCmd fetches and displays credit information
func ViewCreditsCmd() tea.Msg {
	info := GetCreditsInfo()
	if info.Error != nil {
		return StatusMsg{
			Message: fmt.Sprintf("Failed to fetch credits: %v", info.Error),
			IsError: true,
		}
	}

	return StatusMsg{
		Message: fmt.Sprintf("%.1f/%.0f credits used | %.1f remaining | Resets in %d days | %s",
			info.CreditsUsed, info.CreditsLimit, info.CreditsRemaining, info.DaysUntilReset, info.SubscriptionName),
		IsError: false,
	}
}
