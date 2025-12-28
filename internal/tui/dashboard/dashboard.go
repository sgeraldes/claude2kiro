package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bestk/kiro2cc/internal/tui/logger"
	"github.com/bestk/kiro2cc/internal/tui/menu"
)

// Version from menu package
var Version = menu.Version

// FocusedPanel identifies which panel is focused
type FocusedPanel int

const (
	PanelSession FocusedPanel = iota
	PanelLogs
)

// ServerStartedMsg indicates the server has started
type ServerStartedMsg struct {
	Port string
}

// ServerStoppedMsg indicates the server has stopped
type ServerStoppedMsg struct {
	Err error
}

// BackToMenuMsg signals return to the main menu (server stopped)
type BackToMenuMsg struct{}

// GoToMenuMsg signals navigation to menu while keeping server running
type GoToMenuMsg struct{}

// TickMsg for periodic updates
type TickMsg time.Time

// CreditsInfo holds credit usage information
type CreditsInfo struct {
	CreditsUsed      float64
	CreditsLimit     float64
	CreditsRemaining float64
	DaysUntilReset   int
	SubscriptionName string
	LastUpdated      time.Time
	Error            error
}

// CreditsUpdateMsg signals credits have been fetched
type CreditsUpdateMsg struct {
	Info CreditsInfo
}

const kiroVersion = "0.6.0"

// fetchCreditsInfo fetches credit information from Kiro API
func fetchCreditsInfo() CreditsInfo {
	tokenInfo := getTokenInfo()
	if !tokenInfo.Present {
		return CreditsInfo{Error: fmt.Errorf("no token")}
	}

	// Read full token for access token
	homeDir, _ := os.UserHomeDir()
	tokenPath := filepath.Join(homeDir, ".aws", "sso", "cache", "kiro-auth-token.json")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return CreditsInfo{Error: err}
	}

	var token struct {
		AccessToken string `json:"accessToken"`
		ProfileArn  string `json:"profileArn"`
	}
	if err := json.Unmarshal(data, &token); err != nil {
		return CreditsInfo{Error: err}
	}

	// Build request
	baseURL := "https://codewhisperer.us-east-1.amazonaws.com/getUsageLimits"
	req, err := http.NewRequest("GET", baseURL, nil)
	if err != nil {
		return CreditsInfo{Error: err}
	}

	q := req.URL.Query()
	if token.ProfileArn != "" {
		q.Add("profileArn", token.ProfileArn)
	}
	q.Add("origin", "AI_EDITOR")
	q.Add("resourceType", "AGENTIC_REQUEST")
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("KiroIDE-%s-%s", kiroVersion, runtime.GOOS))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return CreditsInfo{Error: err}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return CreditsInfo{Error: fmt.Errorf("API error %d", resp.StatusCode)}
	}

	var usageResp struct {
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
	if err := json.Unmarshal(body, &usageResp); err != nil {
		return CreditsInfo{Error: err}
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

	info := CreditsInfo{
		DaysUntilReset:   daysUntilReset,
		SubscriptionName: usageResp.SubscriptionInfo.SubscriptionTitle,
		LastUpdated:      time.Now(),
	}

	if len(usageResp.UsageBreakdownList) > 0 {
		b := usageResp.UsageBreakdownList[0]
		info.CreditsUsed = b.CurrentUsageWithPrecision
		if info.CreditsUsed == 0 {
			info.CreditsUsed = b.CurrentUsage
		}
		info.CreditsLimit = b.UsageLimitWithPrecision
		if info.CreditsLimit == 0 {
			info.CreditsLimit = b.UsageLimit
		}
		info.CreditsRemaining = info.CreditsLimit - info.CreditsUsed
	}

	return info
}

// fetchCreditsCmd returns a command that fetches credits
func fetchCreditsCmd() tea.Cmd {
	return func() tea.Msg {
		return CreditsUpdateMsg{Info: fetchCreditsInfo()}
	}
}

// DashboardKeyMap defines key bindings for the dashboard
type DashboardKeyMap struct {
	Tab        key.Binding
	Menu       key.Binding
	Quit       key.Binding
	Help       key.Binding
	ScrollUp   key.Binding
	ScrollDown key.Binding
	PageUp     key.Binding
	PageDown   key.Binding
	Top        key.Binding
	Bottom     key.Binding
}

func DefaultDashboardKeyMap() DashboardKeyMap {
	return DashboardKeyMap{
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "switch panel"),
		),
		Menu: key.NewBinding(
			key.WithKeys("m", "esc"),
			key.WithHelp("m/esc", "menu"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "stop server"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		ScrollUp: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "scroll up"),
		),
		ScrollDown: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "scroll down"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("pgup", "ctrl+u"),
			key.WithHelp("pgup", "page up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("pgdown", "ctrl+d"),
			key.WithHelp("pgdn", "page down"),
		),
		Top: key.NewBinding(
			key.WithKeys("home", "g"),
			key.WithHelp("home", "top"),
		),
		Bottom: key.NewBinding(
			key.WithKeys("end", "G"),
			key.WithHelp("end", "bottom"),
		),
	}
}

