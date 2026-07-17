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

const kiroVersion = "0.11.107"

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

	// Bound the call: http.Post uses the zero-timeout default client, so a
	// refresh endpoint that accepts the connection but never responds would
	// otherwise freeze every command that refreshes at startup (run, server,
	// TUI) — the process prints "refreshing..." and hangs forever.
	client := &http.Client{Timeout: cfg.Network.HTTPTimeout}
	resp, err := client.Post(
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
	var config map[string]any
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
		if oauth, ok := config["oauthAccount"].(map[string]any); ok {
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
		config = make(map[string]any)
	}

	// Only modify if not already configured
	if !claudeConfigured {
		// Set claude2kiro flag
		config["claude2kiro"] = true

		// Set oauthAccount type to api_key (preserve other fields if they exist)
		if existingOauth, ok := config["oauthAccount"].(map[string]any); ok {
			existingOauth["type"] = "api_key"
		} else {
			config["oauthAccount"] = map[string]any{
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
set CLAUDE_CODE_USE_BEDROCK=
set CLAUDE_CODE_USE_VERTEX=
set ANTHROPIC_BEDROCK_BASE_URL=
set ANTHROPIC_VERTEX_BASE_URL=
set AWS_BEARER_TOKEN_BEDROCK=
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
$env:CLAUDE_CODE_USE_BEDROCK = $null
$env:CLAUDE_CODE_USE_VERTEX = $null
$env:ANTHROPIC_BEDROCK_BASE_URL = $null
$env:ANTHROPIC_VERTEX_BASE_URL = $null
$env:AWS_BEARER_TOKEN_BEDROCK = $null
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
			scriptMsg = "claude-kiro ready (already in PATH)"
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
unset CLAUDE_CODE_USE_BEDROCK CLAUDE_CODE_USE_VERTEX
unset ANTHROPIC_BEDROCK_BASE_URL ANTHROPIC_VERTEX_BASE_URL AWS_BEARER_TOKEN_BEDROCK
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

type cleanupPaths struct {
	homeDir           string
	appData           string
	localAppData      string
	exeDir            string
	removeWindowsPath bool
}

func defaultCleanupPaths() (cleanupPaths, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return cleanupPaths{}, err
	}
	exeDir := ""
	if exePath, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exePath)
	}
	return cleanupPaths{
		homeDir:           homeDir,
		appData:           os.Getenv("APPDATA"),
		localAppData:      os.Getenv("LOCALAPPDATA"),
		exeDir:            exeDir,
		removeWindowsPath: true,
	}, nil
}

// CleanupClaude2KiroConfig removes persistent Claude Code/Desktop integration
// files that make Claude continue routing through Claude2Kiro after the proxy
// has stopped. It preserves unrelated Claude account, plugin, and MCP state.
func CleanupClaude2KiroConfig() ([]string, error) {
	paths, err := defaultCleanupPaths()
	if err != nil {
		return nil, err
	}
	return cleanupClaude2KiroConfig(paths)
}

func cleanupClaude2KiroConfig(paths cleanupPaths) ([]string, error) {
	if paths.homeDir == "" {
		return nil, fmt.Errorf("home directory is required")
	}

	var actions []string
	var err error
	if a, e := cleanupClaudeJSON(paths.homeDir); e != nil {
		err = e
	} else {
		actions = append(actions, a...)
	}
	if a, e := cleanupClaudeSettings(paths.homeDir); e != nil && err == nil {
		err = e
	} else {
		actions = append(actions, a...)
	}
	if a, e := cleanupClaudePluginRegistries(paths.homeDir); e != nil && err == nil {
		err = e
	} else {
		actions = append(actions, a...)
	}
	if a, e := cleanupClaudePluginDirs(paths.homeDir); e != nil && err == nil {
		err = e
	} else {
		actions = append(actions, a...)
	}
	if a, e := cleanupLaunchScripts(paths); e != nil && err == nil {
		err = e
	} else {
		actions = append(actions, a...)
	}
	if a, e := cleanupClaudeDesktop(paths); e != nil && err == nil {
		err = e
	} else {
		actions = append(actions, a...)
	}
	if err != nil {
		return actions, err
	}
	return actions, nil
}

func cleanupClaudeJSON(homeDir string) ([]string, error) {
	claudePath := filepath.Join(homeDir, ".claude.json")
	backupPath := claudePath + ".backup"
	var actions []string

	if current, err := readJSONMap(claudePath); err == nil && claudeJSONUsesProxyAccount(current) {
		if backupData, err := os.ReadFile(backupPath); err == nil {
			if err := os.WriteFile(claudePath, backupData, 0600); err != nil {
				return actions, fmt.Errorf("restore %s: %w", claudePath, err)
			}
			_ = os.Remove(backupPath)
			actions = append(actions, "restored .claude.json backup")
		}
	}

	cfg, err := readJSONMap(claudePath)
	if err != nil {
		if os.IsNotExist(err) {
			return actions, nil
		}
		return actions, err
	}

	modified := false
	if removeKey(cfg, "claude2kiro") {
		modified = true
	}
	if v, ok := cfg["primaryAccountUuid"].(string); ok && v == "claude2kiro-local" {
		delete(cfg, "primaryAccountUuid")
		modified = true
	}
	if oauth, ok := cfg["oauthAccount"].(map[string]any); ok {
		if t, ok := oauth["type"].(string); ok && t == "api_key" {
			delete(cfg, "oauthAccount")
			modified = true
		}
	}
	if responses, ok := cfg["customApiKeyResponses"].(map[string]any); ok {
		if approved, ok := responses["approved"].([]any); ok {
			filtered, changed := removeStrings(approved, "claude2kiro", "kiro2cc")
			if changed {
				responses["approved"] = filtered
				modified = true
			}
		}
	}

	if modified {
		if err := writeJSONMap(claudePath, cfg, 0600); err != nil {
			return actions, err
		}
		actions = append(actions, "cleaned .claude.json")
	}
	return actions, nil
}

func claudeJSONUsesProxyAccount(cfg map[string]any) bool {
	if v, ok := cfg["claude2kiro"].(bool); ok && v {
		return true
	}
	if v, ok := cfg["primaryAccountUuid"].(string); ok && v == "claude2kiro-local" {
		return true
	}
	if oauth, ok := cfg["oauthAccount"].(map[string]any); ok {
		if t, ok := oauth["type"].(string); ok && t == "api_key" {
			return true
		}
	}
	return false
}

func cleanupClaudeSettings(homeDir string) ([]string, error) {
	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")
	settings, err := readJSONMap(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	modified := false
	looksProxyConfigured := false
	if envMap, ok := settings["env"].(map[string]any); ok {
		looksProxyConfigured = envLooksClaude2Kiro(envMap)
		if looksProxyConfigured {
			for _, key := range []string{
				"CLAUDE_CODE_USE_BEDROCK",
				"CLAUDE_CODE_USE_VERTEX",
				"ANTHROPIC_BEDROCK_BASE_URL",
				"ANTHROPIC_VERTEX_BASE_URL",
				"ANTHROPIC_VERTEX_PROJECT_ID",
				"AWS_BEARER_TOKEN_BEDROCK",
				"CLOUD_ML_REGION",
				"ANTHROPIC_BASE_URL",
				"ANTHROPIC_AUTH_TOKEN",
				"ANTHROPIC_API_KEY",
				"CLAUDE2KIRO",
				"CLAUDE_CODE_DISABLE_THINKING",
			} {
				if removeKey(envMap, key) {
					modified = true
				}
			}
			fakeAWS := false
			if v, ok := envMap["AWS_ACCESS_KEY_ID"].(string); ok && v == "claude2kiro" {
				delete(envMap, "AWS_ACCESS_KEY_ID")
				modified = true
				fakeAWS = true
			}
			if v, ok := envMap["AWS_SECRET_ACCESS_KEY"].(string); ok && v == "secretkey" {
				delete(envMap, "AWS_SECRET_ACCESS_KEY")
				modified = true
				fakeAWS = true
			}
			if fakeAWS {
				if removeKey(envMap, "AWS_REGION") {
					modified = true
				}
			}
		}
		if len(envMap) == 0 {
			delete(settings, "env")
			modified = true
		}
	}

	pluginEnabled := false
	if enabled, ok := settings["enabledPlugins"].(map[string]any); ok {
		if removeKey(enabled, "kiro-proxy@claude2kiro") {
			modified = true
			pluginEnabled = true
		}
	}
	if model, ok := settings["model"].(string); ok && (looksProxyConfigured || pluginEnabled) && isClaude2KiroModelOverride(model) {
		delete(settings, "model")
		modified = true
	}

	if !modified {
		return nil, nil
	}
	if err := writeJSONMap(settingsPath, settings, 0600); err != nil {
		return nil, err
	}
	return []string{"cleaned Claude Code settings"}, nil
}

func envLooksClaude2Kiro(envMap map[string]any) bool {
	for key, value := range envMap {
		s, _ := value.(string)
		switch key {
		case "ANTHROPIC_BASE_URL":
			if strings.Contains(s, "localhost:8080") || strings.Contains(s, "127.0.0.1:8080") {
				return true
			}
		case "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE2KIRO", "AWS_ACCESS_KEY_ID":
			if strings.Contains(strings.ToLower(s), "claude2kiro") {
				return true
			}
		case "AWS_SECRET_ACCESS_KEY":
			if s == "secretkey" {
				return true
			}
		}
	}
	return false
}

func isClaude2KiroModelOverride(model string) bool {
	lower := strings.ToLower(model)
	return strings.HasPrefix(lower, "us.anthropic.") ||
		strings.Contains(lower, "claude-opus-4-8") ||
		strings.Contains(lower, "claude-sonnet-4-6") ||
		strings.Contains(lower, "kiro")
}

func cleanupClaudePluginRegistries(homeDir string) ([]string, error) {
	var actions []string
	knownPath := filepath.Join(homeDir, ".claude", "plugins", "known_marketplaces.json")
	if known, err := readJSONMap(knownPath); err == nil {
		if removeKey(known, "claude2kiro") {
			if err := writeJSONMap(knownPath, known, 0600); err != nil {
				return actions, err
			}
			actions = append(actions, "removed claude2kiro marketplace")
		}
	} else if !os.IsNotExist(err) {
		return actions, err
	}

	installedPath := filepath.Join(homeDir, ".claude", "plugins", "installed_plugins.json")
	if installed, err := readJSONMap(installedPath); err == nil {
		if plugins, ok := installed["plugins"].(map[string]any); ok {
			if removeKey(plugins, "kiro-proxy@claude2kiro") {
				if err := writeJSONMap(installedPath, installed, 0600); err != nil {
					return actions, err
				}
				actions = append(actions, "removed kiro-proxy plugin")
			}
		}
	} else if !os.IsNotExist(err) {
		return actions, err
	}
	return actions, nil
}

func cleanupClaudePluginDirs(homeDir string) ([]string, error) {
	var actions []string
	for _, path := range []string{
		filepath.Join(homeDir, ".claude", "plugins", "marketplaces", "claude2kiro"),
		filepath.Join(homeDir, ".claude", "plugins", "cache", "claude2kiro"),
		filepath.Join(homeDir, ".claude", "plugins", "local", "kiro-proxy"),
	} {
		removed, err := removeAllIfExists(path)
		if err != nil {
			return actions, err
		}
		if removed {
			actions = append(actions, "removed "+filepath.Base(path))
		}
	}
	return actions, nil
}

func cleanupLaunchScripts(paths cleanupPaths) ([]string, error) {
	var actions []string
	if runtime.GOOS == "windows" {
		binDir := filepath.Join(paths.homeDir, ".claude2kiro", "bin")
		for _, path := range []string{
			filepath.Join(binDir, "claude-kiro.bat"),
			filepath.Join(binDir, "claude-kiro.ps1"),
			filepath.Join(paths.exeDir, "claude-kiro.bat"),
			filepath.Join(paths.exeDir, "claude-kiro.ps1"),
		} {
			removed, err := removeFileIfExists(path)
			if err != nil {
				return actions, err
			}
			if removed {
				actions = append(actions, "removed "+filepath.Base(path))
			}
		}
		if paths.removeWindowsPath {
			if removed, _ := removeFromWindowsPath(binDir); removed {
				actions = append(actions, "removed claude-kiro from PATH")
			}
		}
		return actions, nil
	}

	scriptPath := filepath.Join(paths.homeDir, ".local", "bin", "claude-kiro")
	removed, err := removeFileIfExists(scriptPath)
	if err != nil {
		return actions, err
	}
	if removed {
		actions = append(actions, "removed claude-kiro script")
	}
	return actions, nil
}

func cleanupClaudeDesktop(paths cleanupPaths) ([]string, error) {
	var actions []string
	if paths.localAppData != "" {
		configLibrary := filepath.Join(paths.localAppData, "Claude-3p", "configLibrary")
		if isClaude2KiroDesktopConfig(configLibrary) {
			removed, err := removeAllIfExists(configLibrary)
			if err != nil {
				return actions, err
			}
			if removed {
				actions = append(actions, "removed Claude Desktop gateway config")
			}
		}
	}
	if paths.appData != "" {
		shortcut := filepath.Join(paths.appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "Claude2Kiro Proxy.lnk")
		removed, err := removeFileIfExists(shortcut)
		if err != nil {
			return actions, err
		}
		if removed {
			actions = append(actions, "removed Claude2Kiro Startup shortcut")
		}
	}
	return actions, nil
}

func isClaude2KiroDesktopConfig(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	found := false
	_ = filepath.Walk(path, func(file string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found {
			return nil
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return nil
		}
		text := strings.ToLower(string(data))
		if strings.Contains(text, "claude2kiro") {
			found = true
		}
		return nil
	})
	return found
}

func readJSONMap(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func writeJSONMap(path string, value map[string]any, perm os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}

func removeKey(m map[string]any, key string) bool {
	if _, ok := m[key]; !ok {
		return false
	}
	delete(m, key)
	return true
}

func removeStrings(values []any, targets ...string) ([]any, bool) {
	remove := map[string]bool{}
	for _, target := range targets {
		remove[target] = true
	}
	filtered := values[:0]
	changed := false
	for _, value := range values {
		if s, ok := value.(string); ok && remove[s] {
			changed = true
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered, changed
}

func removeFileIfExists(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func removeAllIfExists(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := os.RemoveAll(path); err != nil {
		return false, err
	}
	return true, nil
}

// UnconfigureCmd removes Claude2Kiro settings and restores normal Claude routing.
func UnconfigureCmd() tea.Msg {
	actions, err := CleanupClaude2KiroConfig()
	if err != nil {
		return StatusMsg{
			Message: fmt.Sprintf("Failed to unconfigure Claude2Kiro: %v", err),
			IsError: true,
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

	// Build GET request with query parameters (matches Kiro SDK's GetUsageLimitsCommand)
	cfg := config.Get()
	baseURL := cfg.Advanced.CreditsEndpoint

	req, err := http.NewRequest("GET", baseURL, nil)
	if err != nil {
		return CreditsInfo{Error: fmt.Errorf("failed to create request: %v", err)}
	}

	// Add query parameters
	q := req.URL.Query()
	q.Set("origin", "AI_EDITOR")
	q.Set("resourceType", "AGENTIC_REQUEST")
	if token.ProfileArn != "" {
		q.Set("profileArn", token.ProfileArn)
	}
	req.URL.RawQuery = q.Encode()

	// Add headers
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
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
		daysUntilReset = max(int(time.Until(resetTime).Hours()/24), 0)
	}

	// Extract credit info from response
	info := CreditsInfo{
		DaysUntilReset:   daysUntilReset,
		SubscriptionName: usageResp.SubscriptionInfo.SubscriptionTitle,
	}

	if len(usageResp.UsageBreakdownList) == 0 {
		return CreditsInfo{Error: fmt.Errorf("empty usage data (no usageBreakdownList in response)")}
	}

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
