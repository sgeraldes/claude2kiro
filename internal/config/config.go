package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all application configuration
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Logging  LoggingConfig  `yaml:"logging"`
	Display  DisplayConfig  `yaml:"display"`
	Network  NetworkConfig  `yaml:"network"`
	Advanced AdvancedConfig `yaml:"advanced"`
	Filter   FilterConfig   `yaml:"filter,omitempty"`
}

// ServerConfig holds server-related settings
type ServerConfig struct {
	Port             string        `yaml:"port"`
	AutoStart        bool          `yaml:"auto_start"`
	AutoLaunchClaude bool          `yaml:"auto_launch_claude"` // Launch Claude Code in new terminal when server starts
	StartView        string        `yaml:"start_view"`         // "menu" or "dashboard" - initial TUI view
	ShutdownTimeout  time.Duration `yaml:"shutdown_timeout"`
}

// LoggingConfig holds logging-related settings
type LoggingConfig struct {
	Enabled            bool   `yaml:"enabled"`
	Directory          string `yaml:"directory"`
	DashboardRetention string `yaml:"dashboard_retention"` // "24h", "48h", "72h", "unlimited" - in-memory session display
	FileRetention      string `yaml:"file_retention"`      // "7d", "30d", "90d", "unlimited" - log file retention on disk
	MaxLogSizeMB       int    `yaml:"max_log_size_mb"`     // Max total log directory size in MB (0 = unlimited)
	MaxEntries         int    `yaml:"max_entries"`         // Max entries in memory
	FileContentLen     int    `yaml:"file_content_length"` // Max chars per entry in file (0 = unlimited)
	MaxBodySizeKB      int    `yaml:"max_body_size_kb"`    // Max body size to store in memory per entry in KB (0 = unlimited, default 1024)
	PreviewLength      int    `yaml:"preview_length"`      // Preview length in list view
	AttachmentMode     string `yaml:"attachment_mode"`     // "full", "placeholder", "separate" - how to handle base64 attachments
}

// DisplayConfig holds UI display settings
type DisplayConfig struct {
	ShowStatusInList   bool   `yaml:"show_status_in_list"`
	ShowDurationInList bool   `yaml:"show_duration_in_list"`
	ShowPathInList     bool   `yaml:"show_path_in_list"`
	ShowRequestNumber  bool   `yaml:"show_request_number"`   // Show #01, #02 to correlate req/res pairs
	ShowBodySize       bool   `yaml:"show_body_size"`        // Show body size column (2.1K, 1.5M)
	ShowSystemMessages bool   `yaml:"show_system_messages"`  // Show INF/ERR entries in log list
	MouseClickToSelect bool   `yaml:"mouse_click_to_select"` // Enable mouse click to select entries
	ListWidthPercent   int    `yaml:"list_width_percent"`
	Theme              string `yaml:"theme"`               // "default", "light", "dark"
	HelpPanelPosition  string `yaml:"help_panel_position"` // "right" or "bottom"
	DefaultViewMode    string `yaml:"default_view_mode"`   // "last", "parsed", "json", "raw"
	DefaultExpandMode  string `yaml:"default_expand_mode"` // "last", "compact", "expanded"
	MaxDisplaySizeKB   int    `yaml:"max_display_size_kb"` // Max content size to display in KB (0 = unlimited, default 1024)
	TruncateBase64     bool   `yaml:"truncate_base64"`     // Replace base64 blobs with size placeholders (default true)
}

// NetworkConfig holds network-related settings
type NetworkConfig struct {
	HTTPTimeout           time.Duration `yaml:"http_timeout"`
	TokenRefreshThreshold time.Duration `yaml:"token_refresh_threshold"`
	StreamingDelayMax     time.Duration `yaml:"streaming_delay_max"`
	MaxConcurrentReqs     int           `yaml:"max_concurrent_requests"` // Max parallel requests to Kiro backend
	MaxToolsPerRequest    int           `yaml:"max_tools_per_request"`   // Max tools sent per request (Kiro limit)
}

// AdvancedConfig holds advanced/API settings
type AdvancedConfig struct {
	CodeWhispererEndpoint string `yaml:"codewhisperer_endpoint"`
	CreditsEndpoint       string `yaml:"credits_endpoint"`
	KiroAuthEndpoint      string `yaml:"kiro_auth_endpoint"`
	KiroRefreshEndpoint   string `yaml:"kiro_refresh_endpoint"`
	KiroUsageURL          string `yaml:"kiro_usage_url"`
	AWSRegion             string `yaml:"aws_region"`
	ComparisonMode        bool   `yaml:"comparison_mode"`     // Debug: send to both Anthropic and Kiro
	AnthropicDirect       bool   `yaml:"anthropic_direct"`    // Bypass: send only to Anthropic
	AnthropicApiKey       string `yaml:"anthropic_api_key"`   // API key for Anthropic (required for comparison/direct modes)
	UseNewSSEBuilder      bool   `yaml:"use_new_sse_builder"` // Feature flag: use new sse.EventBuilder (default: false)
	SkipPermissions       bool   `yaml:"skip_permissions"`    // Pass --dangerously-skip-permissions to claude (default: true)
	DebugMode             bool   `yaml:"debug_mode"`          // Write debug files per request
}

