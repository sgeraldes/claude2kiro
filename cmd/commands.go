package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sgeraldes/claude2kiro/internal/attachments"
	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
	"github.com/sgeraldes/claude2kiro/internal/tui/messages"
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

const kiroVersion = "0.11.28"

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

// IsTokenExpired returns true if the token is expired or will expire within the configured threshold
func IsTokenExpired() bool {
	expiry := GetTokenExpiry()
	if expiry.IsZero() {
		return true
	}
	cfg := config.Get()
	return time.Until(expiry) < cfg.Network.TokenRefreshThreshold
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

	cfg := config.Get()
	region := currentToken.Region
	if region == "" {
		region = cfg.Advanced.AWSRegion
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

	client := &http.Client{Timeout: config.Get().Network.HTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return TokenData{}, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return TokenData{}, fmt.Errorf("failed to read response body: %v", err)
	}

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
	cfg := config.Get()
	refreshReq := RefreshRequest{
		RefreshToken: currentToken.RefreshToken,
	}

	reqBody, err := json.Marshal(refreshReq)
	if err != nil {
		return TokenData{}, fmt.Errorf("failed to serialize request: %v", err)
	}

	resp, err := http.Post(
		cfg.Advanced.KiroRefreshEndpoint,
		"application/json",
		bytes.NewBuffer(reqBody),
	)
	if err != nil {
		return TokenData{}, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return TokenData{}, fmt.Errorf("status code: %d (failed to read response: %v)", resp.StatusCode, err)
		}
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
		Message: "Login requires running 'claude2kiro login' separately for now",
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

	// Return instructions with truncated token preview
	var msg string
	tokenPreview := token.AccessToken
	if len(tokenPreview) > 20 {
		tokenPreview = tokenPreview[:20] + "..."
	}
	if runtime.GOOS == "windows" {
		msg = fmt.Sprintf("CMD: set ANTHROPIC_BASE_URL=http://localhost:8080 && set ANTHROPIC_API_KEY=%s", tokenPreview)
	} else {
		msg = fmt.Sprintf("export ANTHROPIC_BASE_URL=http://localhost:8080 ANTHROPIC_API_KEY=%s", tokenPreview)
	}

	return StatusMsg{
		Message: msg,
		IsError: false,
	}
}

// LogoutCmd returns a function that logs out
func LogoutCmd() tea.Msg {
	configPath := filepath.Join(filepath.Dir(GetTokenFilePath()), "claude2kiro-login-config.json")
	tokenPath := GetTokenFilePath()

	os.Remove(configPath)
	os.Remove(tokenPath)

	return StatusMsg{
		Message: "Logged out successfully",
		IsError: false,
	}
}

// addToWindowsPath adds a directory to the user PATH environment variable
// Returns (added bool, err error) where added is true if the path was added
func addToWindowsPath(dir string) (bool, error) {
	if runtime.GOOS != "windows" {
		return false, nil
	}

	// Get current PATH from environment (includes both user and system PATH)
	currentPath := os.Getenv("PATH")
	pathDirs := strings.Split(currentPath, ";")

	// Check if already in PATH (case-insensitive on Windows)
	dirLower := strings.ToLower(dir)
	for _, p := range pathDirs {
		if strings.ToLower(strings.TrimSpace(p)) == dirLower {
			return false, nil // Already in PATH
		}
	}

	// Use PowerShell to add to user PATH via registry
	// This modifies HKCU\Environment\Path which persists across sessions
	psScript := fmt.Sprintf(`
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -eq $null) { $userPath = '' }
$newDir = '%s'
$paths = $userPath -split ';' | Where-Object { $_ -ne '' }
$found = $false
foreach ($p in $paths) {
    if ($p.ToLower() -eq $newDir.ToLower()) {
        $found = $true
        break
    }
}
if (-not $found) {
    if ($userPath -ne '') {
        $userPath = $userPath + ';' + $newDir
    } else {
        $userPath = $newDir
    }
    [Environment]::SetEnvironmentVariable('Path', $userPath, 'User')
    Write-Output 'ADDED'
} else {
    Write-Output 'EXISTS'
}
`, dir)

	// Execute PowerShell
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to update PATH: %v (output: %s)", err, string(output))
	}

	result := strings.TrimSpace(string(output))
	return result == "ADDED", nil
}

