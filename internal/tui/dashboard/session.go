package dashboard

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SessionInfo holds the session state
type SessionInfo struct {
	AuthMethod   string
	Provider     string
	TokenExpiry  time.Time
	RequestCount int
	Port         string
	ServerStatus string // "starting", "running", "stopped"
}

// SessionUpdateMsg updates session info
type SessionUpdateMsg struct {
	RequestCount int
	TokenExpiry  time.Time
}

// SessionModel represents the session info bubble
type SessionModel struct {
	info    SessionInfo
	width   int
	focused bool
}

// NewSessionModel creates a new session model
func NewSessionModel(port string) SessionModel {
	return SessionModel{
		info: SessionInfo{
			AuthMethod:   "Kiro Token",
			Provider:     "AWS CodeWhisperer",
			Port:         port,
			ServerStatus: "starting",
		},
		width: 40,
	}
}

// Init initializes the session model
func (m SessionModel) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (m SessionModel) Update(msg tea.Msg) (SessionModel, tea.Cmd) {
	switch msg := msg.(type) {
	case SessionUpdateMsg:
		m.info.RequestCount = msg.RequestCount
		m.info.TokenExpiry = msg.TokenExpiry
	case tea.WindowSizeMsg:
		m.width = msg.Width
	}
	return m, nil
}

// View renders the session info
func (m SessionModel) View() string {
	// Styles
	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#A0A0A0")).
		Width(14)

	valueStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FAFAFA"))

	goodStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#04B575"))

	warnStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFAA00"))

	errorStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FF5555"))

	// Format token expiry
	var expiryValue string
	var expiryStyle lipgloss.Style
	if m.info.TokenExpiry.IsZero() {
		expiryValue = "Unknown"
		expiryStyle = valueStyle
	} else {
		remaining := time.Until(m.info.TokenExpiry)
		if remaining < 0 {
			expiryValue = "Expired"
			expiryStyle = errorStyle
		} else if remaining < 5*time.Minute {
			expiryValue = fmt.Sprintf("%s (expiring soon!)", remaining.Round(time.Second))
			expiryStyle = errorStyle
		} else if remaining < 30*time.Minute {
			expiryValue = fmt.Sprintf("%s", remaining.Round(time.Second))
			expiryStyle = warnStyle
		} else {
			expiryValue = fmt.Sprintf("%s", remaining.Round(time.Minute))
			expiryStyle = goodStyle
		}
	}

	// Format server status
	var statusValue string
	var statusStyle lipgloss.Style
	switch m.info.ServerStatus {
	case "running":
		statusValue = "● Running"
		statusStyle = goodStyle
	case "starting":
		statusValue = "○ Starting..."
		statusStyle = warnStyle
	case "stopped":
		statusValue = "○ Stopped"
		statusStyle = errorStyle
	default:
		statusValue = m.info.ServerStatus
		statusStyle = valueStyle
	}

	// Build rows
	rows := []string{
		lipgloss.JoinHorizontal(lipgloss.Left,
			labelStyle.Render("Status:"),
			statusStyle.Render(statusValue),
		),
		lipgloss.JoinHorizontal(lipgloss.Left,
			labelStyle.Render("Port:"),
			valueStyle.Render(m.info.Port),
		),
		lipgloss.JoinHorizontal(lipgloss.Left,
			labelStyle.Render("Auth:"),
			valueStyle.Render(m.info.AuthMethod),
		),
		lipgloss.JoinHorizontal(lipgloss.Left,
			labelStyle.Render("Provider:"),
			valueStyle.Render(m.info.Provider),
		),
		lipgloss.JoinHorizontal(lipgloss.Left,
			labelStyle.Render("Token Expiry:"),
			expiryStyle.Render(expiryValue),
		),
		lipgloss.JoinHorizontal(lipgloss.Left,
			labelStyle.Render("Requests:"),
			valueStyle.Render(fmt.Sprintf("%d", m.info.RequestCount)),
		),
	}

	content := lipgloss.JoinVertical(lipgloss.Left, rows...)

	return content
}

// SetFocused sets the focus state
func (m *SessionModel) SetFocused(focused bool) {
	m.focused = focused
}

// IsFocused returns the focus state
func (m SessionModel) IsFocused() bool {
	return m.focused
}

// SetWidth sets the width
func (m *SessionModel) SetWidth(width int) {
	m.width = width
}

// SetStatus sets the server status
func (m *SessionModel) SetStatus(status string) {
	m.info.ServerStatus = status
}

// SetTokenExpiry sets the token expiry time
func (m *SessionModel) SetTokenExpiry(expiry time.Time) {
	m.info.TokenExpiry = expiry
}

// IncrementRequests increments the request count
func (m *SessionModel) IncrementRequests() {
	m.info.RequestCount++
}

// GetRequestCount returns the current request count
func (m SessionModel) GetRequestCount() int {
	return m.info.RequestCount
}

// GetStatus returns the server status
func (m SessionModel) GetStatus() string {
	return m.info.ServerStatus
}
