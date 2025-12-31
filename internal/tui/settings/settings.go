package settings

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sgeraldes/claude2kiro/internal/config"
)

// Tab represents a settings category
type Tab int

const (
	TabServer Tab = iota
	TabLogging
	TabDisplay
	TabNetwork
	TabAdvanced
)

var tabNames = []string{"Server", "Logging", "Display", "Network", "Advanced"}

// SettingType defines the type of setting input
type SettingType int

const (
	TypeText SettingType = iota
	TypeNumber
	TypeToggle
	TypeSelect
)

// ExtendedHelp contains detailed help information for a setting
type ExtendedHelp struct {
	DefaultValue    string
	RecommendedValue string
	Sensitive       bool
	ReferenceURL    string
	DetailedDesc    string
}

// Setting represents a single configurable item
type Setting struct {
	Key          string
	Label        string
	Description  string
	Type         SettingType
	Value        string
	Options      []string // For TypeSelect
	ExtendedHelp ExtendedHelp
	Min          int // For TypeNumber - minimum value
	Max          int // For TypeNumber - maximum value
	Step         int // For TypeNumber - increment/decrement step
}

// BackToMenuMsg signals to go back to the previous view
type BackToMenuMsg struct{}

// BackToDashboardMsg signals to go back to dashboard
type BackToDashboardMsg struct{}

// Model represents the settings view
type Model struct {
	width         int
	height        int
	activeTab     Tab
	selectedItem  int
	editing       bool
	textInput     textinput.Model
	settings      map[Tab][]Setting
	config        *config.Config
	fromDashboard bool
	statusMessage string
	statusErr     bool
	// Stats for display in help panel
	logDiskSize   int64 // Current log directory size in bytes
	logMemorySize int   // Current memory usage by log entries in bytes
	logEntryCount int   // Current number of log entries in memory
	// Dirty tracking for unsaved changes
	dirty             bool   // Whether settings have been modified
	savedSinceOpen    bool   // Whether user has saved at least once
	editOriginalValue string // Original value before entering edit mode
	showExitModal     bool   // Whether to show exit confirmation modal
	escPressedOnce    bool   // Track double-esc for quick exit
	modalSelectedOpt  int    // 0=save, 1=exit without saving, 2=cancel
}

// New creates a new settings model
func New(width, height int, fromDashboard bool) Model {
	ti := textinput.New()
	ti.CharLimit = 200
	ti.Width = 50

	cfg := config.Get()

	m := Model{
		width:         width,
		height:        height,
		activeTab:     TabServer,
		selectedItem:  0,
		textInput:     ti,
		config:        cfg,
		fromDashboard: fromDashboard,
		settings:      make(map[Tab][]Setting),
	}

	// Get initial disk usage stats
	if diskSize, err := config.GetLogDirSize(); err == nil {
		m.logDiskSize = diskSize
	}

	m.loadSettings()
	return m
}

// UpdateStats updates the memory and entry count stats (called from dashboard)
func (m *Model) UpdateStats(memorySize int, entryCount int) {
	m.logMemorySize = memorySize
	m.logEntryCount = entryCount
}

// RefreshDiskStats refreshes the disk usage stats
func (m *Model) RefreshDiskStats() {
	if diskSize, err := config.GetLogDirSize(); err == nil {
		m.logDiskSize = diskSize
	}
}

