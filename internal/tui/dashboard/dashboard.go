package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sgeraldes/claude2kiro/cmd"
	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
	"github.com/sgeraldes/claude2kiro/internal/tui/menu"
	"github.com/sgeraldes/claude2kiro/internal/tui/messages"
)

// RefreshTokenFunc is the function type for refreshing tokens
type RefreshTokenFunc func() (time.Time, error)

// IsTokenExpiredFunc is the function type for checking if token is expired
type IsTokenExpiredFunc func() bool

// Version from menu package
var Version = menu.Version

// FocusedPanel identifies which panel is focused
type FocusedPanel int

const (
	PanelSession FocusedPanel = iota
	PanelLogs
)

// Type aliases for shared messages - allows backward compatibility
// while breaking the import cycle between cmd and dashboard
type ServerStartedMsg = messages.ServerStartedMsg
type ServerStoppedMsg = messages.ServerStoppedMsg

// BackToMenuMsg signals return to the main menu (server stopped)
type BackToMenuMsg struct{}

// GoToMenuMsg signals navigation to menu while keeping server running
type GoToMenuMsg struct{}

// OpenSettingsMsg signals navigation to settings from dashboard
type OpenSettingsMsg struct{}

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

// TokenRefreshedMsg signals token was refreshed on startup
type TokenRefreshedMsg struct {
	Success   bool
	ExpiresAt time.Time
	Err       error
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
// Skips API call entirely for AWS Builder ID accounts
func FetchCreditsCmd() tea.Cmd {
	return func() tea.Msg {
		// Check if this is a BuilderId account - skip credits API entirely
		token, err := cmd.GetToken()
		if err == nil && token.Provider == "BuilderId" {
			// BuilderId accounts don't support the credits API
			// Return a special "not supported" info instead of an error
			return CreditsUpdateMsg{Info: CreditsInfo{
				SubscriptionName: "AWS Builder ID",
				LastUpdated:      time.Now(),
				// No error - this is expected behavior
			}}
		}
		return CreditsUpdateMsg{Info: fetchCreditsInfo()}
	}
}

// autoRefreshTokenCmd checks if token is expired and refreshes it
func (m Model) autoRefreshTokenCmd() tea.Cmd {
	return func() tea.Msg {
		// If we don't have the functions, skip refresh and just fetch credits
		if m.isTokenExpiredFn == nil || m.refreshTokenFn == nil {
			return TokenRefreshedMsg{Success: true, ExpiresAt: m.tokenExpiry}
		}

		if !m.isTokenExpiredFn() {
			// Token is still valid, just fetch credits
			return TokenRefreshedMsg{Success: true, ExpiresAt: m.tokenExpiry}
		}

		// Token is expired, try to refresh
		newExpiry, err := m.refreshTokenFn()
		if err != nil {
			return TokenRefreshedMsg{Success: false, Err: err}
		}
		return TokenRefreshedMsg{Success: true, ExpiresAt: newExpiry}
	}
}

// DashboardKeyMap defines key bindings for the dashboard
type DashboardKeyMap struct {
	Tab        key.Binding
	Menu       key.Binding
	Settings   key.Binding
	OpenClaude key.Binding
	Quit       key.Binding
	Help       key.Binding
	Usage      key.Binding
	ScrollUp   key.Binding
	ScrollDown key.Binding
	PageUp     key.Binding
	PageDown   key.Binding
	Top        key.Binding
	Bottom     key.Binding
	Clear      key.Binding
	Search     key.Binding
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
		Settings: key.NewBinding(
			key.WithKeys("p"),
			key.WithHelp("p", "settings"),
		),
		OpenClaude: key.NewBinding(
			key.WithKeys("ctrl+o"),
			key.WithHelp("^o", "open claude"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "stop server"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Usage: key.NewBinding(
			key.WithKeys("u"),
			key.WithHelp("u", "open usage page"),
		),
		ScrollUp: key.NewBinding(
			key.WithKeys("up"),
			key.WithHelp("↑", "scroll up"),
		),
		ScrollDown: key.NewBinding(
			key.WithKeys("down"),
			key.WithHelp("↓", "scroll down"),
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
		Clear: key.NewBinding(
			key.WithKeys("c"),
			key.WithHelp("c", "clear from now"),
		),
		Search: key.NewBinding(
			key.WithKeys("/", "s"),
			key.WithHelp("s", "search"),
		),
	}
}

func (k DashboardKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.ScrollUp, k.ScrollDown, k.Search, k.Clear, k.Tab, k.Menu, k.Quit}
}

func (k DashboardKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.ScrollUp, k.ScrollDown, k.PageUp, k.PageDown},
		{k.Top, k.Bottom},
		{k.Tab, k.Settings, k.Menu, k.Help, k.Quit},
	}
}