func (k DashboardKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.ScrollUp, k.ScrollDown, k.Tab, k.Menu, k.Quit}
}

func (k DashboardKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.ScrollUp, k.ScrollDown, k.PageUp, k.PageDown},
		{k.Top, k.Bottom},
		{k.Tab, k.Menu, k.Help, k.Quit},
	}
}

// Model represents the server dashboard
type Model struct {
	session         SessionModel
	logViewer       LogViewerModel
	help            help.Model
	keys            DashboardKeyMap
	focusedPanel    FocusedPanel
	width           int
	height          int
	port            string
	server          *http.Server
	logger          *logger.Logger
	quitting        bool
	showFullHelp    bool
	tokenExpiry     time.Time
	credits         CreditsInfo
	creditsProgress progress.Model
	serverRunning   bool
}


// New creates a new dashboard model
func New(port string, tokenExpiry time.Time, lg *logger.Logger) Model {
	h := help.New()
	h.ShowAll = false

	// Initialize progress bar with custom styling
	prog := progress.New(
		progress.WithScaledGradient("#7D56F4", "#6BFF6B"),
		progress.WithWidth(30),
		progress.WithoutPercentage(),
	)

	// Initialize log viewer with focus enabled from the start
	logViewer := NewLogViewerModel(80, 20)
	logViewer.SetFocused(true)

	// Initialize session model with token expiry
	session := NewSessionModel(port)
	session.SetTokenExpiry(tokenExpiry)

	return Model{
		session:         session,
		logViewer:       logViewer,
		help:            h,
		keys:            DefaultDashboardKeyMap(),
		focusedPanel:    PanelLogs,
		port:            port,
		logger:          lg,
		tokenExpiry:     tokenExpiry,
		creditsProgress: prog,
	}
}

// Init initializes the dashboard
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		tickCmd(),
		fetchCreditsCmd(),
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

// Update handles messages
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Menu):
			// Go to menu without stopping the server
			return m, func() tea.Msg { return GoToMenuMsg{} }

		case key.Matches(msg, m.keys.Quit):
			m.quitting = true
			// Graceful shutdown
			if m.server != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				m.server.Shutdown(ctx)
			}
			// Return to menu instead of quitting the app
			return m, func() tea.Msg { return BackToMenuMsg{} }

		case key.Matches(msg, m.keys.Tab):
			// Forward Tab to logViewer to switch between list and detail pane
			var cmd tea.Cmd
			m.logViewer, cmd = m.logViewer.Update(msg)
			cmds = append(cmds, cmd)

		case key.Matches(msg, m.keys.Help):
			m.showFullHelp = !m.showFullHelp
			m.help.ShowAll = m.showFullHelp

		default:
			// Forward to focused panel
			if m.focusedPanel == PanelLogs {
				var cmd tea.Cmd
				m.logViewer, cmd = m.logViewer.Update(msg)
				cmds = append(cmds, cmd)
			}
		}

	case logger.LogEntryMsg:
		// Add to log viewer
		m.logViewer.AddEntry(msg.Entry)
		// Update request count for REQ type
		if msg.Entry.Type == logger.LogTypeReq {
			m.session.IncrementRequests()
		}
		// Refresh credits after each response (credits were consumed)
		if msg.Entry.Type == logger.LogTypeRes {
			cmds = append(cmds, fetchCreditsCmd())
		}

	case ServerStartedMsg:
		m.serverRunning = true

	case ServerStoppedMsg:
		m.serverRunning = false
		if msg.Err != nil {
			m.logViewer.AddEntry(logger.LogEntry{
				Timestamp: time.Now(),
				Type:      logger.LogTypeErr,
				Preview:   fmt.Sprintf("Server stopped: %v", msg.Err),
			})
		}

	case TickMsg:
		// Update session display (token expiry countdown)
		var cmd tea.Cmd
		m.session, cmd = m.session.Update(msg)
		cmds = append(cmds, cmd)
		cmds = append(cmds, tickCmd())

	case CreditsUpdateMsg:
		m.credits = msg.Info

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateLayout()
	}

	return m, tea.Batch(cmds...)
}