// loadSettings loads current config values into settings with extended help
func (m *Model) loadSettings() {
	// Server settings
	m.settings[TabServer] = []Setting{
		{
			Key:         "server.port",
			Label:       "Server Port",
			Description: "HTTP proxy server port",
			Type:        TypeText,
			Value:       m.config.Server.Port,
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "8080",
				RecommendedValue: "8080",
				Sensitive:        false,
				DetailedDesc:     "The TCP port where the Claude2Kiro proxy server listens for incoming requests from Claude Code. Claude Code will send API requests to http://localhost:<port>/v1/messages.",
			},
		},
		{
			Key:         "server.auto_start",
			Label:       "Auto-Start Server",
			Description: "Start server when opening dashboard",
			Type:        TypeToggle,
			Value:       boolToString(m.config.Server.AutoStart),
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "false",
				RecommendedValue: "true",
				Sensitive:        false,
				DetailedDesc:     "When enabled, the proxy server starts automatically when you open the dashboard view. This saves time if you always want the server running.",
			},
		},
		{
			Key:         "server.shutdown_timeout",
			Label:       "Shutdown Timeout",
			Description: "Time to wait for graceful shutdown",
			Type:        TypeText,
			Value:       m.config.Server.ShutdownTimeout.String(),
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "5s",
				RecommendedValue: "5s",
				Sensitive:        false,
				DetailedDesc:     "Duration to wait for active connections to complete before forcefully shutting down the server. Format: 5s, 10s, 1m. Longer values are safer for in-flight requests.",
			},
		},
	}

	// Logging settings
	m.settings[TabLogging] = []Setting{
		{
			Key:         "logging.enabled",
			Label:       "Enable Logging",
			Description: "Write logs to disk",
			Type:        TypeToggle,
			Value:       boolToString(m.config.Logging.Enabled),
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "true",
				RecommendedValue: "true",
				Sensitive:        false,
				DetailedDesc:     "When enabled, all API requests and responses are logged to files in the log directory. Useful for debugging and auditing. Disable to save disk space or for privacy.",
			},
		},
		{
			Key:         "logging.directory",
			Label:       "Log Directory",
			Description: "Where to store log files",
			Type:        TypeText,
			Value:       m.config.Logging.Directory,
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "~/.claude2kiro/logs/",
				RecommendedValue: "~/.claude2kiro/logs/",
				Sensitive:        false,
				DetailedDesc:     "Directory path where log files are stored. Supports ~ for home directory. Each day creates a new log file (YYYY-MM-DD.log).",
			},
		},
		{
			Key:         "logging.dashboard_retention",
			Label:       "Dashboard Retention",
			Description: "In-memory session display time",
			Type:        TypeSelect,
			Value:       m.config.Logging.DashboardRetention,
			Options:     []string{"1h", "2h", "3h", "5h", "8h", "12h", "18h", "24h", "48h", "72h", "unlimited"},
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "24h",
				RecommendedValue: "24h",
				Sensitive:        false,
				DetailedDesc:     "How long sessions remain visible in the dashboard. This only affects the in-memory display - log files are retained separately. 'unlimited' keeps all sessions in memory (uses more RAM). Type a custom value or use left/right to cycle.",
			},
		},
		{
			Key:         "logging.file_retention",
			Label:       "File Retention",
			Description: "How long to keep log files on disk",
			Type:        TypeSelect,
			Value:       m.config.Logging.FileRetention,
			Options:     []string{"1d", "3d", "7d", "14d", "30d", "60d", "90d", "180d", "365d", "unlimited"},
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "unlimited",
				RecommendedValue: "unlimited",
				Sensitive:        false,
				DetailedDesc:     "How long log files are kept on disk before automatic deletion. Type a custom value or use left/right to cycle through options. Files are deleted when this setting is checked at startup.",
			},
		},
		{
			Key:         "logging.max_log_size_mb",
			Label:       "Max Log Size (MB)",
			Description: "Maximum total log directory size",
			Type:        TypeNumber,
			Value:       fmt.Sprintf("%d", m.config.Logging.MaxLogSizeMB),
			Min:         0,
			Max:         10000,
			Step:        50,
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "100",
				RecommendedValue: "100",
				Sensitive:        false,
				DetailedDesc:     "Maximum total size for all log files in MB. When exceeded, oldest files are deleted. Set to 0 for unlimited. Range: 0-10000 MB.",
			},
		},
		{
			Key:         "logging.max_entries",
			Label:       "Max Log Entries",
			Description: "Maximum entries in memory",
			Type:        TypeNumber,
			Value:       fmt.Sprintf("%d", m.config.Logging.MaxEntries),
			Min:         100,
			Max:         5000,
			Step:        100,
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "500",
				RecommendedValue: "500",
				Sensitive:        false,
				DetailedDesc:     "Maximum number of log entries kept in memory for the dashboard view. Higher values use more RAM but let you scroll back further. Range: 100-5000.",
			},
		},
		{
			Key:         "logging.file_content_length",
			Label:       "File Content Length",
			Description: "Max chars saved per entry (0=unlimited)",
			Type:        TypeNumber,
			Value:       fmt.Sprintf("%d", m.config.Logging.FileContentLen),
			Min:         0,
			Max:         100000,
			Step:        1000,
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "0",
				RecommendedValue: "0",
				Sensitive:        false,
				DetailedDesc:     "Maximum characters saved per log entry in files. Set to 0 for unlimited (saves full content). Higher values capture more detail but use more disk space. Range: 0 (unlimited) - 100000 chars.",
			},
		},
		{
			Key:         "logging.preview_length",
			Label:       "Preview Length",
			Description: "Chars shown in list preview (0=unlimited)",
			Type:        TypeNumber,
			Value:       fmt.Sprintf("%d", m.config.Logging.PreviewLength),
			Min:         0,
			Max:         200000,
			Step:        1000,
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "10000",
				RecommendedValue: "10000",
				Sensitive:        false,
				DetailedDesc:     "Number of characters shown in the log list preview column. Set to 0 for unlimited. Higher values show more content. Range: 0 (unlimited) - 200000.",
			},
		},
	}

	// Display settings
	m.settings[TabDisplay] = []Setting{
		{
			Key:         "display.show_status_in_list",
			Label:       "Show Status in List",
			Description: "Display HTTP status code",
			Type:        TypeToggle,
			Value:       boolToString(m.config.Display.ShowStatusInList),
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "true",
				RecommendedValue: "true",
				Sensitive:        false,
				DetailedDesc:     "Shows the HTTP status code (200, 403, 500, etc.) in the log list. Helpful for quickly identifying failed requests.",
			},
		},
		{
			Key:         "display.show_duration_in_list",
			Label:       "Show Duration in List",
			Description: "Display request duration",
			Type:        TypeToggle,
			Value:       boolToString(m.config.Display.ShowDurationInList),
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "true",
				RecommendedValue: "true",
				Sensitive:        false,
				DetailedDesc:     "Shows how long each request took in the log list. Useful for identifying slow requests and performance issues.",
			},
		},
		{
			Key:         "display.show_path_in_list",
			Label:       "Show Path in List",
			Description: "Display URL path",
			Type:        TypeToggle,
			Value:       boolToString(m.config.Display.ShowPathInList),
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "false",
				RecommendedValue: "false",
				Sensitive:        false,
				DetailedDesc:     "Shows the URL path (/v1/messages, etc.) in the log list. Usually not needed since most requests go to the same endpoint.",
			},
		},
		{
			Key:         "display.show_request_number",
			Label:       "Show Request Number",
			Description: "Display #01, #02 to correlate req/res",
			Type:        TypeToggle,
			Value:       boolToString(m.config.Display.ShowRequestNumber),
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "true",
				RecommendedValue: "true",
				Sensitive:        false,
				DetailedDesc:     "Shows a sequential number (#01, #02, etc.) in the log list. This number is shared between a request and its response, making it easy to match them up visually.",
			},
		},
		{
			Key:         "display.show_body_size",
			Label:       "Show Body Size",
			Description: "Display request/response body size",
			Type:        TypeToggle,
			Value:       boolToString(m.config.Display.ShowBodySize),
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "true",
				RecommendedValue: "true",
				Sensitive:        false,
				DetailedDesc:     "Shows the body size (2.1K, 1.5M, etc.) in the log list. Helps identify large requests/responses at a glance.",
			},
		},
		// Note: ShowSystemMessages is now controlled via the filter bar in the dashboard
		{
			Key:         "display.mouse_click_to_select",
			Label:       "Mouse Click to Select",
			Description: "Enable mouse click to select log entries",
			Type:        TypeToggle,
			Value:       boolToString(m.config.Display.MouseClickToSelect),
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "true",
				RecommendedValue: "true",
				Sensitive:        false,
				DetailedDesc:     "When enabled, clicking on a log entry in the list will select it. Disable if mouse interactions interfere with terminal selection.",
			},
		},
		{
			Key:         "display.list_width_percent",
			Label:       "List Width %",
			Description: "Width of log list panel",
			Type:        TypeNumber,
			Value:       fmt.Sprintf("%d", m.config.Display.ListWidthPercent),
			Min:         15,
			Max:         50,
			Step:        5,
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "35",
				RecommendedValue: "35",
				Sensitive:        false,
				DetailedDesc:     "Percentage of screen width for the log list panel in the dashboard. Lower values give more space to the detail view. Range: 15-50%.",
			},
		},
		{
			Key:         "display.theme",
			Label:       "Theme",
			Description: "Color theme",
			Type:        TypeSelect,
			Value:       m.config.Display.Theme,
			Options:     []string{"default", "light", "dark"},
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "default",
				RecommendedValue: "default",
				Sensitive:        false,
				DetailedDesc:     "Visual color theme. 'default' adapts to terminal, 'light' for bright backgrounds, 'dark' for dark backgrounds.",
			},
		},
		{
			Key:         "display.help_panel_position",
			Label:       "Help Panel Position",
			Description: "Where to show extended help",
			Type:        TypeSelect,
			Value:       m.config.Display.HelpPanelPosition,
			Options:     []string{"right", "bottom"},
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "right",
				RecommendedValue: "right",
				Sensitive:        false,
				DetailedDesc:     "Position of the extended help panel in Settings. 'right' shows it in the right 35% of the screen, 'bottom' shows it in the bottom half.",
			},
		},
		{
			Key:         "display.default_view_mode",
			Label:       "Default View Mode",
			Description: "Detail panel view mode on startup",
			Type:        TypeSelect,
			Value:       m.config.Display.DefaultViewMode,
			Options:     []string{"last", "parsed", "json", "raw"},
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "last",
				RecommendedValue: "last",
				Sensitive:        false,
				DetailedDesc:     "How to display log entries in the detail panel. 'last' remembers your previous choice, 'parsed' shows structured view, 'json' shows formatted JSON, 'raw' shows unprocessed content.",
			},
		},
		{
			Key:         "display.default_expand_mode",
			Label:       "Default Expand Mode",
			Description: "Content expansion on startup",
			Type:        TypeSelect,
			Value:       m.config.Display.DefaultExpandMode,
			Options:     []string{"last", "compact", "expanded"},
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "compact",
				RecommendedValue: "compact",
				Sensitive:        false,
				DetailedDesc:     "Whether to show truncated (compact) or full (expanded) content. 'last' remembers your previous choice, 'compact' truncates long content, 'expanded' shows everything.",
			},
		},
	}

	// Network settings
	m.settings[TabNetwork] = []Setting{
		{
			Key:         "network.http_timeout",
			Label:       "HTTP Timeout",
			Description: "Timeout for HTTP requests",
			Type:        TypeText,
			Value:       m.config.Network.HTTPTimeout.String(),
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "30s",
				RecommendedValue: "30s",
				Sensitive:        false,
				DetailedDesc:     "Maximum time to wait for HTTP requests to complete. Format: 30s, 1m, 2m. Longer values help with slow networks but delay error detection.",
				ReferenceURL:     "https://pkg.go.dev/time#ParseDuration",
			},
		},
		{
			Key:         "network.token_refresh_threshold",
			Label:       "Token Refresh Threshold",
			Description: "Refresh token when expiring within",
			Type:        TypeText,
			Value:       m.config.Network.TokenRefreshThreshold.String(),
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "5m",
				RecommendedValue: "5m",
				Sensitive:        false,
				DetailedDesc:     "Proactively refresh the access token when it will expire within this duration. Format: 5m, 10m. Helps avoid mid-request token expiration.",
				ReferenceURL:     "https://pkg.go.dev/time#ParseDuration",
			},
		},
		{
			Key:         "network.streaming_delay_max",
			Label:       "Max Streaming Delay",
			Description: "Random delay between SSE events",
			Type:        TypeText,
			Value:       m.config.Network.StreamingDelayMax.String(),
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "300ms",
				RecommendedValue: "300ms",
				Sensitive:        false,
				DetailedDesc:     "Maximum random delay added between streaming SSE events. Creates more natural-feeling output. Set to 0 for fastest streaming. Format: 100ms, 300ms.",
				ReferenceURL:     "https://pkg.go.dev/time#ParseDuration",
			},
		},
	}

	// Advanced settings
	m.settings[TabAdvanced] = []Setting{
		{
			Key:         "advanced.codewhisperer_endpoint",
			Label:       "CodeWhisperer Endpoint",
			Description: "AWS API endpoint for requests",
			Type:        TypeText,
			Value:       m.config.Advanced.CodeWhispererEndpoint,
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse",
				RecommendedValue: "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse",
				Sensitive:        true,
				DetailedDesc:     "The AWS CodeWhisperer API endpoint that receives translated requests. Only change this if AWS changes the endpoint URL or for testing purposes.",
				ReferenceURL:     "https://docs.aws.amazon.com/codewhisperer/",
			},
		},
		{
			Key:         "advanced.credits_endpoint",
			Label:       "Credits Endpoint",
			Description: "API endpoint for usage/credits",
			Type:        TypeText,
			Value:       m.config.Advanced.CreditsEndpoint,
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "https://codewhisperer.us-east-1.amazonaws.com/getUsageLimits",
				RecommendedValue: "https://codewhisperer.us-east-1.amazonaws.com/getUsageLimits",
				Sensitive:        true,
				DetailedDesc:     "The AWS API endpoint for fetching credit/usage information. Used to display remaining credits in the dashboard.",
			},
		},
		{
			Key:         "advanced.kiro_refresh_endpoint",
			Label:       "Kiro Refresh Endpoint",
			Description: "Endpoint for token refresh",
			Type:        TypeText,
			Value:       m.config.Advanced.KiroRefreshEndpoint,
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken",
				RecommendedValue: "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken",
				Sensitive:        true,
				DetailedDesc:     "The Kiro authentication server endpoint for refreshing access tokens. Only change if Kiro updates their auth infrastructure.",
			},
		},
		{
			Key:         "advanced.kiro_usage_url",
			Label:       "Kiro Usage URL",
			Description: "URL to usage/billing page",
			Type:        TypeText,
			Value:       m.config.Advanced.KiroUsageURL,
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "https://kiro.dev/usage",
				RecommendedValue: "https://kiro.dev/usage",
				Sensitive:        false,
				DetailedDesc:     "The web URL opened when you press 'u' to view your Kiro usage and billing. Opens in your default browser.",
				ReferenceURL:     "https://kiro.dev/usage",
			},
		},
		{
			Key:         "advanced.aws_region",
			Label:       "AWS Region",
			Description: "AWS region for API calls",
			Type:        TypeText,
			Value:       m.config.Advanced.AWSRegion,
			ExtendedHelp: ExtendedHelp{
				DefaultValue:     "us-east-1",
				RecommendedValue: "us-east-1",
				Sensitive:        false,
				DetailedDesc:     "The AWS region used for API calls. Currently only us-east-1 is supported by Kiro's backend.",
				ReferenceURL:     "https://docs.aws.amazon.com/general/latest/gr/rande.html",
			},
		},
	}
}

