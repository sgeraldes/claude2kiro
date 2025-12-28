package menu

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Version is set at build time via ldflags
var Version = "dev"

// TokenInfo holds token information for display
type TokenInfo struct {
	Present    bool
	AuthMethod string
	Provider   string
	ExpiresAt  time.Time
	Region     string
	StartUrl   string
	ProfileArn string
}

// ClaudeConfigStatus holds Claude Code configuration status
type ClaudeConfigStatus struct {
	FileExists   bool
	Kiro2ccSet   bool
	ApiKeyAuth   bool
	Onboarded    bool
}

// StatusInfo holds all status information for the dashboard
type StatusInfo struct {
	Token        TokenInfo
	ClaudeConfig ClaudeConfigStatus
	ServerPort   string // Empty if not running
}

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
		ProfileArn string `json:"profileArn"`
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
		ProfileArn: token.ProfileArn,
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

	if onboarded, ok := config["hasCompletedOnboarding"].(bool); ok && onboarded {
		status.Onboarded = true
	}

	if oauthAccount, ok := config["oauthAccount"].(map[string]interface{}); ok {
		if authType, ok := oauthAccount["type"].(string); ok && authType == "api_key" {
			status.ApiKeyAuth = true
		}
	}

	return status
}

// MenuAction represents a menu selection
type MenuAction int

const (
	ActionLogin MenuAction = iota
	ActionServer
	ActionDashboard
	ActionRefreshToken
	ActionViewToken
	ActionExportEnv
	ActionConfigureClaude
	ActionViewCredits
	ActionLogout
	ActionQuit
)

// MenuActionMsg signals a menu action was selected
type MenuActionMsg struct {
	Action MenuAction
}

// MenuItem represents a menu entry
type MenuItem struct {
	action      MenuAction
	title       string
	description string
}

func (i MenuItem) Title() string       { return i.title }
func (i MenuItem) Description() string { return i.description }
func (i MenuItem) FilterValue() string { return i.title }

// ItemDelegate handles rendering of menu items
type ItemDelegate struct {
	styles        ItemStyles
	shortHelpFunc func() []key.Binding
	fullHelpFunc  func() [][]key.Binding
}

type ItemStyles struct {
	Normal   lipgloss.Style
	Selected lipgloss.Style
	Desc     lipgloss.Style
	DescDim  lipgloss.Style
}

func DefaultItemStyles() ItemStyles {
	return ItemStyles{
		Normal: lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FAFAFA")).
			PaddingLeft(2),
		Selected: lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7D56F4")).
			Bold(true).
			PaddingLeft(2),
		Desc: lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A0A0A0")).
			PaddingLeft(4),
		DescDim: lipgloss.NewStyle().
			Foreground(lipgloss.Color("#626262")).
			PaddingLeft(4),
	}
}

func NewItemDelegate() ItemDelegate {
	return ItemDelegate{
		styles: DefaultItemStyles(),
	}
}

func (d ItemDelegate) Height() int                             { return 2 }
func (d ItemDelegate) Spacing() int                            { return 0 }
func (d ItemDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d ItemDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	i, ok := item.(MenuItem)
	if !ok {
		return
	}

	var title, desc string
	if index == m.Index() {
		title = d.styles.Selected.Render("▸ " + i.Title())
		desc = d.styles.Desc.Render(i.Description())
	} else {
		title = d.styles.Normal.Render("  " + i.Title())
		desc = d.styles.DescDim.Render(i.Description())
	}

	fmt.Fprintf(w, "%s\n%s", title, desc)
}

// Model represents the menu component
type Model struct {
	list            list.Model
	width           int
	height          int
	quitting        bool
	status          string
	statusErr       bool
	tokenInfo       TokenInfo
	claudeConfig    ClaudeConfigStatus
	serverRunning   bool
	serverPort      string
	credits         CreditsInfo
	creditsProgress progress.Model
}