// removeFromWindowsPath removes a directory from the user PATH environment variable
// Returns (removed bool, err error) where removed is true if the path was removed
func removeFromWindowsPath(dir string) (bool, error) {
	if runtime.GOOS != "windows" {
		return false, nil
	}

	// Use PowerShell to remove from user PATH via registry
	psScript := fmt.Sprintf(`
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -eq $null) {
    Write-Output 'NOTFOUND'
    exit
}
$removeDir = '%s'
$paths = $userPath -split ';' | Where-Object { $_ -ne '' }
$newPaths = @()
$found = $false
foreach ($p in $paths) {
    if ($p.ToLower() -eq $removeDir.ToLower()) {
        $found = $true
    } else {
        $newPaths += $p
    }
}
if ($found) {
    $newPath = $newPaths -join ';'
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    Write-Output 'REMOVED'
} else {
    Write-Output 'NOTFOUND'
}
`, dir)

	// Execute PowerShell
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to update PATH: %v (output: %s)", err, string(output))
	}

	result := strings.TrimSpace(string(output))
	return result == "REMOVED", nil
}

// ConfigureClaudeCmd creates launch scripts and configures ~/.claude.json for Claude2Kiro proxy
func ConfigureClaudeCmd() tea.Msg {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return StatusMsg{
			Message: fmt.Sprintf("Failed to get home directory: %v", err),
			IsError: true,
		}
	}

	// Configure ~/.claude.json
	claudePath := filepath.Join(homeDir, ".claude.json")
	var config map[string]interface{}
	claudeConfigured := false

	// Read existing config or create new one
	if data, err := os.ReadFile(claudePath); err == nil {
		json.Unmarshal(data, &config)

		// Check if already configured correctly
		claude2kiroSet := false
		apiKeySet := false

		if k, ok := config["claude2kiro"].(bool); ok && k {
			claude2kiroSet = true
		}
		if oauth, ok := config["oauthAccount"].(map[string]interface{}); ok {
			if t, ok := oauth["type"].(string); ok && t == "api_key" {
				apiKeySet = true
			}
		}

		if claude2kiroSet && apiKeySet {
			// Already configured, skip modification
			claudeConfigured = true
		} else {
			// Need to modify - create backup atomically (only if it doesn't exist)
			backupPath := claudePath + ".backup"
			// Use O_CREATE|O_EXCL for atomic "create if not exists" - prevents TOCTOU race
			backupFile, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err == nil {
				// Backup file was created, write the data
				_, writeErr := backupFile.Write(data)
				backupFile.Close()
				if writeErr != nil {
					return StatusMsg{
						Message: fmt.Sprintf("Failed to write backup ~/.claude.json: %v", writeErr),
						IsError: true,
					}
				}
			}
			// If err != nil (file already exists), that's fine - we skip backup creation
		}
	}
	if config == nil {
		config = make(map[string]interface{})
	}

	// Only modify if not already configured
	if !claudeConfigured {
		// Set claude2kiro flag
		config["claude2kiro"] = true

		// Set oauthAccount type to api_key (preserve other fields if they exist)
		if existingOauth, ok := config["oauthAccount"].(map[string]interface{}); ok {
			existingOauth["type"] = "api_key"
		} else {
			config["oauthAccount"] = map[string]interface{}{
				"type": "api_key",
			}
		}

		// Write updated config
		configData, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			return StatusMsg{
				Message: fmt.Sprintf("Failed to serialize config: %v", err),
				IsError: true,
			}
		}
		if err := os.WriteFile(claudePath, configData, 0600); err != nil {
			return StatusMsg{
				Message: fmt.Sprintf("Failed to write ~/.claude.json: %v", err),
				IsError: true,
			}
		}
	}

	// Create platform-specific launch scripts in PATH-accessible location
	var scriptMsg string
	binDir := filepath.Join(homeDir, ".claude2kiro", "bin")

	if runtime.GOOS == "windows" {
		// Windows: create scripts in ~/.claude2kiro/bin and add to PATH
		if err := os.MkdirAll(binDir, 0755); err != nil {
			return StatusMsg{
				Message: fmt.Sprintf("Configured ~/.claude.json but failed to create bin directory: %v", err),
				IsError: true,
			}
		}

		// Create CMD batch file
		batPath := filepath.Join(binDir, "claude-kiro.bat")
		batScript := `@echo off
set ANTHROPIC_BASE_URL=http://localhost:8080
set ANTHROPIC_API_KEY=claude2kiro
claude %*
`
		if err := os.WriteFile(batPath, []byte(batScript), 0755); err != nil {
			return StatusMsg{
				Message: fmt.Sprintf("Configured ~/.claude.json but failed to create batch script: %v", err),
				IsError: true,
			}
		}

		// Create PowerShell script
		ps1Path := filepath.Join(binDir, "claude-kiro.ps1")
		ps1Script := `$env:ANTHROPIC_BASE_URL = "http://localhost:8080"
$env:ANTHROPIC_API_KEY = "claude2kiro"
claude @args
`
		if err := os.WriteFile(ps1Path, []byte(ps1Script), 0755); err != nil {
			return StatusMsg{
				Message: fmt.Sprintf("Configured ~/.claude.json but failed to create PowerShell script: %v", err),
				IsError: true,
			}
		}

		// Add binDir to user PATH if not already present
		pathAdded, pathErr := addToWindowsPath(binDir)
		if pathErr != nil {
			scriptMsg = fmt.Sprintf("scripts in %s (add to PATH manually: %v)", binDir, pathErr)
		} else if pathAdded {
			scriptMsg = fmt.Sprintf("claude-kiro added to PATH (%s)", binDir)
		} else {
			scriptMsg = fmt.Sprintf("claude-kiro ready (already in PATH)")
		}
	} else {
		// Unix (Linux/macOS): create shell script in ~/.local/bin
		binDir = filepath.Join(homeDir, ".local", "bin")
		if err := os.MkdirAll(binDir, 0755); err != nil {
			return StatusMsg{
				Message: fmt.Sprintf("Configured ~/.claude.json but failed to create ~/.local/bin: %v", err),
				IsError: true,
			}
		}

		scriptPath := filepath.Join(binDir, "claude-kiro")
		script := `#!/bin/bash
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_API_KEY=claude2kiro
claude "$@"
`
		if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
			return StatusMsg{
				Message: fmt.Sprintf("Configured ~/.claude.json but failed to create script: %v", err),
				IsError: true,
			}
		}

		scriptMsg = scriptPath
	}

	return StatusMsg{
		Message: fmt.Sprintf("Configured Claude Code + created %s", scriptMsg),
		IsError: false,
	}
}