// saveSettings saves the current settings to config
func (m *Model) saveSettings() error {
	// Apply all settings back to config
	for _, settings := range m.settings {
		for _, s := range settings {
			m.applySetting(s)
		}
	}

	// Save to disk
	if err := m.config.Save(); err != nil {
		return err
	}

	// Update global config
	config.Set(m.config)
	return nil
}

// applySetting applies a single setting to the config
func (m *Model) applySetting(s Setting) {
	switch s.Key {
	// Server
	case "server.port":
		m.config.Server.Port = s.Value
	case "server.auto_start":
		m.config.Server.AutoStart = stringToBool(s.Value)
	case "server.shutdown_timeout":
		if d, err := parseDuration(s.Value); err == nil {
			m.config.Server.ShutdownTimeout = d
		}

	// Logging
	case "logging.enabled":
		m.config.Logging.Enabled = stringToBool(s.Value)
	case "logging.directory":
		m.config.Logging.Directory = s.Value
	case "logging.dashboard_retention":
		m.config.Logging.DashboardRetention = s.Value
	case "logging.file_retention":
		m.config.Logging.FileRetention = s.Value
	case "logging.max_log_size_mb":
		if v, err := strconv.Atoi(s.Value); err == nil {
			m.config.Logging.MaxLogSizeMB = v
		}
	case "logging.max_entries":
		if v, err := strconv.Atoi(s.Value); err == nil {
			m.config.Logging.MaxEntries = v
		}
	case "logging.file_content_length":
		if v, err := strconv.Atoi(s.Value); err == nil {
			m.config.Logging.FileContentLen = v
		}
	case "logging.preview_length":
		if v, err := strconv.Atoi(s.Value); err == nil {
			m.config.Logging.PreviewLength = v
		}

	// Display
	case "display.show_status_in_list":
		m.config.Display.ShowStatusInList = stringToBool(s.Value)
	case "display.show_duration_in_list":
		m.config.Display.ShowDurationInList = stringToBool(s.Value)
	case "display.show_path_in_list":
		m.config.Display.ShowPathInList = stringToBool(s.Value)
	case "display.show_request_number":
		m.config.Display.ShowRequestNumber = stringToBool(s.Value)
	case "display.show_body_size":
		m.config.Display.ShowBodySize = stringToBool(s.Value)
	case "display.mouse_click_to_select":
		m.config.Display.MouseClickToSelect = stringToBool(s.Value)
	// Note: show_system_messages is now controlled via filter bar
	case "display.list_width_percent":
		if v, err := strconv.Atoi(s.Value); err == nil && v >= 15 && v <= 50 {
			m.config.Display.ListWidthPercent = v
		}
	case "display.theme":
		m.config.Display.Theme = s.Value
	case "display.help_panel_position":
		m.config.Display.HelpPanelPosition = s.Value
	case "display.default_view_mode":
		m.config.Display.DefaultViewMode = s.Value
	case "display.default_expand_mode":
		m.config.Display.DefaultExpandMode = s.Value

	// Network
	case "network.http_timeout":
		if d, err := parseDuration(s.Value); err == nil {
			m.config.Network.HTTPTimeout = d
		}
	case "network.token_refresh_threshold":
		if d, err := parseDuration(s.Value); err == nil {
			m.config.Network.TokenRefreshThreshold = d
		}
	case "network.streaming_delay_max":
		if d, err := parseDuration(s.Value); err == nil {
			m.config.Network.StreamingDelayMax = d
		}

	// Advanced
	case "advanced.codewhisperer_endpoint":
		m.config.Advanced.CodeWhispererEndpoint = s.Value
	case "advanced.credits_endpoint":
		m.config.Advanced.CreditsEndpoint = s.Value
	case "advanced.kiro_refresh_endpoint":
		m.config.Advanced.KiroRefreshEndpoint = s.Value
	case "advanced.kiro_usage_url":
		m.config.Advanced.KiroUsageURL = s.Value
	case "advanced.aws_region":
		m.config.Advanced.AWSRegion = s.Value
	}
}