// New creates a new menu model
func New(width, height int) Model {
	items := []list.Item{
		MenuItem{ActionLogin, "Login", "Authenticate with Kiro (social or AWS Builder ID)"},
		MenuItem{ActionServer, "Start Server", "Launch the API proxy server"},
		MenuItem{ActionRefreshToken, "Refresh Token", "Refresh the access token"},
		MenuItem{ActionViewToken, "View Token", "Display current token information"},
		MenuItem{ActionConfigureClaude, "Create Launch Script", "Create claude-kiro script to run Claude with proxy"},
		MenuItem{ActionViewCredits, "View Credits", "Open Kiro billing page in browser"},
		MenuItem{ActionLogout, "Logout", "Clear saved credentials"},
		MenuItem{ActionQuit, "Quit", "Exit kiro2cc"},
	}

	delegate := NewItemDelegate()

	// Calculate list dimensions (leave room for title, help, and potential status)
	listWidth := width - 8
	listHeight := height - 12

	l := list.New(items, delegate, listWidth, listHeight)
	l.Title = ""
	l.SetShowStatusBar(false)
	l.SetShowFilter(false)
	l.SetShowHelp(true)
	l.SetShowPagination(false)

	// Style the help
	l.Styles.HelpStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#626262")).
		MarginTop(1)

	// Disable filtering and hide filter input completely
	l.SetFilteringEnabled(false)
	l.FilterInput.Blur()
	l.FilterInput.Prompt = ""
	l.FilterInput.Cursor.SetChar(" ")

	// Initialize progress bar with custom styling
	prog := progress.New(
		progress.WithScaledGradient("#7D56F4", "#6BFF6B"),
		progress.WithWidth(30),
		progress.WithoutPercentage(),
	)

	return Model{
		list:            l,
		width:           width,
		height:          height,
		tokenInfo:       getTokenInfo(),
		claudeConfig:    getClaudeConfigStatus(),
		creditsProgress: prog,
	}
}

// StatusTickMsg triggers a status refresh
type StatusTickMsg time.Time

// Init initializes the menu
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		fetchCreditsCmd(),
		tea.Tick(30*time.Second, func(t time.Time) tea.Msg {
			return StatusTickMsg(t)
		}),
	)
}