// Model represents the server dashboard
type Model struct {
	session            SessionModel
	logViewer          LogViewerModel
	filterBar          FilterBarModel
	help               help.Model
	keys               DashboardKeyMap
	focusedPanel       FocusedPanel
	width              int
	height             int
	port               string
	server             *http.Server
	logger             *logger.Logger
	quitting           bool
	showFullHelp       bool
	tokenExpiry        time.Time
	credits            CreditsInfo
	creditsProgress    progress.Model
	serverRunning      bool
	refreshTokenFn     RefreshTokenFunc
	isTokenExpiredFn   IsTokenExpiredFunc
	creditsFetchFailed bool // Stop retrying after persistent 403/access denied errors
}


// New creates a new dashboard model
func New(port string, tokenExpiry time.Time, lg *logger.Logger, refreshFn RefreshTokenFunc, isExpiredFn IsTokenExpiredFunc) Model {
	h := help.New()
	h.ShowAll = false

	// Initialize progress bar with custom styling
	prog := progress.New(
		progress.WithScaledGradient("#7D56F4", "#6BFF6B"),
		progress.WithWidth(30),
		progress.WithoutPercentage(),
	)

	// Initialize filter bar
	filterBar := NewFilterBarModel()

	// Initialize log viewer with focus enabled from the start
	logViewer := NewLogViewerModel(80, 20)
	logViewer.SetFocused(true)

	// Apply initial filters from filter bar (includes persisted AfterDate)
	logViewer.ApplyFilters(filterBar.GetFilters())

	// Load existing entries from logger (for persistence across restarts)
	if lg != nil {
		existingEntries := lg.GetEntries()
		if len(existingEntries) > 0 {
			logViewer.SetEntries(existingEntries)
			// Re-apply filters after loading entries
			logViewer.ApplyFilters(filterBar.GetFilters())
		}
	}

	// Initialize session model with token expiry
	session := NewSessionModel(port)
	session.SetTokenExpiry(tokenExpiry)

	return Model{
		session:          session,
		logViewer:        logViewer,
		filterBar:        filterBar,
		help:             h,
		keys:             DefaultDashboardKeyMap(),
		focusedPanel:     PanelLogs,
		port:             port,
		logger:           lg,
		tokenExpiry:      tokenExpiry,
		creditsProgress:  prog,
		refreshTokenFn:   refreshFn,
		isTokenExpiredFn: isExpiredFn,
	}
}