// Init initializes the model
func (m Model) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd

	// Handle exit modal
	if m.showExitModal {
		return m.handleExitModal(msg)
	}

	if m.editing {
		return m.handleEditing(msg)
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Reset double-esc tracker on any other key
		keyStr := msg.String()
		if keyStr != "esc" {
			m.escPressedOnce = false
		}

		switch keyStr {
		case "esc":
			// Handle exit with unsaved changes check
			if m.dirty && !m.savedSinceOpen {
				if m.escPressedOnce {
					// Double-esc: exit without saving
					if m.fromDashboard {
						return m, func() tea.Msg { return BackToDashboardMsg{} }
					}
					return m, func() tea.Msg { return BackToMenuMsg{} }
				}
				// First esc: show modal
				m.escPressedOnce = true
				m.showExitModal = true
				return m, nil
			}
			// No unsaved changes or already saved: just exit
			if m.fromDashboard {
				return m, func() tea.Msg { return BackToDashboardMsg{} }
			}
			return m, func() tea.Msg { return BackToMenuMsg{} }

		case "left":
			// Left: previous category
			m.activeTab = Tab((int(m.activeTab) - 1 + len(tabNames)) % len(tabNames))
			m.selectedItem = 0

		case "right":
			// Right: next category
			m.activeTab = Tab((int(m.activeTab) + 1) % len(tabNames))
			m.selectedItem = 0

		case "up":
			if m.selectedItem > 0 {
				m.selectedItem--
			}

		case "down":
			settings := m.settings[m.activeTab]
			if m.selectedItem < len(settings)-1 {
				m.selectedItem++
			}

		case "enter", " ":
			// Enter/space: toggle or enter edit mode
			m.handleEnterKey()

		case "s", "ctrl+s":
			// Save settings
			if err := m.saveSettings(); err != nil {
				m.statusMessage = fmt.Sprintf("Error saving: %v", err)
				m.statusErr = true
			} else {
				m.statusMessage = "Settings saved!"
				m.statusErr = false
				m.dirty = false
				m.savedSinceOpen = true
			}

		case "r":
			// Reset current setting to default
			m.resetCurrentSetting()
			m.dirty = true
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}

	return m, cmd
}

