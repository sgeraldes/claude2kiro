package menu

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sgeraldes/claude2kiro/cmd"
)

// Version is set at build time via ldflags
var Version = "dev"

// renderBanner creates the ASCII art header with Kiro ghost
func renderBanner() string {
	// Colors
	purple := lipgloss.Color("#7D56F4")
	white := lipgloss.Color("#FAFAFA")
	dim := lipgloss.Color("#626262")
	orange := lipgloss.Color("#E07B53")

	p := lipgloss.NewStyle().Foreground(purple).Bold(true)
	w := lipgloss.NewStyle().Foreground(white)
	d := lipgloss.NewStyle().Foreground(dim)
	o := lipgloss.NewStyle().Foreground(orange).Bold(true)

	// Kiro phantom ghost logo
	ghost := []string{
		w.Render("        @@@@@@@@@#      "),
		w.Render("      %@@@@@@@@@@@@+    "),
		w.Render("     @@@@@@@@@@@@@@@%   "),
		w.Render("    .@@@@@@@  @@  @@@   "),
		w.Render("    +@@@@@@@  @@  @@@:  "),
		w.Render("    %@@@@@@@@@@@@@@@@=  "),
		w.Render("    @@@@@@@@@@@@@@@@@-  "),
		w.Render("   %@@@@@@@@@@@@@@@@@   "),
		w.Render("  =@@@@@@@@@@@@@@@@@@   "),
		w.Render("   @% @@@@@@@@@@@@@@    "),
		w.Render("     :@@@@@@@@@@@@@     "),
		w.Render("      @@@@@@ @@@@*      "),
	}

	// Claude2Kiro title - CLAUDE in orange
	title := []string{
		o.Render(" ██████╗██╗      █████╗ ██╗   ██╗██████╗ ███████╗"),
		o.Render("██╔════╝██║     ██╔══██╗██║   ██║██╔══██╗██╔════╝"),
		o.Render("██║     ██║     ███████║██║   ██║██║  ██║█████╗  "),
		o.Render("██║     ██║     ██╔══██║██║   ██║██║  ██║██╔══╝  "),
		o.Render("╚██████╗███████╗██║  ██║╚██████╔╝██████╔╝███████╗"),
		o.Render(" ╚═════╝╚══════╝╚═╝  ╚═╝ ╚═════╝ ╚═════╝ ╚══════╝"),
	}

	// 2KIRO - 2 in white, KIRO in purple
	title2 := []string{
		d.Render("        ") + w.Render("██████╗ ") + p.Render("██╗  ██╗██╗██████╗  ██████╗ "),
		d.Render("        ") + w.Render("╚════██╗") + p.Render("██║ ██╔╝██║██╔══██╗██╔═══██╗"),
		d.Render("        ") + w.Render(" █████╔╝") + p.Render("█████╔╝ ██║██████╔╝██║   ██║"),
		d.Render("        ") + w.Render("██╔═══╝ ") + p.Render("██╔═██╗ ██║██╔══██╗██║   ██║"),
		d.Render("        ") + w.Render("███████╗") + p.Render("██║  ██╗██║██║  ██║╚██████╔╝"),
		d.Render("        ") + w.Render("╚══════╝") + p.Render("╚═╝  ╚═╝╚═╝╚═╝  ╚═╝ ╚═════╝ "),
	}

	subtitle := d.Render("Use Claude Code with your Kiro subscription")

	// Combine: ghost on left, title on right
	lines := []string{
		ghost[0] + " " + title[0],
		ghost[1] + " " + title[1],
		ghost[2] + " " + title[2],
		ghost[3] + " " + title[3],
		ghost[4] + " " + title[4],
		ghost[5] + " " + title[5],
		ghost[6] + " " + title2[0],
		ghost[7] + " " + title2[1],
		ghost[8] + " " + title2[2],
		ghost[9] + " " + title2[3],
		ghost[10] + " " + title2[4],
		ghost[11] + " " + title2[5],
		"",
		"  " + subtitle,
		"",
	}

	return strings.Join(lines, "\n")
}

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
	FileExists     bool
	Claude2KiroSet bool
	ApiKeyAuth     bool
	Onboarded      bool
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

// fetchCreditsInfo fetches credit information using the shared cmd function
func fetchCreditsInfo() CreditsInfo {
	info := cmd.GetCreditsInfo()
	return CreditsInfo{
		CreditsUsed:      info.CreditsUsed,
		CreditsLimit:     info.CreditsLimit,
		CreditsRemaining: info.CreditsRemaining,
		DaysUntilReset:   info.DaysUntilReset,
		SubscriptionName: info.SubscriptionName,
		LastUpdated:      time.Now(),
		Error:            info.Error,
	}
}

