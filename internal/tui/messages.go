package tui

import (
	"time"
)

// AppState represents the current view state
type AppState int

const (
	StateMenu AppState = iota
	StateLogin
	StateDashboard
)

// MenuAction represents a menu selection
type MenuAction int

const (
	ActionLogin MenuAction = iota
	ActionServer
	ActionRefreshToken
	ActionViewToken
	ActionExportEnv
	ActionConfigureClaude
	ActionLogout
	ActionQuit
)

// NavigateToMenuMsg signals navigation to the main menu
type NavigateToMenuMsg struct{}

// NavigateToDashboardMsg signals navigation to the dashboard with server
type NavigateToDashboardMsg struct {
	Port        string
	TokenExpiry time.Time
}

// ServerStartedMsg indicates the server has started
type ServerStartedMsg struct {
	Port string
}

// ServerStoppedMsg indicates the server has stopped
type ServerStoppedMsg struct {
	Err error
}

// ServerErrorMsg indicates a server error
type ServerErrorMsg struct {
	Err error
}

// MenuActionMsg signals a menu action was selected
type MenuActionMsg struct {
	Action MenuAction
}

// LogType categorizes log entries
type LogType int

const (
	LogTypeReq LogType = iota
	LogTypeRes
	LogTypeErr
	LogTypeInf
)

// String returns a short string representation of the log type
func (t LogType) String() string {
	switch t {
	case LogTypeReq:
		return "REQ"
	case LogTypeRes:
		return "RES"
	case LogTypeErr:
		return "ERR"
	case LogTypeInf:
		return "INF"
	default:
		return "???"
	}
}

// LogEntry represents a single log entry
type LogEntry struct {
	Timestamp time.Time
	Type      LogType
	Model     string        // For requests: the model being used
	Method    string        // HTTP method
	Path      string        // URL path
	Status    int           // HTTP status code (for responses)
	Duration  time.Duration // Request duration (for responses)
	Preview   string        // Truncated body preview
}

// LogEntryMsg carries a log entry to the TUI
type LogEntryMsg struct {
	Entry LogEntry
}

// SessionUpdateMsg updates the session info
type SessionUpdateMsg struct {
	RequestCount int
	TokenExpiry  time.Time
}

// TokenRefreshedMsg indicates the token was refreshed
type TokenRefreshedMsg struct {
	ExpiresAt time.Time
}

// TickMsg for periodic updates
type TickMsg time.Time

// FocusedPanel identifies which panel is focused in dashboard
type FocusedPanel int

const (
	PanelSession FocusedPanel = iota
	PanelLogs
)

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

// StatusMsg displays a temporary status message
type StatusMsg struct {
	Message string
	IsError bool
}