// handleExitModal handles key presses in the exit confirmation modal
func (m Model) handleExitModal(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up":
			// Navigate up in options
			if m.modalSelectedOpt > 0 {
				m.modalSelectedOpt--
			}
		case "down":
			// Navigate down in options
			if m.modalSelectedOpt < 2 {
				m.modalSelectedOpt++
			}
		case "enter", " ":
			// Confirm selected option
			switch m.modalSelectedOpt {
			case 0: // Save and exit
				if err := m.saveSettings(); err != nil {
					m.statusMessage = fmt.Sprintf("Error saving: %v", err)
					m.statusErr = true
					m.showExitModal = false
					return m, nil
				}
				if m.fromDashboard {
					return m, func() tea.Msg { return BackToDashboardMsg{} }
				}
				return m, func() tea.Msg { return BackToMenuMsg{} }
			case 1: // Exit without saving
				if m.fromDashboard {
					return m, func() tea.Msg { return BackToDashboardMsg{} }
				}
				return m, func() tea.Msg { return BackToMenuMsg{} }
			case 2: // Cancel
				m.showExitModal = false
				m.escPressedOnce = false
			}
		case "y":
			// Save and exit (shortcut)
			if err := m.saveSettings(); err != nil {
				m.statusMessage = fmt.Sprintf("Error saving: %v", err)
				m.statusErr = true
				m.showExitModal = false
				return m, nil
			}
			if m.fromDashboard {
				return m, func() tea.Msg { return BackToDashboardMsg{} }
			}
			return m, func() tea.Msg { return BackToMenuMsg{} }
		case "n":
			// Exit without saving (shortcut)
			if m.fromDashboard {
				return m, func() tea.Msg { return BackToDashboardMsg{} }
			}
			return m, func() tea.Msg { return BackToMenuMsg{} }
		case "esc", "c":
			// Cancel, stay in settings
			m.showExitModal = false
			m.escPressedOnce = false
		}
	}
	return m, nil
}


// handleEnterKey handles enter/space for toggling or entering edit mode
func (m *Model) handleEnterKey() {
	settings := m.settings[m.activeTab]
	if m.selectedItem >= len(settings) {
		return
	}

	s := &settings[m.selectedItem]
	switch s.Type {
	case TypeToggle:
		// Toggle the value
		if s.Value == "true" {
			s.Value = "false"
		} else {
			s.Value = "true"
		}
		m.dirty = true
		m.settings[m.activeTab] = settings
	case TypeSelect:
		// Cycle through options
		for i, opt := range s.Options {
			if opt == s.Value {
				s.Value = s.Options[(i+1)%len(s.Options)]
				break
			}
		}
		m.dirty = true
		m.settings[m.activeTab] = settings
	case TypeText, TypeNumber:
		// Enter edit mode, store original value for Esc cancel
		m.editOriginalValue = s.Value
		m.editing = true
		m.textInput.SetValue(s.Value)
		m.textInput.Focus()
		m.textInput.CursorEnd()
	}
}

// handleEditing handles input while in edit mode
func (m Model) handleEditing(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd

	settings := m.settings[m.activeTab]
	if m.selectedItem >= len(settings) {
		m.editing = false
		m.textInput.Blur()
		return m, nil
	}
	s := &settings[m.selectedItem]

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			// Save the value
			newValue := m.textInput.Value()

			// Validate number fields
			if s.Type == TypeNumber {
				if v, err := strconv.Atoi(newValue); err != nil {
					m.statusMessage = "Invalid number"
					m.statusErr = true
					m.editing = false
					m.textInput.Blur()
					return m, nil
				} else {
					// Clamp to range
					if s.Min != 0 || s.Max != 0 {
						if v < s.Min {
							v = s.Min
						}
						if v > s.Max {
							v = s.Max
						}
						newValue = fmt.Sprintf("%d", v)
					}
				}
			}

			s.Value = newValue
			m.settings[m.activeTab] = settings
			m.dirty = true
			m.editing = false
			m.textInput.Blur()
			return m, nil

		case "esc":
			// Cancel editing - restore original value
			s.Value = m.editOriginalValue
			m.settings[m.activeTab] = settings
			m.editing = false
			m.textInput.Blur()
			return m, nil

		case "up":
			// For numbers: increment by 1
			if s.Type == TypeNumber {
				if v, err := strconv.Atoi(m.textInput.Value()); err == nil {
					newVal := v + 1
					if s.Max != 0 && newVal > s.Max {
						newVal = s.Max
					}
					m.textInput.SetValue(fmt.Sprintf("%d", newVal))
					m.textInput.CursorEnd()
				}
				return m, nil
			}

		case "down":
			// For numbers: decrement by 1
			if s.Type == TypeNumber {
				if v, err := strconv.Atoi(m.textInput.Value()); err == nil {
					newVal := v - 1
					if newVal < s.Min {
						newVal = s.Min
					}
					m.textInput.SetValue(fmt.Sprintf("%d", newVal))
					m.textInput.CursorEnd()
				}
				return m, nil
			}
		}
	}

	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