// Update handles messages
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "d":
			// Go to dashboard if server is running
			if m.serverRunning {
				return m, func() tea.Msg {
					return MenuActionMsg{Action: ActionDashboard}
				}
			}
		case "enter":
			if item, ok := m.list.SelectedItem().(MenuItem); ok {
				return m, func() tea.Msg {
					return MenuActionMsg{Action: item.action}
				}
			}
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(msg.Width-8, msg.Height-12)

	case StatusTickMsg:
		// Refresh token info and schedule next tick (but not credits - those refresh on requests)
		m.tokenInfo = getTokenInfo()
		m.claudeConfig = getClaudeConfigStatus()
		return m, tea.Tick(30*time.Second, func(t time.Time) tea.Msg {
			return StatusTickMsg(t)
		})

	case CreditsUpdateMsg:
		m.credits = msg.Info
		return m, nil
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// View renders the menu
func (m Model) View() string {
	if m.quitting {
		return ""
	}

	// Main box style with prominent border
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7D56F4")).
		Padding(1, 2).
		Width(m.width - 4).
		Height(m.height - 4)

	// Header
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#7D56F4"))

	subtitleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#A0A0A0"))

	header := lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("kiro2cc"),
		subtitleStyle.Render("Claude Code proxy for Kiro subscriptions"),
		"",
	)

	// Status dashboard panel
	var statusPanel string
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FAFAFA")).Bold(true)
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6BFF6B")).Bold(true)
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFAA00")).Bold(true)
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B")).Bold(true)

	var statusLines []string

	// Server status
	if m.serverRunning {
		statusLines = append(statusLines,
			labelStyle.Render("Server: ")+okStyle.Render("✓ Running on port ")+valueStyle.Render(m.serverPort)+
				labelStyle.Render(" (press 'd' for dashboard)"))
	} else {
		statusLines = append(statusLines,
			labelStyle.Render("Server: ")+errStyle.Render("✗ Not running"))
	}

	// Token/Credentials status
	if m.tokenInfo.Present {
		// Session type
		sessionType := "Unknown"
		if m.tokenInfo.AuthMethod == "IdC" {
			sessionType = "Enterprise SSO"
		} else if m.tokenInfo.Provider == "github" {
			sessionType = "GitHub"
		} else if m.tokenInfo.Provider == "google" {
			sessionType = "Google"
		} else if m.tokenInfo.Provider == "aws" || m.tokenInfo.AuthMethod == "BuilderId" {
			sessionType = "AWS Builder ID"
		} else if m.tokenInfo.Provider != "" {
			sessionType = m.tokenInfo.Provider
		}

		// Expiry time
		var expiryStr string
		var expiryStyle lipgloss.Style
		if !m.tokenInfo.ExpiresAt.IsZero() {
			remaining := time.Until(m.tokenInfo.ExpiresAt)
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

		// Region and Start URL (for IdC)
		if m.tokenInfo.Region != "" {
			statusLines = append(statusLines,
				labelStyle.Render("Region: ")+valueStyle.Render(m.tokenInfo.Region))
		}
		if m.tokenInfo.StartUrl != "" {
			// Truncate long URLs
			url := m.tokenInfo.StartUrl
			if len(url) > 50 {
				url = url[:47] + "..."
			}
			statusLines = append(statusLines,
				labelStyle.Render("SSO URL: ")+valueStyle.Render(url))
		}
	} else {
		statusLines = append(statusLines,
			labelStyle.Render("Credentials: ")+errStyle.Render("✗ Not logged in"))
	}

	// Claude Code config status
	if m.claudeConfig.FileExists {
		if m.claudeConfig.Kiro2ccSet && m.claudeConfig.ApiKeyAuth {
			statusLines = append(statusLines,
				labelStyle.Render("Claude Code: ")+okStyle.Render("✓ Configured for kiro2cc"))
		} else if m.claudeConfig.Kiro2ccSet {
			statusLines = append(statusLines,
				labelStyle.Render("Claude Code: ")+warnStyle.Render("⚠ Partial config (run Create Launch Script)"))
		} else {
			statusLines = append(statusLines,
				labelStyle.Render("Claude Code: ")+warnStyle.Render("⚠ Not configured (run Create Launch Script)"))
		}
	} else {
		statusLines = append(statusLines,
			labelStyle.Render("Claude Code: ")+errStyle.Render("✗ Not installed"))
	}

	// Credits status with progress bar
	if m.credits.CreditsLimit > 0 && m.credits.Error == nil {
		// Calculate percentage remaining
		percentUsed := m.credits.CreditsUsed / m.credits.CreditsLimit
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
		creditsStr := fmt.Sprintf("%.0f/%.0f", m.credits.CreditsRemaining, m.credits.CreditsLimit)
		planName := m.credits.SubscriptionName
		if planName == "" {
			planName = "Kiro"
		}

		// Reset text after progress bar
		resetStr := ""
		if m.credits.DaysUntilReset > 0 {
			resetStr = fmt.Sprintf(" (resets in %dd)", m.credits.DaysUntilReset)
		}

		// Render progress bar (shows remaining)
		progressBar := m.creditsProgress.ViewAs(percentRemaining)

		statusLines = append(statusLines,
			labelStyle.Render("Credits: ")+progressStyle.Render(creditsStr+" ")+progressBar+
				labelStyle.Render(" "+planName)+labelStyle.Render(resetStr))
	} else if m.credits.Error != nil && m.tokenInfo.Present {
		// Show error if logged in
		statusLines = append(statusLines,
			labelStyle.Render("Credits: ")+warnStyle.Render("⚠ "+m.credits.Error.Error()))
	} else if m.tokenInfo.Present && m.credits.LastUpdated.IsZero() {
		// Show loading if logged in but credits not yet fetched
		statusLines = append(statusLines,
			labelStyle.Render("Credits: ")+labelStyle.Render("Loading..."))
	}

	// Build status panel with border
	var borderColor lipgloss.Color
	if m.serverRunning && m.tokenInfo.Present && m.claudeConfig.Kiro2ccSet {
		borderColor = lipgloss.Color("#6BFF6B")
	} else if m.tokenInfo.Present || m.claudeConfig.FileExists || m.serverRunning {
		borderColor = lipgloss.Color("#FFAA00")
	} else {
		borderColor = lipgloss.Color("#FF6B6B")
	}

	bubbleStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1).
		MarginBottom(1)

	statusPanel = bubbleStyle.Render(strings.Join(statusLines, "\n"))

	// Render menu items manually (to avoid the filter cursor box)
	var menuLines []string
	items := m.list.Items()
	selectedIdx := m.list.Index()

	selectedStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#7D56F4")).
		Bold(true).
		PaddingLeft(2)
	normalStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FAFAFA")).
		PaddingLeft(2)
	descStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#A0A0A0")).
		PaddingLeft(4)
	descDimStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#626262")).
		PaddingLeft(4)

	for i, item := range items {
		if menuItem, ok := item.(MenuItem); ok {
			var title, desc string
			if i == selectedIdx {
				title = selectedStyle.Render("▸ " + menuItem.Title())
				desc = descStyle.Render(menuItem.Description())
			} else {
				title = normalStyle.Render("  " + menuItem.Title())
				desc = descDimStyle.Render(menuItem.Description())
			}
			menuLines = append(menuLines, title)
			menuLines = append(menuLines, desc)
		}
	}
	menuContent := strings.Join(menuLines, "\n")

	// Help text
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#626262")).
		MarginTop(1)
	helpText := helpStyle.Render("↑/k up • ↓/j down • q quit • ? more")

	// Status bar at bottom (fixed position)
	var statusBar string
	if m.status != "" {
		var statusStyle lipgloss.Style
		if m.statusErr {
			statusStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#3D1F1F")).
				Foreground(lipgloss.Color("#FF6B6B")).
				Padding(0, 2).
				Bold(true)
		} else {
			statusStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#1F3D1F")).
				Foreground(lipgloss.Color("#6BFF6B")).
				Padding(0, 2).
				Bold(true)
		}
		statusBar = statusStyle.Render(m.status)
	}

	// Version in bottom right
	versionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#626262"))
	versionText := versionStyle.Render("v" + Version)

	// Build footer with status and version
	footerWidth := m.width - 8
	var footer string
	if statusBar != "" {
		// Status on left, version on right
		statusWidth := lipgloss.Width(statusBar)
		padding := footerWidth - statusWidth - len("v"+Version)
		if padding < 1 {
			padding = 1
		}
		footer = statusBar + strings.Repeat(" ", padding) + versionText
	} else {
		// Just version on right
		padding := footerWidth - len("v"+Version)
		if padding < 0 {
			padding = 0
		}
		footer = strings.Repeat(" ", padding) + versionText
	}

	// Build content with fixed structure
	content := lipgloss.JoinVertical(lipgloss.Left,
		header,
		statusPanel,
		menuContent,
		helpText,
		"",
		footer,
	)

	return boxStyle.Render(content)
}