// FilterConfig holds log filter settings (persisted across sessions)
type FilterConfig struct {
	ClearAfter     time.Time `yaml:"clear_after,omitempty"`      // Show only entries after this timestamp
	ShowReq        *bool     `yaml:"show_req,omitempty"`         // Show request entries (nil = use default true)
	ShowRes        *bool     `yaml:"show_res,omitempty"`         // Show response entries (nil = use default true)
	ShowInf        *bool     `yaml:"show_inf,omitempty"`         // Show info entries (nil = use default false)
	ShowErr        *bool     `yaml:"show_err,omitempty"`         // Show error entries (nil = use default false)
	LastViewMode   string    `yaml:"last_view_mode,omitempty"`   // Last used view mode: "parsed", "json", "raw"
	LastExpandMode string    `yaml:"last_expand_mode,omitempty"` // Last used expand mode: "compact", "expanded"
}

// Default returns the default configuration
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Port:             "8080",
			AutoStart:        false,
			AutoLaunchClaude: false,  // Set to true to auto-launch Claude Code when server starts
			StartView:        "menu", // "menu" or "dashboard"
			ShutdownTimeout:  5 * time.Second,
		},
		Logging: LoggingConfig{
			Enabled:            true,
			Directory:          "~/.claude2kiro/logs/",
			DashboardRetention: "48h",
			FileRetention:      "unlimited",
			MaxLogSizeMB:       100,
			MaxEntries:         500,
			FileContentLen:     0,          // 0 = unlimited
			MaxBodySizeKB:      1024,       // 1MB default limit for in-memory body storage
			PreviewLength:      10000,      // 0 = unlimited, max 200000
			AttachmentMode:     "separate", // "full", "placeholder", "separate"
		},
		Display: DisplayConfig{
			ShowStatusInList:   true,
			ShowDurationInList: true,
			ShowPathInList:     false,
			ShowRequestNumber:  true,
			ShowBodySize:       true,
			ShowSystemMessages: true,
			MouseClickToSelect: true,
			ListWidthPercent:   35,
			Theme:              "default",
			HelpPanelPosition:  "right",
			DefaultViewMode:    "last",    // "last", "parsed", "json", "raw"
			DefaultExpandMode:  "compact", // "last", "compact", "expanded"
			MaxDisplaySizeKB:   1024,      // 1MB default limit for display
			TruncateBase64:     true,      // Replace base64 blobs with placeholders
		},
		Network: NetworkConfig{
			HTTPTimeout:           30 * time.Second,
			TokenRefreshThreshold: 5 * time.Minute,
			StreamingDelayMax:     0,  // No artificial delay - Claude Code handles rendering pacing
			MaxConcurrentReqs:     4,  // Max parallel requests to Kiro backend
			MaxToolsPerRequest:    85, // Kiro rejects requests with ~95+ tools
		},
		Advanced: AdvancedConfig{
			CodeWhispererEndpoint: "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse",
			CreditsEndpoint:       "https://q.us-east-1.amazonaws.com/getUsageLimits",
			KiroAuthEndpoint:      "https://prod.us-east-1.auth.desktop.kiro.dev",
			KiroRefreshEndpoint:   "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken",
			KiroUsageURL:          "https://kiro.dev/usage",
			AWSRegion:             "us-east-1",
			SkipPermissions:       true,
			DebugMode:             false,
		},
	}
}

// configPath returns the path to the config file
func configPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".claude2kiro", "config.yaml"), nil
}

// Load loads configuration from file, returning defaults if file doesn't exist
func Load() (*Config, error) {
	cfg := Default()

	path, err := configPath()
	if err != nil {
		return cfg, nil // Return defaults on error
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // Return defaults if file doesn't exist
		}
		return cfg, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return Default(), err // Return defaults on parse error
	}

	return cfg, nil
}

// Save saves the configuration to file
func (c *Config) Save() error {
	path, err := configPath()
	if err != nil {
		return err
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0600)
}

// ExpandPath expands ~ to home directory
func ExpandPath(path string) string {
	if len(path) > 0 && path[0] == '~' {
		if homeDir, err := os.UserHomeDir(); err == nil {
			return filepath.Join(homeDir, path[1:])
		}
	}
	return path
}

// Global config instance
var current *Config

// Get returns the current configuration (loads if not yet loaded)
func Get() *Config {
	if current == nil {
		current, _ = Load()
	}
	return current
}

// Set sets the current configuration
func Set(cfg *Config) {
	current = cfg
}

// Reload reloads configuration from file
func Reload() error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	current = cfg
	return nil
}

// GetLogDirSize returns the total size of the log directory in bytes
func GetLogDirSize() (int64, error) {
	cfg := Get()
	dir := ExpandPath(cfg.Logging.Directory)

	var totalSize int64
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Ignore errors (e.g., permission denied)
		}
		if !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})
	return totalSize, err
}

// FormatBytes formats bytes into human-readable string
func FormatBytes(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