// fetchCreditsCmd returns a command that fetches credits
func fetchCreditsCmd() tea.Cmd {
	return func() tea.Msg {
		// Check if this is a BuilderId account - skip credits API entirely
		token, err := cmd.GetToken()
		if err == nil && token.Provider == "BuilderId" {
			return CreditsUpdateMsg{Info: CreditsInfo{
				SubscriptionName: "AWS Builder ID",
				LastUpdated:      time.Now(),
			}}
		}
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

	if claude2kiro, ok := config["claude2kiro"].(bool); ok && claude2kiro {
		status.Claude2KiroSet = true
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
	ActionConfigureClaude // Also used for Unconfigure (contextual)
	ActionLogout          // Also used for Login (contextual)
	ActionSettings
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

// Spinner frames for loading animation
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// SpinnerTickMsg triggers spinner animation
type SpinnerTickMsg time.Time

// spinnerTickCmd returns a command that triggers spinner animation
func spinnerTickCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg {
		return SpinnerTickMsg(t)
	})
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
	// Loading state for slow operations
	isLoading     bool
	loadingAction MenuAction
	loadingText   string
	spinnerFrame  int
}

// New creates a new menu model
func New(width, height int) Model {
	delegate := NewItemDelegate()

	// Calculate list dimensions (leave room for title, help, and potential status)
	listWidth := width - 8
	listHeight := height - 12

	// Create empty list initially - items will be populated by rebuildMenuItems
	l := list.New([]list.Item{}, delegate, listWidth, listHeight)
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

	m := Model{
		list:            l,
		width:           width,
		height:          height,
		tokenInfo:       getTokenInfo(),
		claudeConfig:    getClaudeConfigStatus(),
		creditsProgress: prog,
	}

	// Build menu items based on current state
	m.rebuildMenuItems()
	return m
}

// rebuildMenuItems rebuilds the menu items based on current state
func (m *Model) rebuildMenuItems() {
	var items []list.Item
	isLoggedIn := m.tokenInfo.Present

	// 1. Login (if not logged in)
	if !isLoggedIn {
		items = append(items, MenuItem{ActionLogin, "Login", "Authenticate with Kiro"})
	}

	// 2. Server action (contextual based on server state)
	if m.serverRunning {
		items = append(items, MenuItem{ActionDashboard, "View Dashboard", "View the running server dashboard"})
	} else if isLoggedIn {
		items = append(items, MenuItem{ActionServer, "Start Server", "Launch the API proxy server"})
	}

	// 3. Refresh Token (only when logged in)
	if isLoggedIn {
		items = append(items, MenuItem{ActionRefreshToken, "Refresh Token", "Refresh the access token"})
	}

	// 4. Configure/Unconfigure Claude (contextual)
	if m.claudeConfig.FileExists {
		if m.claudeConfig.Claude2KiroSet {
			items = append(items, MenuItem{ActionConfigureClaude, "Unconfigure Claude", "Remove Claude2Kiro settings"})
		} else {
			items = append(items, MenuItem{ActionConfigureClaude, "Configure Claude", "Configure Claude Code for Claude2Kiro"})
		}
	}

	// 5. Settings (always)
	items = append(items, MenuItem{ActionSettings, "Settings", "Configure Claude2Kiro options"})

	// 6. Logout (if logged in) - between Settings and Quit
	if isLoggedIn {
		items = append(items, MenuItem{ActionLogout, "Logout", "Clear saved credentials"})
	}

	// 7. Quit (always)
	items = append(items, MenuItem{ActionQuit, "Quit", "Exit Claude2Kiro"})

	m.list.SetItems(items)
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
		// Ignore key input while loading
		if m.isLoading {
			return m, nil
		}
		switch msg.String() {
		case "esc":
			// Esc does nothing in menu - use q or select Quit to exit
			return m, nil
		case "d":
			// Go to dashboard if server is running
			if m.serverRunning {
				return m, func() tea.Msg {
					return MenuActionMsg{Action: ActionDashboard}
				}
			}
		case "enter":
			if item, ok := m.list.SelectedItem().(MenuItem); ok {
				// Start loading state for slow operations (Configure/Unconfigure Claude)
				if item.action == ActionConfigureClaude {
					m.isLoading = true
					m.loadingAction = item.action
					m.spinnerFrame = 0
					// Determine action based on current config state
					if m.claudeConfig.Claude2KiroSet {
						m.loadingText = "Removing Claude2Kiro configuration..."
					} else {
						m.loadingText = "Configuring Claude Code..."
					}
					return m, tea.Batch(
						func() tea.Msg {
							return MenuActionMsg{Action: item.action}
						},
						spinnerTickCmd(),
					)
				}
				return m, func() tea.Msg {
					return MenuActionMsg{Action: item.action}
				}
			}
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		}

	case SpinnerTickMsg:
		if m.isLoading {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
			return m, spinnerTickCmd()
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(msg.Width-8, msg.Height-12)

	case StatusTickMsg:
		// Refresh token info and rebuild menu if state changed
		m.tokenInfo = getTokenInfo()
		m.claudeConfig = getClaudeConfigStatus()
		m.rebuildMenuItems()
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

	// Header with ASCII art banner
	header := renderBanner()

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
		// Session type - check Provider first, then AuthMethod
		sessionType := "Unknown"
		if m.tokenInfo.Provider == "BuilderId" {
			sessionType = "AWS Builder ID"
		} else if m.tokenInfo.Provider == "github" || m.tokenInfo.Provider == "GitHub" {
			sessionType = "GitHub"
		} else if m.tokenInfo.Provider == "google" || m.tokenInfo.Provider == "Google" {
			sessionType = "Google"
		} else if m.tokenInfo.AuthMethod == "IdC" {
			sessionType = "Enterprise SSO"
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

		// Region and Start URL (only for Enterprise SSO, not BuilderId)
		if m.tokenInfo.AuthMethod == "IdC" && m.tokenInfo.Provider != "BuilderId" {
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
		}
	} else {
		statusLines = append(statusLines,
			labelStyle.Render("Credentials: ")+errStyle.Render("✗ Not logged in"))
	}

	// Claude Code config status
	if m.claudeConfig.FileExists {
		if m.claudeConfig.Claude2KiroSet && m.claudeConfig.ApiKeyAuth {
			statusLines = append(statusLines,
				labelStyle.Render("Claude Code: ")+okStyle.Render("✓ Configured for Claude2Kiro"))
		} else if m.claudeConfig.Claude2KiroSet {
			statusLines = append(statusLines,
				labelStyle.Render("Claude Code: ")+warnStyle.Render("⚠ Partial config (run Configure Claude)"))
		} else {
			statusLines = append(statusLines,
				labelStyle.Render("Claude Code: ")+warnStyle.Render("⚠ Not configured (run Configure Claude)"))
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

		// Format credits info - show used/limit and remaining
		usedStr := fmt.Sprintf("%.1f/%.0f used", m.credits.CreditsUsed, m.credits.CreditsLimit)
		remainingStr := fmt.Sprintf("%.1f remaining", m.credits.CreditsRemaining)
		planName := m.credits.SubscriptionName
		if planName == "" {
			planName = "Kiro"
		}

		// Reset text
		resetStr := ""
		if m.credits.DaysUntilReset > 0 {
			resetStr = fmt.Sprintf("Resets in %d days", m.credits.DaysUntilReset)
		}

		// Render progress bar (shows remaining)
		progressBar := m.creditsProgress.ViewAs(percentRemaining)

		// Line 1: Credits used [progress bar] remaining
		statusLines = append(statusLines,
			labelStyle.Render("Credits: ")+progressStyle.Render(usedStr+" ")+progressBar+
				labelStyle.Render(" ")+progressStyle.Render(remainingStr))
		// Line 2: Plan name | Resets in X days
		if resetStr != "" {
			statusLines = append(statusLines,
				labelStyle.Render("         ")+valueStyle.Render(planName)+labelStyle.Render(" | ")+labelStyle.Render(resetStr))
		} else {
			statusLines = append(statusLines,
				labelStyle.Render("         ")+valueStyle.Render(planName))
		}
	} else if m.tokenInfo.Provider == "BuilderId" && m.credits.Error == nil {
		// BuilderId accounts don't have credits tracking - show informational message
		statusLines = append(statusLines,
			labelStyle.Render("Credits: ")+labelStyle.Render("Not tracked for AWS Builder ID"))
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
	if m.serverRunning && m.tokenInfo.Present && m.claudeConfig.Claude2KiroSet {
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

	// Help text (italic like Claude Code style)
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#626262")).
		Italic(true).
		MarginTop(1)
	helpText := helpStyle.Render("↑/k up • ↓/j down • q quit • ? more")

	// Status bar at bottom (fixed position)
	var statusBar string
	if m.isLoading {
		// Show animated spinner during loading
		spinnerStyle := lipgloss.NewStyle().
			Background(lipgloss.Color("#1F2D3D")).
			Foreground(lipgloss.Color("#7D56F4")).
			Padding(0, 2).
			Bold(true)
		spinnerChar := spinnerFrames[m.spinnerFrame]
		statusBar = spinnerStyle.Render(spinnerChar + " " + m.loadingText)
	} else if m.status != "" {
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

// SetStatus sets the status message and clears loading state
func (m *Model) SetStatus(msg string, isError bool) {
	m.status = msg
	m.statusErr = isError
	// Clear loading state when we receive a status update
	m.isLoading = false
	m.loadingText = ""
}

// ClearStatus clears the status message
func (m *Model) ClearStatus() {
	m.status = ""
	m.statusErr = false
}

// RefreshTokenInfo reloads token information from disk and rebuilds menu
func (m *Model) RefreshTokenInfo() {
	m.tokenInfo = getTokenInfo()
	m.claudeConfig = getClaudeConfigStatus()
	m.rebuildMenuItems()
}

// SetServerRunning sets the server running state and rebuilds menu
func (m *Model) SetServerRunning(running bool, port string) {
	m.serverRunning = running
	m.serverPort = port
	m.rebuildMenuItems()
}

// IsServerRunning returns whether the server is running
func (m *Model) IsServerRunning() bool {
	return m.serverRunning
}

// IsClaude2KiroConfigured returns whether Claude2Kiro is configured in Claude
func (m *Model) IsClaude2KiroConfigured() bool {
	return m.claudeConfig.Claude2KiroSet
}

// GetServerPort returns the server port
func (m *Model) GetServerPort() string {
	return m.serverPort
}