// SetSize updates the menu dimensions
func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
	m.list.SetSize(width-8, height-12)
}

// SetStatus sets the status message
func (m *Model) SetStatus(msg string, isError bool) {
	m.status = msg
	m.statusErr = isError
}

// ClearStatus clears the status message
func (m *Model) ClearStatus() {
	m.status = ""
	m.statusErr = false
}

// RefreshTokenInfo reloads token information from disk
func (m *Model) RefreshTokenInfo() {
	m.tokenInfo = getTokenInfo()
	m.claudeConfig = getClaudeConfigStatus()
}

// SetServerRunning sets the server running state and updates menu items
func (m *Model) SetServerRunning(running bool, port string) {
	m.serverRunning = running
	m.serverPort = port

	// Update the menu item text based on server state
	items := m.list.Items()
	for i, item := range items {
		if menuItem, ok := item.(MenuItem); ok && menuItem.action == ActionServer {
			if running {
				items[i] = MenuItem{ActionServer, "View Dashboard", "View the running server dashboard"}
			} else {
				items[i] = MenuItem{ActionServer, "Start Server", "Launch the API proxy server"}
			}
			break
		}
	}
	m.list.SetItems(items)
}

// IsServerRunning returns whether the server is running
func (m *Model) IsServerRunning() bool {
	return m.serverRunning
}

// GetServerPort returns the server port
func (m *Model) GetServerPort() string {
	return m.serverPort
}