// UnconfigureCmd removes Claude2Kiro settings and restores original Claude config
func UnconfigureCmd() tea.Msg {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return StatusMsg{
			Message: fmt.Sprintf("Failed to get home directory: %v", err),
			IsError: true,
		}
	}

	var actions []string

	// Restore ~/.claude.json from backup or remove Claude2Kiro settings
	claudePath := filepath.Join(homeDir, ".claude.json")
	backupPath := claudePath + ".backup"

	if _, err := os.Stat(backupPath); err == nil {
		// Backup exists - restore it
		if err := os.Rename(backupPath, claudePath); err != nil {
			return StatusMsg{
				Message: fmt.Sprintf("Failed to restore ~/.claude.json from backup: %v", err),
				IsError: true,
			}
		}
		actions = append(actions, "restored ~/.claude.json")
	} else if data, err := os.ReadFile(claudePath); err == nil {
		// No backup, but config exists - remove Claude2Kiro settings
		var config map[string]interface{}
		if err := json.Unmarshal(data, &config); err == nil {
			modified := false
			if _, ok := config["claude2kiro"]; ok {
				delete(config, "claude2kiro")
				modified = true
			}
			if _, ok := config["oauthAccount"]; ok {
				delete(config, "oauthAccount")
				modified = true
			}
			if modified {
				configData, _ := json.MarshalIndent(config, "", "  ")
				if err := os.WriteFile(claudePath, configData, 0600); err != nil {
					return StatusMsg{
						Message: fmt.Sprintf("Failed to update ~/.claude.json: %v", err),
						IsError: true,
					}
				}
				actions = append(actions, "removed Claude2Kiro settings from ~/.claude.json")
			}
		}
	}

	// Remove launch scripts
	if runtime.GOOS == "windows" {
		// New location: ~/.claude2kiro/bin
		binDir := filepath.Join(homeDir, ".claude2kiro", "bin")
		batPath := filepath.Join(binDir, "claude-kiro.bat")
		ps1Path := filepath.Join(binDir, "claude-kiro.ps1")

		if err := os.Remove(batPath); err == nil {
			actions = append(actions, "removed claude-kiro.bat")
		}
		if err := os.Remove(ps1Path); err == nil {
			actions = append(actions, "removed claude-kiro.ps1")
		}

		// Also check old location (next to executable) for backward compatibility
		if exePath, err := os.Executable(); err == nil {
			exeDir := filepath.Dir(exePath)
			oldBatPath := filepath.Join(exeDir, "claude-kiro.bat")
			oldPs1Path := filepath.Join(exeDir, "claude-kiro.ps1")

			if err := os.Remove(oldBatPath); err == nil {
				actions = append(actions, "removed old claude-kiro.bat")
			}
			if err := os.Remove(oldPs1Path); err == nil {
				actions = append(actions, "removed old claude-kiro.ps1")
			}
		}

		// Remove binDir from PATH
		if removed, _ := removeFromWindowsPath(binDir); removed {
			actions = append(actions, "removed from PATH")
		}
	} else {
		scriptPath := filepath.Join(homeDir, ".local", "bin", "claude-kiro")
		if err := os.Remove(scriptPath); err == nil {
			actions = append(actions, "removed claude-kiro script")
		}
	}

	if len(actions) == 0 {
		return StatusMsg{
			Message: "Nothing to unconfigure",
			IsError: false,
		}
	}

	return StatusMsg{
		Message: "Unconfigured: " + strings.Join(actions, ", "),
		IsError: false,
	}
}