// updateLayout recalculates component sizes
func (m *Model) updateLayout() {
	// Reserve space for borders and help
	contentWidth := m.width - 4
	helpHeight := 2
	sessionHeight := 8 // Fixed session panel height

	// Session panel gets fixed height
	m.session.SetWidth(contentWidth)

	// Log viewer gets remaining space
	logHeight := m.height - sessionHeight - helpHeight - 6 // borders, spacing
	if logHeight < 5 {
		logHeight = 5
	}
	m.logViewer.SetSize(contentWidth, logHeight)
}

// View renders the dashboard
func (m Model) View() string {
	if m.quitting {
		return ""
	}

	// Styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#7D56F4"))

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#383838")).
		Padding(0, 1)

	focusedBoxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7D56F4")).
		Padding(0, 1)

	// Status panel (always visible at top)
	statusPanel := renderStatusPanel(m.serverRunning, m.port, m.credits, m.creditsProgress)

	// Log panel
	logStyle := boxStyle
	if m.focusedPanel == PanelLogs {
		logStyle = focusedBoxStyle
	}

	// Add scroll indicator
	scrollInfo := ""
	if !m.logViewer.AtBottom() && m.logViewer.EntryCount() > 0 {
		scrollInfo = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#626262")).
			Render(" (scroll: " + m.logViewer.ScrollInfo() + ")")
	}

	logTitle := titleStyle.Render("Logs") + scrollInfo
	logBox := lipgloss.JoinVertical(lipgloss.Left,
		logTitle,
		logStyle.Width(m.width-4).Render(m.logViewer.View()),
	)

	// Help bar
	helpView := m.help.View(m.keys)

	// Version in bottom right
	versionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#626262"))
	versionText := versionStyle.Render("v" + Version)

	// Build footer with help and version
	footerWidth := m.width - 4
	helpWidth := lipgloss.Width(helpView)
	versionWidth := lipgloss.Width(versionText)
	padding := footerWidth - helpWidth - versionWidth
	if padding < 1 {
		padding = 1
	}
	footer := helpView + strings.Repeat(" ", padding) + versionText

	// Combine all panels
	content := lipgloss.JoinVertical(lipgloss.Left,
		statusPanel,
		"",
		logBox,
		"",
		footer,
	)

	// Wrap in container that fills the terminal
	containerStyle := lipgloss.NewStyle().
		Width(m.width).
		Height(m.height)

	return containerStyle.Render(content)
}

// SetServer sets the HTTP server reference for graceful shutdown
func (m *Model) SetServer(server *http.Server) {
	m.server = server
}

// GetLogger returns the logger instance
func (m Model) GetLogger() *logger.Logger {
	return m.logger
}

// SetTokenExpiry updates the token expiry time
func (m *Model) SetTokenExpiry(expiry time.Time) {
	m.tokenExpiry = expiry
	m.session.SetTokenExpiry(expiry)
}

// TokenInfo holds token information for display
type TokenInfo struct {
	Present    bool
	AuthMethod string
	Provider   string
	ExpiresAt  time.Time
	Region     string
	StartUrl   string
}