// Init initializes the dashboard
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		tickCmd(),
		m.autoRefreshTokenCmd(), // Refresh token first if expired, then TokenRefreshedMsg will trigger credits fetch
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
		// If filter bar search is focused (typing mode), forward all keys to it
		if m.filterBar.IsSearchFocused() {
			var cmd tea.Cmd
			m.filterBar, cmd = m.filterBar.Update(msg)
			cmds = append(cmds, cmd)
			return m, tea.Batch(cmds...)
		}

		// If filter bar is focused (navigating), forward navigation keys to it
		if m.filterBar.IsFocused() {
			switch msg.String() {
			case "m", "esc":
				// Allow menu key to work
			case "q", "ctrl+c":
				// Allow quit key to work
			case "ctrl+o", "p":
				// Allow open claude and settings keys to work
			default:
				// Forward other keys to filter bar
				var cmd tea.Cmd
				m.filterBar, cmd = m.filterBar.Update(msg)
				cmds = append(cmds, cmd)
				return m, tea.Batch(cmds...)
			}
		}

		switch {
		case key.Matches(msg, m.keys.Menu):
			// Go to menu without stopping the server
			return m, func() tea.Msg { return GoToMenuMsg{} }

		case key.Matches(msg, m.keys.Settings):
			// Open settings
			return m, func() tea.Msg { return OpenSettingsMsg{} }

		case key.Matches(msg, m.keys.OpenClaude):
			// Open Claude Code with current session
			meta := m.logViewer.GetCurrentSessionMetadata()
			if meta == nil {
				m.logViewer.AddEntry(logger.LogEntry{
					Timestamp: time.Now(),
					Type:      logger.LogTypeErr,
					Preview:   "No session selected - use 's' to select a specific session first",
				})
				return m, nil
			}
			if meta.FullUUID == "" {
				m.logViewer.AddEntry(logger.LogEntry{
					Timestamp: time.Now(),
					Type:      logger.LogTypeErr,
					Preview:   fmt.Sprintf("Session %s has no UUID available (session may be too old)", meta.SessionID),
				})
				return m, nil
			}
			// Open Claude Code with session parameters
			err := cmd.OpenClaudeCode(meta.WorkingDir, meta.FullUUID, meta.SessionID)
			if err != nil {
				m.logViewer.AddEntry(logger.LogEntry{
					Timestamp: time.Now(),
					Type:      logger.LogTypeErr,
					Preview:   fmt.Sprintf("Failed to open Claude Code: %v", err),
				})
			} else {
				// Determine which mode was used
				mode := "resumed"
				if cmd.IsClaudeRunning() {
					mode = "forked"
				}
				m.logViewer.AddEntry(logger.LogEntry{
					Timestamp: time.Now(),
					Type:      logger.LogTypeInf,
					Preview:   fmt.Sprintf("Opened Claude Code (%s session %s in %s)", mode, meta.SessionID, meta.WorkingDir),
				})
			}
			return m, nil

		case key.Matches(msg, m.keys.Quit):
			m.quitting = true
			// Graceful shutdown
			if m.server != nil {
				cfg := config.Get()
				ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
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

		case key.Matches(msg, m.keys.Usage):
			// Open Kiro usage page in browser
			if err := cmd.OpenBrowser(cmd.GetKiroUsageURL()); err != nil {
				m.logViewer.AddEntry(logger.LogEntry{
					Timestamp: time.Now(),
					Type:      logger.LogTypeErr,
					Preview:   fmt.Sprintf("Failed to open browser: %v", err),
				})
			} else {
				m.logViewer.AddEntry(logger.LogEntry{
					Timestamp: time.Now(),
					Type:      logger.LogTypeInf,
					Preview:   "Opened Kiro usage page in browser",
				})
			}

		default:
			// Handle filter bar keys
			switch msg.String() {
			case "c":
				// Set AfterDate filter to now
				m.filterBar.SetAfterDate(time.Now())
				m.logViewer.ApplyFilters(m.filterBar.GetFilters())
				return m, nil
			case "/", "s":
				// Focus search in filter bar
				m.filterBar.SetFocused(true)
				m.logViewer.SetFocused(false)
				cmd := m.filterBar.FocusSearch()
				return m, cmd
			case "1", "2", "3", "4":
				// Forward to filter bar for type toggles
				var cmd tea.Cmd
				m.filterBar, cmd = m.filterBar.Update(msg)
				cmds = append(cmds, cmd)
			case "x", "X":
				// Clear AfterDate filter
				m.filterBar.ClearAfterDate()
				m.logViewer.ApplyFilters(m.filterBar.GetFilters())
				return m, nil
			default:
				// Forward to focused panel
				if m.focusedPanel == PanelLogs {
					var cmd tea.Cmd
					m.logViewer, cmd = m.logViewer.Update(msg)
					cmds = append(cmds, cmd)
				}
			}
		}

	case FilterChangedMsg:
		// Apply filter changes to log viewer
		m.logViewer.ApplyFilters(msg.State)

	case FocusFilterBarMsg:
		// User pressed up from first log entry, focus filter bar
		m.filterBar.SetFocused(true)
		m.logViewer.SetFocused(false)

	case FocusLogListMsg:
		// User pressed down from filter bar, focus log viewer
		m.filterBar.SetFocused(false)
		m.logViewer.SetFocused(true)

	case logger.LogEntryMsg:
		// Add to log viewer
		m.logViewer.AddEntry(msg.Entry)
		// Update request count for REQ type
		if msg.Entry.Type == logger.LogTypeReq {
			m.session.IncrementRequests()
		}
		// Refresh credits after each response (credits were consumed), unless API is unavailable
		if msg.Entry.Type == logger.LogTypeRes && !m.creditsFetchFailed {
			cmds = append(cmds, FetchCreditsCmd())
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

		// Check if token is expired and needs refresh (retry periodically)
		if m.isTokenExpiredFn != nil && m.isTokenExpiredFn() && m.refreshTokenFn != nil {
			// Only attempt refresh if not already showing an error (avoid spam)
			if m.credits.Error == nil || time.Since(m.credits.LastUpdated) > 30*time.Second {
				cmds = append(cmds, m.autoRefreshTokenCmd())
			}
		}

	case CreditsUpdateMsg:
		m.credits = msg.Info
		// Log credits errors for debugging
		if msg.Info.Error != nil {
			errStr := msg.Info.Error.Error()
			// Check if this is a 403 error - stop retrying for these
			if strings.Contains(errStr, "status 403") || strings.Contains(errStr, "Access Denied") {
				if !m.creditsFetchFailed {
					// Only log once
					m.logViewer.AddEntry(logger.LogEntry{
						Timestamp: time.Now(),
						Type:      logger.LogTypeErr,
						Preview:   fmt.Sprintf("Credits API unavailable: %v", msg.Info.Error),
					})
					m.creditsFetchFailed = true
				}
			} else {
				m.logViewer.AddEntry(logger.LogEntry{
					Timestamp: time.Now(),
					Type:      logger.LogTypeErr,
					Preview:   fmt.Sprintf("Credits fetch failed: %v", msg.Info.Error),
				})
			}
		} else {
			// Reset flag on success
			m.creditsFetchFailed = false
		}

	case TokenRefreshedMsg:
		if msg.Success {
			// Token is valid/refreshed, update expiry and fetch credits (unless we know credits API is unavailable)
			m.tokenExpiry = msg.ExpiresAt
			m.session.SetTokenExpiry(msg.ExpiresAt)
			if !m.creditsFetchFailed {
				cmds = append(cmds, FetchCreditsCmd())
			}
		} else {
			// Token refresh failed, show error in credits and log it
			m.credits = CreditsInfo{Error: msg.Err, LastUpdated: time.Now()}
			m.logViewer.AddEntry(logger.LogEntry{
				Timestamp: time.Now(),
				Type:      logger.LogTypeErr,
				Preview:   fmt.Sprintf("Token refresh failed: %v", msg.Err),
			})
		}

	case ClipboardResultMsg:
		if msg.Success {
			m.logViewer.AddEntry(logger.LogEntry{
				Timestamp: time.Now(),
				Type:      logger.LogTypeInf,
				Preview:   "Copied to clipboard",
			})
		} else {
			m.logViewer.AddEntry(logger.LogEntry{
				Timestamp: time.Now(),
				Type:      logger.LogTypeErr,
				Preview:   fmt.Sprintf("Failed to copy to clipboard: %v", msg.Error),
			})
		}

	case EditorResultMsg:
		if msg.Success {
			m.logViewer.AddEntry(logger.LogEntry{
				Timestamp: time.Now(),
				Type:      logger.LogTypeInf,
				Preview:   "Opened in external editor",
			})
		} else {
			m.logViewer.AddEntry(logger.LogEntry{
				Timestamp: time.Now(),
				Type:      logger.LogTypeErr,
				Preview:   fmt.Sprintf("Failed to open in editor: %v", msg.Error),
			})
		}

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
	helpHeight := 1      // Footer with help text
	sessionHeight := 8   // Fixed session panel height
	filterBarHeight := 3 // Filter bar height (border + content)

	// Session panel gets fixed height
	m.session.SetWidth(contentWidth)

	// Filter bar width
	m.filterBar.SetWidth(contentWidth)

	// Log viewer gets remaining space
	// Total: m.height - sessionHeight - filterBarHeight - helpHeight - 2 (margins)
	logHeight := m.height - sessionHeight - filterBarHeight - helpHeight - 2
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

	// Status panel (always visible at top)
	statusPanel := renderStatusPanel(m.serverRunning, m.port, m.credits, m.creditsProgress, m.tokenExpiry)

	// Stats panel (memory/disk usage) - render as a box next to status
	statsPanel := m.renderStatsPanel()

	// Calculate remaining width for session stats panel
	statusWidth := lipgloss.Width(statusPanel)
	statsWidth := lipgloss.Width(statsPanel)
	usedWidth := statusWidth + statsWidth + 2 // +2 for spaces
	remainingWidth := m.width - usedWidth - 4 // -4 for margins

	// Session stats panel (only when a specific session is selected)
	var topSection string
	if sessionPanel := m.renderSessionStatsPanel(remainingWidth); sessionPanel != "" && remainingWidth > 20 {
		topSection = lipgloss.JoinHorizontal(lipgloss.Top, statusPanel, " ", statsPanel, " ", sessionPanel)
	} else {
		topSection = lipgloss.JoinHorizontal(lipgloss.Top, statusPanel, " ", statsPanel)
	}

	// Filter bar (between status and log viewer)
	filterBarView := m.filterBar.View()

	// Log viewer renders its own composable panels with focus borders
	logView := m.logViewer.View()

	// Help bar (italic like Claude Code style) - build custom help text
	helpText := m.renderHelpText()

	// Version in bottom right
	versionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#626262"))
	versionText := versionStyle.Render("v" + Version)

	// Build footer with help and version
	footerWidth := m.width - 4
	helpWidth := lipgloss.Width(helpText)
	versionWidth := lipgloss.Width(versionText)
	padding := footerWidth - helpWidth - versionWidth
	if padding < 1 {
		padding = 1
	}
	footer := helpText + strings.Repeat(" ", padding) + versionText

	// Combine all panels
	content := lipgloss.JoinVertical(lipgloss.Left,
		topSection,
		filterBarView,
		logView,
		footer,
	)

	// Wrap in container that fills the terminal
	containerStyle := lipgloss.NewStyle().
		Width(m.width).
		Height(m.height)

	return containerStyle.Render(content)
}

// renderHelpText renders the help bar with context-sensitive text
func (m Model) renderHelpText() string {
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#626262")).
		Italic(true)

	accentStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#7D56F4")).
		Italic(true)

	var parts []string

	// Determine current focus and show relevant help
	if m.filterBar.IsFocused() {
		if m.filterBar.IsSearchFocused() {
			// Search input focused
			parts = []string{
				"type to search",
				"enter/esc confirm",
			}
		} else {
			// Filter bar navigation
			parts = []string{
				"←/→ select",
				"enter toggle",
				"/ search",
				"1-4 type filters",
				"↓ to list",
				"x clear date",
			}
		}
	} else if m.logViewer.IsFocused() {
		if m.logViewer.GetPanelFocus() == FocusDetail {
			// Detail view focused
			parts = []string{
				"↑/↓ scroll",
				"pgup/pgdn page",
				"tab to list",
				"v view mode",
				"e expand",
				"y copy",
				"o open",
			}
		} else {
			// Log list focused
			parts = []string{
				"↑/↓ navigate",
				"r/R req↔res",
				"tab to detail",
				"s/S session",
				"c clear",
				"v view",
				"e expand",
				"y copy",
				"o open",
			}
		}
	} else {
		// Fallback - general help
		parts = []string{
			"↑/↓ navigate",
			"tab switch",
			"/ search",
			"p settings",
		}
	}

	// Add global keys
	globalParts := []string{
		accentStyle.Render("q quit"),
		accentStyle.Render("p settings"),
	}

	allParts := append(parts, globalParts...)
	return helpStyle.Render(strings.Join(allParts, " • "))
}

// renderStatsPanel renders the memory and disk usage stats as a boxed panel
func (m Model) renderStatsPanel() string {
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A0A0A0"))
	headerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262")).Bold(true)

	var lines []string

	// Header
	lines = append(lines, headerStyle.Render("System Stats"))

	// Get memory usage from logger
	if m.logger != nil {
		memBytes := m.logger.EstimatedMemoryUsage()
		entryCount := m.logger.Count()
		lines = append(lines,
			labelStyle.Render("Memory: ")+valueStyle.Render(fmt.Sprintf("%s (%d entries)", config.FormatBytes(int64(memBytes)), entryCount)))
	} else {
		lines = append(lines,
			labelStyle.Render("Memory: ")+valueStyle.Render("N/A"))
	}

	// Get disk usage
	diskBytes, err := config.GetLogDirSize()
	if err == nil {
		cfg := config.Get()
		if cfg.Logging.MaxLogSizeMB > 0 {
			pct := float64(diskBytes) / float64(cfg.Logging.MaxLogSizeMB*1024*1024) * 100
			lines = append(lines,
				labelStyle.Render("Disk: ")+valueStyle.Render(fmt.Sprintf("%s / %d MB (%.0f%%)", config.FormatBytes(diskBytes), cfg.Logging.MaxLogSizeMB, pct)))
		} else {
			lines = append(lines,
				labelStyle.Render("Disk: ")+valueStyle.Render(config.FormatBytes(diskBytes)))
		}
	} else {
		lines = append(lines,
			labelStyle.Render("Disk: ")+valueStyle.Render("N/A"))
	}

	// Session count
	sessionCount := m.logViewer.GetSessionCount()
	lines = append(lines,
		labelStyle.Render("Sessions: ")+valueStyle.Render(fmt.Sprintf("%d", sessionCount)))

	// Filtered/Total entries
	filteredCount := m.logViewer.EntryCount()
	totalCount := m.logViewer.TotalEntryCount()
	if filteredCount != totalCount {
		lines = append(lines,
			labelStyle.Render("Showing: ")+valueStyle.Render(fmt.Sprintf("%d / %d entries", filteredCount, totalCount)))
	} else {
		lines = append(lines,
			labelStyle.Render("Entries: ")+valueStyle.Render(fmt.Sprintf("%d", totalCount)))
	}

	// Current view mode
	viewMode := m.logViewer.GetViewMode()
	viewStr := "Parsed"
	switch viewMode {
	case ViewModeJSON:
		viewStr = "JSON"
	case ViewModeRaw:
		viewStr = "Raw"
	}
	lines = append(lines,
		labelStyle.Render("View: ")+valueStyle.Render(viewStr))

	// Empty line for spacing (to match status panel height)
	lines = append(lines, "")

	// Box style similar to status panel
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#626262")).
		Padding(0, 1)

	return boxStyle.Render(strings.Join(lines, "\n"))
}

// renderSessionStatsPanel renders the session information panel
// Only shown when a specific session is selected (not "All")
func (m Model) renderSessionStatsPanel(availableWidth int) string {
	meta := m.logViewer.GetCurrentSessionMetadata()
	if meta == nil {
		return "" // "All" selected, no session panel
	}

	// Don't render if not enough width
	if availableWidth < 30 {
		return ""
	}

	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A0A0A0"))
	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#7D56F4")).Bold(true)
	headerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262")).Bold(true)

	// Content width for text (width - border(2) - padding(2))
	contentWidth := availableWidth - 4
	if contentWidth < 20 {
		contentWidth = 20
	}

	var lines []string

	// Header
	lines = append(lines, headerStyle.Render("Session Info"))

	// Session ID (truncate to fit)
	sessionID := meta.SessionID
	maxIDLen := contentWidth - 4 // "ID: " is 4 chars
	if len(sessionID) > maxIDLen && maxIDLen > 10 {
		sessionID = sessionID[:maxIDLen-2] + ".."
	}
	lines = append(lines, labelStyle.Render("ID: ")+valueStyle.Render(sessionID))

	// Working folder (show folder name if available, otherwise path)
	if meta.FolderName != "" {
		folder := meta.FolderName
		maxFolderLen := contentWidth - 8 // "Folder: " is 8 chars
		if len(folder) > maxFolderLen && maxFolderLen > 10 {
			folder = folder[:maxFolderLen-2] + ".."
		}
		lines = append(lines, labelStyle.Render("Folder: ")+valueStyle.Render(folder))
	} else if meta.WorkingDir != "" {
		// Truncate path if too long
		path := meta.WorkingDir
		maxLen := contentWidth - 6 // "Path: " is 6 chars
		if len(path) > maxLen && maxLen > 10 {
			path = "..." + path[len(path)-maxLen+3:]
		}
		lines = append(lines, labelStyle.Render("Path: ")+valueStyle.Render(path))
	} else {
		lines = append(lines, labelStyle.Render("Folder: ")+valueStyle.Render("-"))
	}

	// Title (truncate with ellipsis if too long)
	titleLabel := "Title: "
	if meta.Title != "" {
		titleText := meta.Title
		maxTitleLen := contentWidth - len(titleLabel)
		if len(titleText) > maxTitleLen && maxTitleLen > 10 {
			titleText = titleText[:maxTitleLen-3] + "..."
		}
		lines = append(lines, labelStyle.Render(titleLabel)+titleStyle.Render(titleText))
	} else {
		lines = append(lines, labelStyle.Render(titleLabel)+valueStyle.Render("-"))
	}

	// Pad to 7 lines (2 taller than before to match other panels)
	for len(lines) < 7 {
		lines = append(lines, "")
	}

	// Box style with fixed width
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#626262")).
		Padding(0, 1).
		Width(availableWidth - 2) // Width includes padding, border adds 2 more

	return boxStyle.Render(strings.Join(lines, "\n"))
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
	FileExists     bool
	Claude2KiroSet bool
	ApiKeyAuth     bool
}

// getTokenInfo reads token information using the shared cmd function
func getTokenInfo() TokenInfo {
	token, err := cmd.GetToken()
	if err != nil {
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

	if claude2kiro, ok := config["claude2kiro"].(bool); ok && claude2kiro {
		status.Claude2KiroSet = true
	}

	if oauthAccount, ok := config["oauthAccount"].(map[string]interface{}); ok {
		if authType, ok := oauthAccount["type"].(string); ok && authType == "api_key" {
			status.ApiKeyAuth = true
		}
	}

	return status
}

// renderStatusPanel renders the status panel
func renderStatusPanel(serverRunning bool, serverPort string, credits CreditsInfo, creditsProgress progress.Model, tokenExpiry time.Time) string {
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FAFAFA")).Bold(true)
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6BFF6B")).Bold(true)
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFAA00")).Bold(true)
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B")).Bold(true)

	var statusLines []string
	tokenInfo := getTokenInfo()
	claudeConfig := getClaudeConfigStatus()

	// Only use model cache as fallback when file data is missing
	// Fresh file data takes priority over potentially stale model cache
	if tokenInfo.ExpiresAt.IsZero() && !tokenExpiry.IsZero() {
		tokenInfo.ExpiresAt = tokenExpiry
	}

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
		// Session type - check Provider first, then AuthMethod
		sessionType := "Unknown"
		if tokenInfo.Provider == "BuilderId" {
			sessionType = "AWS Builder ID"
		} else if tokenInfo.Provider == "github" || tokenInfo.Provider == "GitHub" {
			sessionType = "GitHub"
		} else if tokenInfo.Provider == "google" || tokenInfo.Provider == "Google" {
			sessionType = "Google"
		} else if tokenInfo.AuthMethod == "IdC" {
			sessionType = "Enterprise SSO"
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

		// Region and Start URL (only for Enterprise SSO, not BuilderId)
		if tokenInfo.AuthMethod == "IdC" && tokenInfo.Provider != "BuilderId" {
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
		}
	} else {
		statusLines = append(statusLines,
			labelStyle.Render("Credentials: ")+errStyle.Render("✗ Not logged in"))
	}

	// Claude Code config status
	if claudeConfig.FileExists {
		if claudeConfig.Claude2KiroSet && claudeConfig.ApiKeyAuth {
			statusLines = append(statusLines,
				labelStyle.Render("Claude Code: ")+okStyle.Render("✓ Configured for Claude2Kiro"))
		} else if claudeConfig.Claude2KiroSet {
			statusLines = append(statusLines,
				labelStyle.Render("Claude Code: ")+warnStyle.Render("⚠ Partial config (press m → Configure Claude)"))
		} else {
			statusLines = append(statusLines,
				labelStyle.Render("Claude Code: ")+warnStyle.Render("⚠ Not configured (press m → Configure Claude)"))
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

		// Format credits info - show used/limit and remaining
		usedStr := fmt.Sprintf("%.1f/%.0f used", credits.CreditsUsed, credits.CreditsLimit)
		remainingStr := fmt.Sprintf("%.1f remaining", credits.CreditsRemaining)
		planName := credits.SubscriptionName
		if planName == "" {
			planName = "Kiro"
		}

		// Reset text
		resetStr := ""
		if credits.DaysUntilReset > 0 {
			resetStr = fmt.Sprintf("Resets in %d days", credits.DaysUntilReset)
		}

		// Render progress bar (shows remaining)
		progressBar := creditsProgress.ViewAs(percentRemaining)

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
	} else if tokenInfo.Provider == "BuilderId" && credits.Error == nil {
		// AWS Builder ID - credits API not applicable, show informational message
		statusLines = append(statusLines,
			labelStyle.Render("Credits: ")+labelStyle.Render("Not tracked for AWS Builder ID"))
	} else if credits.Error != nil && tokenInfo.Present {
		// Show error if logged in - clean up the message
		errMsg := credits.Error.Error()
		// Check for common errors and show friendlier messages
		if strings.Contains(errMsg, "401") || strings.Contains(errMsg, "unauthorized") {
			errMsg = "Unauthorized - please re-login"
		} else if strings.Contains(errMsg, "403") {
			errMsg = "API unavailable (press u to view online)"
		} else if strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "connection") {
			errMsg = "Connection error (press u to view online)"
		} else if strings.Contains(errMsg, "invalid") || strings.Contains(errMsg, "bearer token") {
			errMsg = "Invalid token - please re-login"
		} else if len(errMsg) > 50 {
			errMsg = errMsg[:47] + "..."
		}
		statusLines = append(statusLines,
			labelStyle.Render("Credits: ")+warnStyle.Render("⚠ "+errMsg))
	} else if tokenInfo.Present && credits.LastUpdated.IsZero() {
		// Show loading if logged in but credits not yet fetched
		statusLines = append(statusLines,
			labelStyle.Render("Credits: ")+labelStyle.Render("Loading..."))
	}

	// Determine border color
	var borderColor lipgloss.Color
	if serverRunning && tokenInfo.Present && claudeConfig.Claude2KiroSet {
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