// StartServerCmd returns a function that starts the server
// This is a special command that needs to send log messages to the TUI
func StartServerCmd(port string, lg *logger.Logger, program *tea.Program) tea.Msg {
	// The actual server start will be done in main.go
	// Here we just signal that it should start
	return messages.ServerStartedMsg{Port: port}
}

// UsageLimitsResponse represents the response from getUsageLimits API
type UsageLimitsResponse struct {
	DaysUntilReset     int     `json:"daysUntilReset"`
	NextDateReset      float64 `json:"nextDateReset"`
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

	// Build POST request with JSON body (CodeWhisperer endpoints use POST, not GET)
	cfg := config.Get()
	baseURL := cfg.Advanced.CreditsEndpoint

	// Build request body
	reqBody := map[string]string{
		"origin":       "AI_EDITOR",
		"resourceType": "AGENTIC_REQUEST",
	}
	if token.AuthMethod != "IdC" && token.ProfileArn != "" {
		reqBody["profileArn"] = token.ProfileArn
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return CreditsInfo{Error: fmt.Errorf("failed to marshal request: %v", err)}
	}

	req, err := http.NewRequest("POST", baseURL, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return CreditsInfo{Error: fmt.Errorf("failed to create request: %v", err)}
	}

	// Add headers
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("KiroIDE-%s-%s", kiroVersion, runtime.GOOS))

	client := &http.Client{Timeout: config.Get().Network.HTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return CreditsInfo{Error: fmt.Errorf("request failed: %v", err)}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return CreditsInfo{Error: fmt.Errorf("failed to read response: %v", err)}
	}

	if resp.StatusCode != http.StatusOK {
		// Include more detail in error for debugging
		bodyPreview := string(body)
		if len(bodyPreview) > 200 {
			bodyPreview = bodyPreview[:200] + "..."
		}
		return CreditsInfo{Error: fmt.Errorf("status %d: %s", resp.StatusCode, bodyPreview)}
	}

	var usageResp UsageLimitsResponse
	if err := json.Unmarshal(body, &usageResp); err != nil {
		return CreditsInfo{Error: fmt.Errorf("failed to parse response: %v", err)}
	}

	// Calculate days until reset from timestamp if not provided
	daysUntilReset := usageResp.DaysUntilReset
	if daysUntilReset == 0 && usageResp.NextDateReset > 0 {
		resetTime := time.Unix(int64(usageResp.NextDateReset), 0)
		daysUntilReset = int(time.Until(resetTime).Hours() / 24)
		if daysUntilReset < 0 {
			daysUntilReset = 0
		}
	}

	// Extract credit info from response
	info := CreditsInfo{
		DaysUntilReset:   daysUntilReset,
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

// GetKiroUsageURL returns the URL to the Kiro usage/billing page
func GetKiroUsageURL() string {
	return config.Get().Advanced.KiroUsageURL
}

// OpenBrowser opens the specified URL in the default browser
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default: // Linux and others
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// IsClaudeRunning checks if Claude Code process is currently running
func IsClaudeRunning() bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// Use tasklist to check for claude.exe or node.exe running Claude
		cmd = exec.Command("tasklist", "/FI", "IMAGENAME eq claude.exe", "/NH")
	case "darwin":
		// macOS: use pgrep to check for claude process
		cmd = exec.Command("pgrep", "-x", "claude")
	default: // Linux and others
		cmd = exec.Command("pgrep", "-x", "claude")
	}

	output, err := cmd.Output()
	if err != nil {
		return false
	}

	// If output contains something, process is running
	return len(output) > 0 && !strings.Contains(string(output), "INFO: No tasks")
}