// resetCurrentSetting resets the current setting to its default value
func (m *Model) resetCurrentSetting() {
	settings := m.settings[m.activeTab]
	if m.selectedItem >= len(settings) {
		return
	}

	s := &settings[m.selectedItem]
	s.Value = s.ExtendedHelp.DefaultValue
	m.settings[m.activeTab] = settings
	m.statusMessage = "Reset to default"
	m.statusErr = false
}

// View renders the settings view
func (m Model) View() string {
	// Colors
	purple := lipgloss.Color("#7D56F4")
	dim := lipgloss.Color("#626262")
	green := lipgloss.Color("#04B575")

	helpPosition := m.config.Display.HelpPanelPosition
	if helpPosition == "" {
		helpPosition = "right"
	}

	// Calculate panel dimensions
	var mainWidth, helpWidth, mainHeight, helpHeight int
	if helpPosition == "right" {
		helpWidth = (m.width - 8) * 35 / 100
		mainWidth = m.width - 8 - helpWidth - 3 // 3 for separator
		mainHeight = m.height - 6
		helpHeight = mainHeight
	} else {
		mainWidth = m.width - 8
		helpWidth = mainWidth
		helpHeight = (m.height - 6) / 2
		mainHeight = m.height - 6 - helpHeight - 1
	}

	// Main container style
	containerStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(purple).
		Padding(1, 2).
		Width(m.width - 4).
		Height(m.height - 4)

	// Title
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(purple).
		MarginBottom(1)
	title := titleStyle.Render("Settings")

	// Tab bar
	tabBar := m.renderTabBar()

	// Settings list for current tab
	settingsContent := m.renderSettings(mainWidth - 4)

	// Help panel
	helpPanel := m.renderHelpPanel(helpWidth, helpHeight)

	// Help text (italic like Claude Code style)
	helpStyle := lipgloss.NewStyle().
		Foreground(dim).
		Italic(true).
		MarginTop(1)

	var helpText string
	if m.editing {
		settings := m.settings[m.activeTab]
		if m.selectedItem < len(settings) && settings[m.selectedItem].Type == TypeNumber {
			helpText = helpStyle.Render("up/down ±1 | enter save | esc cancel")
		} else {
			helpText = helpStyle.Render("enter save | esc cancel")
		}
	} else {
		dirtyIndicator := ""
		if m.dirty && !m.savedSinceOpen {
			dirtyIndicator = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFAA00")).Bold(true).Render(" [unsaved]")
		}
		helpText = helpStyle.Render("left/right category | up/down navigate | enter edit | s save | r reset | esc back") + dirtyIndicator
	}

	// Status bar
	var statusBar string
	if m.statusMessage != "" {
		var statusStyle lipgloss.Style
		if m.statusErr {
			statusStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FF6B6B")).
				Bold(true)
		} else {
			statusStyle = lipgloss.NewStyle().
				Foreground(green).
				Bold(true)
		}
		statusBar = statusStyle.Render(m.statusMessage)
	}

	// Build main content area
	mainContent := lipgloss.JoinVertical(lipgloss.Left,
		title,
		"",
		tabBar,
		"",
		settingsContent,
	)

	// Combine main and help panels based on position
	var combinedContent string
	if helpPosition == "right" {
		// Side by side layout
		combinedContent = lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(mainWidth).Render(mainContent),
			lipgloss.NewStyle().Width(1).Render(" "),
			helpPanel,
		)
	} else {
		// Stacked layout
		separator := lipgloss.NewStyle().
			Foreground(dim).
			Render(strings.Repeat("─", mainWidth))

		combinedContent = lipgloss.JoinVertical(lipgloss.Left,
			mainContent,
			separator,
			helpPanel,
		)
	}

	// Add help and status
	content := lipgloss.JoinVertical(lipgloss.Left,
		combinedContent,
		"",
		helpText,
	)

	if statusBar != "" {
		content = lipgloss.JoinVertical(lipgloss.Left,
			content,
			statusBar,
		)
	}

	rendered := containerStyle.Render(content)

	// Overlay exit modal if showing
	if m.showExitModal {
		rendered = m.renderExitModal(rendered)
	}

	return rendered
}