// ClaudeConfigStatus holds Claude Code configuration status
type ClaudeConfigStatus struct {
	FileExists bool
	Kiro2ccSet bool
	ApiKeyAuth bool
}

// getTokenInfo reads token information for display
func getTokenInfo() TokenInfo {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return TokenInfo{}
	}

	tokenPath := filepath.Join(homeDir, ".aws", "sso", "cache", "kiro-auth-token.json")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return TokenInfo{}
	}

	var token struct {
		AuthMethod string `json:"authMethod"`
		Provider   string `json:"provider"`
		ExpiresAt  string `json:"expiresAt"`
		Region     string `json:"region"`
		StartUrl   string `json:"startUrl"`
	}
	if err := json.Unmarshal(data, &token); err != nil {
		return TokenInfo{}
	}

	expiresAt, _ := time.Parse(time.RFC3339, token.ExpiresAt)

	return TokenInfo{
		Present:    true,
		AuthMethod: token.AuthMethod,
		Provider:   token.Provider,
		ExpiresAt:  expiresAt,
		Region:     token.Region,
		StartUrl:   token.StartUrl,
	}
}

// getClaudeConfigStatus checks Claude Code configuration
func getClaudeConfigStatus() ClaudeConfigStatus {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ClaudeConfigStatus{}
	}

	claudePath := filepath.Join(homeDir, ".claude.json")
	data, err := os.ReadFile(claudePath)
	if err != nil {
		return ClaudeConfigStatus{FileExists: false}
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return ClaudeConfigStatus{FileExists: true}
	}

	status := ClaudeConfigStatus{FileExists: true}

	if kiro2cc, ok := config["kiro2cc"].(bool); ok && kiro2cc {
		status.Kiro2ccSet = true
	}

	if oauthAccount, ok := config["oauthAccount"].(map[string]interface{}); ok {
		if authType, ok := oauthAccount["type"].(string); ok && authType == "api_key" {
			status.ApiKeyAuth = true
		}
	}

	return status
}