// OpenClaudeCode opens Claude Code with the specified session parameters
// workingDir: the directory to open Claude in
// fullUUID: the full session UUID (not the short 8-char version)
// sessionID: the short session ID for logging purposes
func OpenClaudeCode(workingDir, fullUUID, sessionID string) error {
	// Build the claude command
	var args []string

	// Add session ID if provided (the full UUID, not the short 8-char version)
	if fullUUID != "" {
		args = append(args, "--session-id", fullUUID)
	}

	// Add working directory if provided
	if workingDir != "" {
		args = append(args, workingDir)
	}

	// Execute claude command
	cmd := exec.Command("claude", args...)

	// Start the process in the background
	return cmd.Start()
}

// MigrateLogs migrates existing log files to use the attachment store
func MigrateLogs(dateFilter string) error {
	cfg := config.Get()
	logDir := config.ExpandPath(cfg.Logging.Directory)
	attachDir := filepath.Join(filepath.Dir(logDir), "attachments")

	// Create attachment store
	store, err := attachments.NewStore(attachDir)
	if err != nil {
		return fmt.Errorf("failed to create attachment store: %w", err)
	}

	if dateFilter != "" {
		// Migrate specific date
		logFile := filepath.Join(logDir, dateFilter+".log")
		result, err := attachments.MigrateLogFile(logFile, store)
		if err != nil {
			return err
		}
		fmt.Println(result.FormatSummary())
	} else {
		// Migrate all logs
		results, err := attachments.MigrateAllLogs(logDir, store)
		if err != nil {
			return err
		}
		var totalSaved int64
		for _, r := range results {
			fmt.Println(r.FormatSummary())
			totalSaved += r.SpaceSaved
		}
		fmt.Printf("\nTotal space saved: %s\n", attachments.FormatBytes(totalSaved))
	}

	return nil
}