// renderExitModal renders the exit confirmation modal overlayed on the content
func (m Model) renderExitModal(background string) string {
	purple := lipgloss.Color("#7D56F4")
	dim := lipgloss.Color("#626262")
	white := lipgloss.Color("#FAFAFA")
	orange := lipgloss.Color("#FFAA00")
	green := lipgloss.Color("#04B575")

	// Modal content - no background color to avoid black boxes
	modalStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(purple).
		Padding(1, 2)

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(orange)

	textStyle := lipgloss.NewStyle().
		Foreground(white)

	// Option styles based on selection
	selectedStyle := lipgloss.NewStyle().
		Foreground(green).
		Bold(true)

	unselectedStyle := lipgloss.NewStyle().
		Foreground(dim)

	// Render options with selection indicator
	opt0Style := unselectedStyle
	opt1Style := unselectedStyle
	opt2Style := unselectedStyle
	if m.modalSelectedOpt == 0 {
		opt0Style = selectedStyle
	} else if m.modalSelectedOpt == 1 {
		opt1Style = selectedStyle
	} else if m.modalSelectedOpt == 2 {
		opt2Style = selectedStyle
	}

	indicator0 := "  "
	indicator1 := "  "
	indicator2 := "  "
	if m.modalSelectedOpt == 0 {
		indicator0 = "▸ "
	} else if m.modalSelectedOpt == 1 {
		indicator1 = "▸ "
	} else if m.modalSelectedOpt == 2 {
		indicator2 = "▸ "
	}

	modalContent := lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("Unsaved Changes"),
		"",
		textStyle.Render("You have unsaved changes."),
		textStyle.Render("Do you want to save before exiting?"),
		"",
		opt0Style.Render(indicator0+"[y] Save and exit"),
		opt1Style.Render(indicator1+"[n] Exit without saving"),
		opt2Style.Render(indicator2+"[esc] Cancel"),
		"",
		lipgloss.NewStyle().Foreground(dim).Italic(true).Render("↑/↓ select • enter confirm"),
	)

	modal := modalStyle.Render(modalContent)

	// Center the modal on the viewport (use m.width/m.height, not background size)
	modalWidth := lipgloss.Width(modal)
	modalHeight := lipgloss.Height(modal)

	// Split background into lines
	bgLines := strings.Split(background, "\n")

	// Calculate position to center modal on viewport
	startRow := (m.height - modalHeight) / 2
	startCol := (m.width - modalWidth) / 2
	if startRow < 0 {
		startRow = 0
	}
	if startCol < 0 {
		startCol = 0
	}

	// Overlay modal onto background
	modalLines := strings.Split(modal, "\n")
	for i, modalLine := range modalLines {
		row := startRow + i
		if row >= 0 && row < len(bgLines) {
			// Replace characters in this row with modal line
			bgLine := bgLines[row]
			bgRunes := []rune(bgLine)

			// Pad background line if needed
			for len(bgRunes) < startCol+lipgloss.Width(modalLine) {
				bgRunes = append(bgRunes, ' ')
			}

			// Create the new line with modal overlayed
			prefix := string(bgRunes[:startCol])
			suffix := ""
			suffixStart := startCol + lipgloss.Width(modalLine)
			if suffixStart < len(bgRunes) {
				suffix = string(bgRunes[suffixStart:])
			}

			bgLines[row] = prefix + modalLine + suffix
		}
	}

	return strings.Join(bgLines, "\n")
}