// renderStatusPanel renders the status panel
func renderStatusPanel(serverRunning bool, serverPort string, credits CreditsInfo, creditsProgress progress.Model) string {
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FAFAFA")).Bold(true)
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6BFF6B")).Bold(true)
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFAA00")).Bold(true)
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B")).Bold(true)

	var statusLines []string
	tokenInfo := getTokenInfo()
	claudeConfig := getClaudeConfigStatus()

	// Server status
	if serverRunning {
		statusLines = append(statusLines,
			labelStyle.Render("Server: ")+okStyle.Render("✓ Running on port ")+valueStyle.Render(serverPort))
	} else {
		statusLines = append(statusLines,
			labelStyle.Render("Server: ")+errStyle.Render("✗ Not running"))
	}

	// Token/Credentials status
	if tokenInfo.Present {
		sessionType := "Unknown"
		if tokenInfo.AuthMethod == "IdC" {
			sessionType = "Enterprise SSO"
		} else if tokenInfo.Provider == "github" {
			sessionType = "GitHub"
		} else if tokenInfo.Provider == "google" {
			sessionType = "Google"
		} else if tokenInfo.Provider == "aws" || tokenInfo.AuthMethod == "BuilderId" {
			sessionType = "AWS Builder ID"
		} else if tokenInfo.Provider != "" {
			sessionType = tokenInfo.Provider
		}

		var expiryStr string
		var expiryStyle lipgloss.Style
		if !tokenInfo.ExpiresAt.IsZero() {
			remaining := time.Until(tokenInfo.ExpiresAt)
			if remaining < 0 {
				expiryStr = "EXPIRED"
				expiryStyle = errStyle
			} else if remaining < 30*time.Minute {
				expiryStr = fmt.Sprintf("%dm", int(remaining.Minutes()))
				expiryStyle = warnStyle
			} else if remaining < time.Hour {
				expiryStr = fmt.Sprintf("%dm", int(remaining.Minutes()))
				expiryStyle = okStyle
			} else {
				expiryStr = fmt.Sprintf("%dh %dm", int(remaining.Hours()), int(remaining.Minutes())%60)
				expiryStyle = okStyle
			}
		} else {
			expiryStr = "N/A"
			expiryStyle = warnStyle
		}

		statusLines = append(statusLines,
			labelStyle.Render("Credentials: ")+okStyle.Render("✓ ")+valueStyle.Render(sessionType)+
				labelStyle.Render("  Expires: ")+expiryStyle.Render(expiryStr))

		if tokenInfo.Region != "" {
			statusLines = append(statusLines,
				labelStyle.Render("Region: ")+valueStyle.Render(tokenInfo.Region))
		}
		if tokenInfo.StartUrl != "" {
			url := tokenInfo.StartUrl
			if len(url) > 45 {
				url = url[:42] + "..."
			}
			statusLines = append(statusLines,
				labelStyle.Render("SSO URL: ")+valueStyle.Render(url))
		}
	} else {
		statusLines = append(statusLines,
			labelStyle.Render("Credentials: ")+errStyle.Render("✗ Not logged in"))
	}

	// Claude Code config status
	if claudeConfig.FileExists {
		if claudeConfig.Kiro2ccSet && claudeConfig.ApiKeyAuth {
			statusLines = append(statusLines,
				labelStyle.Render("Claude Code: ")+okStyle.Render("✓ Configured for kiro2cc"))
		} else if claudeConfig.Kiro2ccSet {
			statusLines = append(statusLines,
				labelStyle.Render("Claude Code: ")+warnStyle.Render("⚠ Partial config"))
		} else {
			statusLines = append(statusLines,
				labelStyle.Render("Claude Code: ")+warnStyle.Render("⚠ Not configured"))
		}
	} else {
		statusLines = append(statusLines,
			labelStyle.Render("Claude Code: ")+errStyle.Render("✗ Not installed"))
	}

	// Credits status with progress bar
	if credits.CreditsLimit > 0 && credits.Error == nil {
		// Calculate percentage remaining
		percentUsed := credits.CreditsUsed / credits.CreditsLimit
		percentRemaining := 1.0 - percentUsed
		if percentRemaining < 0 {
			percentRemaining = 0
		}

		// Choose color based on remaining percentage
		var progressStyle lipgloss.Style
		if percentRemaining < 0.1 {
			progressStyle = errStyle
		} else if percentRemaining < 0.3 {
			progressStyle = warnStyle
		} else {
			progressStyle = okStyle
		}

		// Format credits info
		creditsStr := fmt.Sprintf("%.0f/%.0f", credits.CreditsRemaining, credits.CreditsLimit)
		planName := credits.SubscriptionName
		if planName == "" {
			planName = "Kiro"
		}

		// Reset text after progress bar
		resetStr := ""
		if credits.DaysUntilReset > 0 {
			resetStr = fmt.Sprintf(" (resets in %dd)", credits.DaysUntilReset)
		}

		// Render progress bar (shows remaining)
		progressBar := creditsProgress.ViewAs(percentRemaining)

		statusLines = append(statusLines,
			labelStyle.Render("Credits: ")+progressStyle.Render(creditsStr+" ")+progressBar+
				labelStyle.Render(" "+planName)+labelStyle.Render(resetStr))
	} else if credits.Error != nil && tokenInfo.Present {
		// Show error if logged in
		statusLines = append(statusLines,
			labelStyle.Render("Credits: ")+warnStyle.Render("⚠ "+credits.Error.Error()))
	} else if tokenInfo.Present && credits.LastUpdated.IsZero() {
		// Show loading if logged in but credits not yet fetched
		statusLines = append(statusLines,
			labelStyle.Render("Credits: ")+labelStyle.Render("Loading..."))
	}

	// Determine border color
	var borderColor lipgloss.Color
	if serverRunning && tokenInfo.Present && claudeConfig.Kiro2ccSet {
		borderColor = lipgloss.Color("#6BFF6B")
	} else if tokenInfo.Present || claudeConfig.FileExists {
		borderColor = lipgloss.Color("#FFAA00")
	} else {
		borderColor = lipgloss.Color("#FF6B6B")
	}

	bubbleStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1)

	return bubbleStyle.Render(strings.Join(statusLines, "\n"))
}