// renderTabBar renders the tab navigation bar
func (m Model) renderTabBar() string {
	purple := lipgloss.Color("#7D56F4")
	white := lipgloss.Color("#FAFAFA")
	dim := lipgloss.Color("#626262")

	activeStyle := lipgloss.NewStyle().
		Foreground(white).
		Background(purple).
		Bold(true).
		Padding(0, 2)

	inactiveStyle := lipgloss.NewStyle().
		Foreground(dim).
		Padding(0, 2)

	var tabs []string
	for i, name := range tabNames {
		if Tab(i) == m.activeTab {
			tabs = append(tabs, activeStyle.Render(name))
		} else {
			tabs = append(tabs, inactiveStyle.Render(name))
		}
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
}

// renderSettings renders the settings list for the current tab
func (m Model) renderSettings(maxWidth int) string {
	purple := lipgloss.Color("#7D56F4")
	dim := lipgloss.Color("#626262")
	green := lipgloss.Color("#04B575")
	white := lipgloss.Color("#FAFAFA")
	orange := lipgloss.Color("#FFAA00")

	settings := m.settings[m.activeTab]
	var lines []string

	labelWidth := 26
	if maxWidth > 60 {
		labelWidth = 30
	}

	for i, s := range settings {
		isSelected := i == m.selectedItem

		// Label style
		labelStyle := lipgloss.NewStyle().Width(labelWidth)
		if isSelected {
			labelStyle = labelStyle.Foreground(purple).Bold(true)
		} else {
			labelStyle = labelStyle.Foreground(white)
		}

		// Value rendering
		var valueStr string
		switch s.Type {
		case TypeToggle:
			if s.Value == "true" {
				valueStr = lipgloss.NewStyle().Foreground(green).Render("[ON]")
			} else {
				valueStr = lipgloss.NewStyle().Foreground(dim).Render("[OFF]")
			}
		case TypeSelect:
			valueStr = lipgloss.NewStyle().Foreground(purple).Render("[" + s.Value + "]")
		case TypeNumber:
			if m.editing && isSelected {
				valueStr = m.textInput.View()
			} else {
				valueStr = lipgloss.NewStyle().Foreground(orange).Render(s.Value)
			}
		default:
			if m.editing && isSelected {
				valueStr = m.textInput.View()
			} else {
				valueStr = lipgloss.NewStyle().Foreground(white).Render(truncate(s.Value, maxWidth-labelWidth-10))
			}
		}

		// Selection indicator
		indicator := "  "
		if isSelected {
			indicator = lipgloss.NewStyle().Foreground(purple).Render("▸ ")
		}

		// Main line
		line := indicator + labelStyle.Render(s.Label) + " " + valueStr
		lines = append(lines, line)

		// Short description (only for selected item when not showing help panel)
		if isSelected {
			descStyle := lipgloss.NewStyle().
				Foreground(dim).
				PaddingLeft(4)
			lines = append(lines, descStyle.Render(s.Description))
		}
	}

	return strings.Join(lines, "\n")
}

// renderHelpPanel renders the extended help panel
func (m Model) renderHelpPanel(width, height int) string {
	purple := lipgloss.Color("#7D56F4")
	dim := lipgloss.Color("#626262")
	white := lipgloss.Color("#FAFAFA")
	green := lipgloss.Color("#04B575")
	orange := lipgloss.Color("#FFAA00")
	red := lipgloss.Color("#FF6B6B")
	cyan := lipgloss.Color("#00FFFF")

	settings := m.settings[m.activeTab]
	if m.selectedItem >= len(settings) {
		return ""
	}

	s := settings[m.selectedItem]
	help := s.ExtendedHelp

	// Panel style
	panelStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(purple).
		Padding(1, 2).
		Width(width - 2)

	// Title
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(purple)

	// Labels
	labelStyle := lipgloss.NewStyle().
		Foreground(dim).
		Width(12)

	valueStyle := lipgloss.NewStyle().
		Foreground(white)

	var lines []string

	// Setting name
	lines = append(lines, titleStyle.Render(s.Label))
	lines = append(lines, "")

	// Current value
	currentStyle := lipgloss.NewStyle().Foreground(green).Bold(true)
	lines = append(lines, labelStyle.Render("Current:")+" "+currentStyle.Render(s.Value))

	// Default value
	lines = append(lines, labelStyle.Render("Default:")+" "+valueStyle.Render(help.DefaultValue))

	// Recommended value (if different from default)
	if help.RecommendedValue != "" && help.RecommendedValue != help.DefaultValue {
		recStyle := lipgloss.NewStyle().Foreground(orange)
		lines = append(lines, labelStyle.Render("Recommend:")+" "+recStyle.Render(help.RecommendedValue))
	}

	// Show current usage stats for relevant logging settings
	statsStyle := lipgloss.NewStyle().Foreground(cyan).Bold(true)
	switch s.Key {
	case "logging.directory", "logging.file_retention", "logging.max_log_size_mb", "logging.file_content_length":
		// Show disk usage for file-related settings
		lines = append(lines, "")
		diskUsage := config.FormatBytes(m.logDiskSize)
		maxSize := m.config.Logging.MaxLogSizeMB
		if maxSize > 0 {
			pct := float64(m.logDiskSize) / float64(maxSize*1024*1024) * 100
			lines = append(lines, labelStyle.Render("Disk Used:")+" "+statsStyle.Render(fmt.Sprintf("%s / %d MB (%.1f%%)", diskUsage, maxSize, pct)))
		} else {
			lines = append(lines, labelStyle.Render("Disk Used:")+" "+statsStyle.Render(diskUsage+" (no limit)"))
		}
	case "logging.max_entries", "logging.dashboard_retention":
		// Show memory usage for memory-related settings
		lines = append(lines, "")
		memUsage := config.FormatBytes(int64(m.logMemorySize))
		lines = append(lines, labelStyle.Render("Memory:")+" "+statsStyle.Render(fmt.Sprintf("%s (%d entries)", memUsage, m.logEntryCount)))
	}

	// Sensitive indicator
	if help.Sensitive {
		sensitiveStyle := lipgloss.NewStyle().Foreground(red).Bold(true)
		lines = append(lines, "")
		lines = append(lines, sensitiveStyle.Render("⚠ SENSITIVE - Change with caution"))
	}

	// Detailed description
	if help.DetailedDesc != "" {
		lines = append(lines, "")
		// Word wrap the description
		wrapped := wordWrap(help.DetailedDesc, width-8)
		descStyle := lipgloss.NewStyle().Foreground(white)
		lines = append(lines, descStyle.Render(wrapped))
	}

	// Reference URL
	if help.ReferenceURL != "" {
		lines = append(lines, "")
		urlStyle := lipgloss.NewStyle().Foreground(purple).Underline(true)
		lines = append(lines, labelStyle.Render("Reference:")+" "+urlStyle.Render(help.ReferenceURL))
	}

	// Type-specific hints
	lines = append(lines, "")
	hintStyle := lipgloss.NewStyle().Foreground(dim).Italic(true)
	switch s.Type {
	case TypeToggle:
		lines = append(lines, hintStyle.Render("Press Enter or Space to toggle"))
	case TypeSelect:
		lines = append(lines, hintStyle.Render("Press Enter or Space to cycle options"))
		lines = append(lines, hintStyle.Render("Options: "+strings.Join(s.Options, ", ")))
	case TypeNumber:
		hint := "Press Enter to edit, Up/Down to adjust ±1"
		if s.Min != 0 || s.Max != 0 {
			hint += fmt.Sprintf("\nRange: %d - %d", s.Min, s.Max)
		}
		lines = append(lines, hintStyle.Render(hint))
	case TypeText:
		lines = append(lines, hintStyle.Render("Press Enter to edit, Esc to cancel"))
	}

	content := strings.Join(lines, "\n")
	return panelStyle.Render(content)
}

// SetSize updates the dimensions
func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// Helper functions
func boolToString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func stringToBool(s string) bool {
	return s == "true"
}

func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 20
	}
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func wordWrap(s string, width int) string {
	if width <= 0 {
		width = 40
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return s
	}

	var lines []string
	var currentLine string

	for _, word := range words {
		if currentLine == "" {
			currentLine = word
		} else if len(currentLine)+1+len(word) <= width {
			currentLine += " " + word
		} else {
			lines = append(lines, currentLine)
			currentLine = word
		}
	}
	if currentLine != "" {
		lines = append(lines, currentLine)
	}

	return strings.Join(lines, "\n")
}

// parseDuration parses a duration string with better error handling
func parseDuration(s string) (time.Duration, error) {
	// Import time for parsing
	return time.ParseDuration(s)
}

// KeyMap for settings
type KeyMap struct {
	Tab      key.Binding
	ShiftTab key.Binding
	Up       key.Binding
	Down     key.Binding
	Left     key.Binding
	Right    key.Binding
	Enter    key.Binding
	Save     key.Binding
	Reset    key.Binding
	Back     key.Binding
}

// DefaultKeyMap returns the default key bindings
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "next category"),
		),
		ShiftTab: key.NewBinding(
			key.WithKeys("shift+tab"),
			key.WithHelp("shift+tab", "prev category"),
		),
		Up: key.NewBinding(
			key.WithKeys("up"),
			key.WithHelp("↑", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down"),
			key.WithHelp("↓", "down"),
		),
		Left: key.NewBinding(
			key.WithKeys("left"),
			key.WithHelp("←", "prev category"),
		),
		Right: key.NewBinding(
			key.WithKeys("right"),
			key.WithHelp("→", "next category"),
		),
		Enter: key.NewBinding(
			key.WithKeys("enter", " "),
			key.WithHelp("enter", "edit/toggle"),
		),
		Save: key.NewBinding(
			key.WithKeys("s", "ctrl+s"),
			key.WithHelp("s", "save"),
		),
		Reset: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "reset to default"),
		),
		Back: key.NewBinding(
			key.WithKeys("esc", "q"),
			key.WithHelp("esc", "back"),
		),
	}
}
