package tui

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sgeraldes/claude2kiro/cmd"
	"github.com/sgeraldes/claude2kiro/internal/attachments"
	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/tui/dashboard"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
	"github.com/sgeraldes/claude2kiro/internal/tui/login"
	"github.com/sgeraldes/claude2kiro/internal/tui/loginprogress"
	"github.com/sgeraldes/claude2kiro/internal/tui/menu"
	"github.com/sgeraldes/claude2kiro/internal/tui/settings"
)

// Commands holds the command functions from main
type Commands struct {
	Login           func() tea.Msg
	StartServer     func(port string, lg *logger.Logger) func() tea.Msg
	StopServer      func() tea.Msg
	RefreshToken    func() tea.Msg
	ViewToken       func() tea.Msg
	ExportEnv       func() tea.Msg
	ConfigureClaude func() tea.Msg
	Unconfigure     func() tea.Msg
	ViewCredits     func() tea.Msg
	Logout          func() tea.Msg
	GetTokenExpiry  func() time.Time
	HasToken        func() bool
	IsTokenExpired  func() bool
	TryRefreshToken func() error
}

// Model is the root TUI model
type Model struct {
	state              AppState
	menu               menu.Model
	login              login.Model
	loginProgress      loginprogress.Model
	dashboard          dashboard.Model
	settings           settings.Model
	width              int
	height             int
	commands           Commands
	logger             *logger.Logger
	attachmentStore    *attachments.Store
	program            *tea.Program
	serverRunning      bool
	serverStarting     bool
	serverPort         string
	autoStartAttempted bool
}

// NewRootModel creates a new root model
func NewRootModel(cmds Commands) Model {
	cfg := config.Get()
	lg := logger.NewLogger(cfg.Logging.MaxEntries)

	// Create attachment store for deduplication
	var attachStore *attachments.Store
	if cfg.Logging.AttachmentMode == "separate" {
		logDir := config.ExpandPath(cfg.Logging.Directory)
		attachDir := filepath.Join(filepath.Dir(logDir), "attachments")
		if store, err := attachments.NewStore(attachDir); err == nil {
			attachStore = store
			lg.SetAttachmentStore(store)
		}
	}

	// Enable file logging if configured
	if cfg.Logging.Enabled {
		logDir := config.ExpandPath(cfg.Logging.Directory)
		if err := lg.EnableFileLogging(logDir); err == nil {
			totalLoaded := 0

			// Determine how many days of logs to load based on DashboardRetention
			daysToLoad := parseDashboardRetentionDays(cfg.Logging.DashboardRetention)

			// Load logs from previous days (oldest first so entries are in order)
			now := time.Now()
			for i := daysToLoad - 1; i >= 1; i-- {
				pastDate := now.AddDate(0, 0, -i)
				pastFile := filepath.Join(logDir, pastDate.Format("2006-01-02")+".log")
				if count, err := lg.LoadFromFile(pastFile); err == nil && count > 0 {
					totalLoaded += count
				}
			}

			// Load today's log file
			logPath := lg.FilePath()
			if count, err := lg.LoadFromFile(logPath); err == nil && count > 0 {
				totalLoaded += count
			}

			if totalLoaded > 0 {
				lg.LogInfo(fmt.Sprintf("Loaded %d previous log entries", totalLoaded))
			}
			lg.LogInfo(fmt.Sprintf("Logs saved to: %s", logPath))
		}
	}

	port := cfg.Server.Port
	tokenExpiry := time.Time{}
	if cmds.GetTokenExpiry != nil {
		tokenExpiry = cmds.GetTokenExpiry()
	}

	// Create wrapper functions for dashboard
	var refreshFn dashboard.RefreshTokenFunc
	var isExpiredFn dashboard.IsTokenExpiredFunc

	if cmds.TryRefreshToken != nil && cmds.GetTokenExpiry != nil {
		refreshFn = func() (time.Time, error) {
			err := cmds.TryRefreshToken()
			if err != nil {
				return time.Time{}, err
			}
			return cmds.GetTokenExpiry(), nil
		}
	}

	if cmds.IsTokenExpired != nil {
		isExpiredFn = cmds.IsTokenExpired
	}

	// Determine initial state based on config and login status
	initialState := StateMenu
	if config.Get().Server.StartView != "menu" && cmds.HasToken != nil && cmds.HasToken() {
		initialState = StateDashboard
	}

	return Model{
		state:           initialState,
		menu:            menu.New(80, 24),
		dashboard:       dashboard.New(port, tokenExpiry, lg, attachStore, refreshFn, isExpiredFn),
		commands:        cmds,
		logger:          lg,
		attachmentStore: attachStore,
		serverPort:      port,
	}
}

// SetProgram sets the tea.Program reference
func (m *Model) SetProgram(p *tea.Program) {
	m.program = p
	m.logger.SetProgram(p)
}

// createDashboard creates a new dashboard with the proper token functions
func (m *Model) createDashboard(port string, tokenExpiry time.Time) dashboard.Model {
	var refreshFn dashboard.RefreshTokenFunc
	var isExpiredFn dashboard.IsTokenExpiredFunc

	if m.commands.TryRefreshToken != nil && m.commands.GetTokenExpiry != nil {
		refreshFn = func() (time.Time, error) {
			err := m.commands.TryRefreshToken()
			if err != nil {
				return time.Time{}, err
			}
			return m.commands.GetTokenExpiry(), nil
		}
	}

	if m.commands.IsTokenExpired != nil {
		isExpiredFn = m.commands.IsTokenExpired
	}

	return dashboard.New(port, tokenExpiry, m.logger, m.attachmentStore, refreshFn, isExpiredFn)
}

// Init initializes the model
func (m Model) Init() tea.Cmd {
	var initCmd tea.Cmd
	switch m.state {
	case StateMenu:
		initCmd = m.menu.Init()
	case StateDashboard:
		initCmd = m.dashboard.Init()
	case StateSettings:
		initCmd = m.settings.Init()
	}

	// Auto-start server on app launch if configured and user has a token
	cfg := config.Get()
	if cfg.Server.AutoStart && m.commands.HasToken != nil && m.commands.HasToken() && m.commands.StartServer != nil {
		m.autoStartAttempted = true
		m.serverStarting = true
		serverFunc := m.commands.StartServer(m.serverPort, m.logger)
		return tea.Batch(initCmd, func() tea.Msg { return serverFunc() })
	}

	return initCmd
}

// Update handles messages
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		// Auto-start server on first WindowSizeMsg if initial state is dashboard
		// (for start_view=dashboard config; auto_start is handled in Init)
		if !m.autoStartAttempted && m.state == StateDashboard {
			m.autoStartAttempted = true
			m.serverStarting = true
			if m.commands.HasToken != nil && m.commands.HasToken() && m.commands.StartServer != nil {
				serverFunc := m.commands.StartServer(m.serverPort, m.logger)
				cmds = append(cmds, func() tea.Msg { return serverFunc() })
			}
		}

		switch m.state {
		case StateMenu:
			m.menu.SetSize(msg.Width, msg.Height)
		case StateLogin:
			m.login.SetSize(msg.Width, msg.Height)
		case StateLoginProgress:
			m.loginProgress.SetSize(msg.Width, msg.Height)
		case StateDashboard:
			var cmd tea.Cmd
			m.dashboard, cmd = m.dashboard.Update(msg)
			cmds = append(cmds, cmd)
		case StateSettings:
			m.settings.SetSize(msg.Width, msg.Height)
		}

	case NavigateToMenuMsg:
		m.state = StateMenu
		m.menu = menu.New(m.width, m.height)
		m.menu.SetServerRunning(m.serverRunning, m.serverPort)
		return m, m.menu.Init()

	case NavigateToDashboardMsg:
		m.state = StateDashboard
		m.dashboard = m.createDashboard(msg.Port, msg.TokenExpiry)
		m.dashboard.SetServerRunning(m.serverRunning)
		var sizeCmd tea.Cmd
		m.dashboard, sizeCmd = m.dashboard.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		return m, tea.Batch(m.dashboard.Init(), sizeCmd)

	case dashboard.ServerStartedMsg:
		m.serverRunning = true
		m.serverStarting = false
		m.serverPort = msg.Port
		m.dashboard.SetServerRunning(true)
		m.menu.SetServerRunning(true, msg.Port)
		if m.state == StateDashboard {
			var cmd tea.Cmd
			dm, cmd := m.dashboard.Update(msg)
			m.dashboard = dm
			cmds = append(cmds, cmd)
		}
		// Auto-launch Claude Code if configured
		cfg := config.Get()
		if cfg.Server.AutoLaunchClaude {
			cmds = append(cmds, launchClaudeRemote(m.serverPort))
		}

	case dashboard.ServerStoppedMsg:
		m.serverRunning = false
		m.serverStarting = false
		m.serverPort = ""
		m.dashboard.SetServerRunning(false)
		m.menu.SetServerRunning(false, "")

		if msg.Err != nil {
			m.menu.SetStatus(fmt.Sprintf("Server stopped: %v", msg.Err), true)
		} else {
			m.menu.SetStatus("Server stopped", false)
		}

		if m.state == StateDashboard {
			var cmd tea.Cmd
			dm, cmd := m.dashboard.Update(msg)
			m.dashboard = dm
			cmds = append(cmds, cmd)
		}

	case dashboard.GoToMenuMsg:
		m.state = StateMenu
		m.menu = menu.New(m.width, m.height)
		m.menu.SetServerRunning(m.serverRunning, m.serverPort)
		return m, m.menu.Init()

	case dashboard.BackToMenuMsg:
		m.state = StateMenu
		m.menu = menu.New(m.width, m.height)
		// Mark it as not running immediately to disable "running" actions during shutdown
		m.menu.SetServerRunning(false, "")
		m.menu.SetStatus("Server stopping...", false)

		var cmd tea.Cmd
		if m.commands.StopServer != nil {
			cmd = func() tea.Msg { return m.commands.StopServer() }
		}

		return m, tea.Batch(
			m.menu.Init(),
			cmd,
			tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearStatusMsg{}
			}),
		)

	case dashboard.OpenSettingsMsg:
		m.state = StateSettings
		m.settings = settings.New(m.width, m.height, true) // fromDashboard=true
		return m, m.settings.Init()

	case settings.BackToMenuMsg:
		m.state = StateMenu
		m.menu = menu.New(m.width, m.height)
		m.menu.SetServerRunning(m.serverRunning, m.serverPort)
		return m, m.menu.Init()

	case settings.BackToDashboardMsg:
		m.state = StateDashboard
		m.dashboard.SetServerRunning(m.serverRunning)
		var sizeCmd tea.Cmd
		m.dashboard, sizeCmd = m.dashboard.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		return m, sizeCmd

	case login.BackToMenuMsg:
		m.state = StateMenu
		m.menu = menu.New(m.width, m.height)
		m.menu.SetServerRunning(m.serverRunning, m.serverPort)
		return m, m.menu.Init()

	case login.LoginStartMsg:
		var args []string
		switch msg.Method {
		case login.MethodGithub:
			args = []string{"login", "github"}
		case login.MethodGoogle:
			args = []string{"login", "google"}
		case login.MethodBuilderID:
			args = []string{"login", "builderid"}
		case login.MethodIdC:
			args = []string{"login", "idc", msg.StartUrl, msg.Region}
		}

		// Switch to login progress view
		m.state = StateLoginProgress
		m.loginProgress = loginprogress.New(m.width, m.height)

		// Run login in background and capture output
		return m, tea.Batch(
			m.loginProgress.Init(),
			runLoginCommand(args, m.program),
		)

	case logger.LogEntryMsg:
		var cmd tea.Cmd
		dm, cmd := m.dashboard.Update(msg)
		m.dashboard = dm
		if cmd != nil {
			cmds = append(cmds, cmd)
		}

	case loginprogress.LoginOutputMsg:
		if m.state == StateLoginProgress {
			var cmd tea.Cmd
			m.loginProgress, cmd = m.loginProgress.Update(msg)
			cmds = append(cmds, cmd)
		}

	case loginprogress.LoginCompleteMsg:
		if m.state == StateLoginProgress {
			var cmd tea.Cmd
			m.loginProgress, cmd = m.loginProgress.Update(msg)
			cmds = append(cmds, cmd)
		}

	case loginprogress.BackToMenuMsg:
		m.state = StateMenu
		m.menu = menu.New(m.width, m.height)
		m.menu.SetServerRunning(m.serverRunning, m.serverPort)
		m.menu.RefreshTokenInfo()
		return m, m.menu.Init()

	case StatusMsg:
		if m.state == StateLogin {
			m.state = StateMenu
			m.menu = menu.New(m.width, m.height)
			m.menu.SetServerRunning(m.serverRunning, m.serverPort)
		}
		m.menu.SetStatus(msg.Message, msg.IsError)
		cmds = append(cmds, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearStatusMsg{}
		}))

	case clearStatusMsg:
		m.menu.ClearStatus()

	case cmd.RefreshResultMsg:
		if msg.Success {
			m.menu.SetStatus(fmt.Sprintf("Token refreshed! Expires: %s", msg.ExpiresAt.Format("15:04:05")), false)
			m.menu.RefreshTokenInfo()
		} else {
			m.menu.SetStatus(fmt.Sprintf("Refresh failed: %v", msg.Err), true)
		}
		cmds = append(cmds, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearStatusMsg{}
		}))

	case cmd.TokenInfoMsg:
		if msg.Err != nil {
			m.menu.SetStatus(fmt.Sprintf("Failed to read token: %v", msg.Err), true)
		} else {
			expiry := "N/A"
			if msg.Token.ExpiresAt != "" {
				if t, err := time.Parse(time.RFC3339, msg.Token.ExpiresAt); err == nil {
					expiry = t.Format("15:04:05")
				}
			}
			m.menu.SetStatus(fmt.Sprintf("Auth: %s | Provider: %s | Expires: %s", msg.Token.AuthMethod, msg.Token.Provider, expiry), false)
		}
		cmds = append(cmds, tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
			return clearStatusMsg{}
		}))

	case cmd.StatusMsg:
		m.menu.SetStatus(msg.Message, msg.IsError)
		m.menu.RefreshTokenInfo()
		cmds = append(cmds, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return clearStatusMsg{}
		}))

	case menu.MenuActionMsg:
		return m.handleMenuAction(msg.Action)

	case menu.CreditsUpdateMsg:
		if m.state == StateMenu {
			var cmd tea.Cmd
			m.menu, cmd = m.menu.Update(msg)
			cmds = append(cmds, cmd)
		}

	case dashboard.CreditsUpdateMsg:
		var cmd tea.Cmd
		dm, cmd := m.dashboard.Update(msg)
		m.dashboard = dm
		if cmd != nil {
			cmds = append(cmds, cmd)
		}

	case dashboard.TokenRefreshedMsg:
		var cmd tea.Cmd
		dm, cmd := m.dashboard.Update(msg)
		m.dashboard = dm
		if cmd != nil {
			cmds = append(cmds, cmd)
		}

	case dashboard.TickMsg:
		var cmd tea.Cmd
		dm, cmd := m.dashboard.Update(msg)
		m.dashboard = dm
		if cmd != nil {
			cmds = append(cmds, cmd)
		}

	case menu.StatusTickMsg:
		if m.state == StateMenu {
			var cmd tea.Cmd
			m.menu, cmd = m.menu.Update(msg)
			cmds = append(cmds, cmd)
		}

	default:
		switch m.state {
		case StateMenu:
			var cmd tea.Cmd
			m.menu, cmd = m.menu.Update(msg)
			cmds = append(cmds, cmd)
		case StateLogin:
			var cmd tea.Cmd
			m.login, cmd = m.login.Update(msg)
			cmds = append(cmds, cmd)
		case StateLoginProgress:
			var cmd tea.Cmd
			m.loginProgress, cmd = m.loginProgress.Update(msg)
			cmds = append(cmds, cmd)
		case StateDashboard:
			var cmd tea.Cmd
			dm, cmd := m.dashboard.Update(msg)
			m.dashboard = dm
			cmds = append(cmds, cmd)
		case StateSettings:
			var cmd tea.Cmd
			m.settings, cmd = m.settings.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	return m, tea.Batch(cmds...)
}

type clearStatusMsg struct{}

func (m Model) handleMenuAction(action menu.MenuAction) (tea.Model, tea.Cmd) {
	switch action {
	case menu.ActionLogin:
		m.state = StateLogin
		m.login = login.New(m.width, m.height)
		return m, m.login.Init()

	case menu.ActionServer:
		if m.serverRunning || m.serverStarting {
			m.state = StateDashboard
			m.dashboard.SetServerRunning(m.serverRunning)
			var sizeCmd tea.Cmd
			m.dashboard, sizeCmd = m.dashboard.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
			return m, sizeCmd
		}

		if m.commands.HasToken != nil && !m.commands.HasToken() {
			m.menu.SetStatus("No token found. Please login first.", true)
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearStatusMsg{}
			})
		}

		if m.commands.StartServer != nil {
			cfg := config.Get()
			port := cfg.Server.Port
			tokenExpiry := time.Time{}
			if m.commands.GetTokenExpiry != nil {
				tokenExpiry = m.commands.GetTokenExpiry()
			}

			m.state = StateDashboard
			m.serverPort = port
			m.dashboard = m.createDashboard(port, tokenExpiry)
			m.dashboard.SetServerRunning(false)
			var sizeCmd tea.Cmd
			m.dashboard, sizeCmd = m.dashboard.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})

			serverFunc := m.commands.StartServer(port, m.logger)

			return m, tea.Batch(
				m.dashboard.Init(),
				sizeCmd,
				func() tea.Msg { return serverFunc() },
			)
		}

	case menu.ActionDashboard:
		if m.serverRunning {
			m.state = StateDashboard
			m.dashboard.SetServerRunning(true)
			// Refresh token expiry from file before showing dashboard
			if m.commands.GetTokenExpiry != nil {
				newExpiry := m.commands.GetTokenExpiry()
				m.dashboard.SetTokenExpiry(newExpiry)
			}
			var sizeCmd tea.Cmd
			m.dashboard, sizeCmd = m.dashboard.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
			// Also trigger a credits refresh
			return m, tea.Batch(dashboard.FetchCreditsCmd(), sizeCmd)
		}

	case menu.ActionLaunchClaude:
		if m.serverRunning {
			return m, launchClaudeRemote(m.serverPort)
		}

	case menu.ActionRefreshToken:
		if m.commands.RefreshToken != nil {
			return m, func() tea.Msg { return m.commands.RefreshToken() }
		}

	case menu.ActionConfigureClaude:
		// Contextual action: Configure if not configured, Unconfigure if configured
		// Check current state by reading config
		if m.menu.IsClaude2KiroConfigured() {
			if m.commands.Unconfigure != nil {
				return m, func() tea.Msg { return m.commands.Unconfigure() }
			}
		} else {
			if m.commands.ConfigureClaude != nil {
				return m, func() tea.Msg { return m.commands.ConfigureClaude() }
			}
		}

	case menu.ActionLogout:
		if m.commands.Logout != nil {
			return m, func() tea.Msg { return m.commands.Logout() }
		}

	case menu.ActionSettings:
		m.state = StateSettings
		m.settings = settings.New(m.width, m.height, false) // fromDashboard=false
		return m, m.settings.Init()

	case menu.ActionQuit:
		return m, tea.Quit
	}

	return m, nil
}

func (m Model) View() string {
	switch m.state {
	case StateMenu:
		return m.menu.View()
	case StateLogin:
		return m.login.View()
	case StateLoginProgress:
		return m.loginProgress.View()
	case StateDashboard:
		return m.dashboard.View()
	case StateSettings:
		return m.settings.View()
	}
	return ""
}

func (m Model) GetLogger() *logger.Logger {
	return m.logger
}

// runLoginCommand runs the login command in background and streams output to TUI
func runLoginCommand(args []string, program *tea.Program) tea.Cmd {
	return func() tea.Msg {
		executable, err := os.Executable()
		if err != nil {
			executable = os.Args[0]
		}

		cmd := exec.Command(executable, args...)

		// Capture both stdout and stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return loginprogress.LoginCompleteMsg{Success: false, Error: err.Error()}
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return loginprogress.LoginCompleteMsg{Success: false, Error: err.Error()}
		}

		if err := cmd.Start(); err != nil {
			return loginprogress.LoginCompleteMsg{Success: false, Error: err.Error()}
		}

		// Read stdout in goroutine
		go func() {
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				if program != nil {
					program.Send(loginprogress.LoginOutputMsg{Line: scanner.Text()})
				}
			}
		}()

		// Read stderr in goroutine
		go func() {
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				if program != nil {
					program.Send(loginprogress.LoginOutputMsg{Line: scanner.Text()})
				}
			}
		}()

		// Wait for command to complete
		err = cmd.Wait()
		if err != nil {
			return loginprogress.LoginCompleteMsg{Success: false, Error: err.Error()}
		}

		return loginprogress.LoginCompleteMsg{Success: true}
	}
}

// parseDashboardRetentionDays converts the DashboardRetention setting to number of days to load
// Returns 1 for "24h", 2 for "48h" (default), 3 for "72h", 7 for "7d", 30 for "30d", etc.
// "unlimited" returns 365 (1 year max)
func parseDashboardRetentionDays(retention string) int {
	switch retention {
	case "24h":
		return 1
	case "48h":
		return 2
	case "72h":
		return 3
	case "7d":
		return 7
	case "30d":
		return 30
	case "90d":
		return 90
	case "unlimited":
		return 365 // Max 1 year
	default:
		return 2 // Default to 2 days
	}
}

// LaunchClaudeResultMsg reports the result of launching Claude Code
type LaunchClaudeResultMsg struct {
	Err error
}

// launchClaudeRemote spawns `claude2kiro remote` in a new terminal window.
func launchClaudeRemote(port string) tea.Cmd {
	return func() tea.Msg {
		// Find our own executable
		self, err := os.Executable()
		if err != nil {
			return LaunchClaudeResultMsg{Err: fmt.Errorf("cannot find executable: %v", err)}
		}

		// Spawn in a new terminal window (platform-specific)
		var proc *exec.Cmd
		switch {
		case fileExists("C:\\Windows\\System32\\cmd.exe"):
			// Windows: open new cmd window
			proc = exec.Command("cmd", "/c", "start", "", self, "remote")
		case envExists("DISPLAY") || envExists("WAYLAND_DISPLAY"):
			// Linux with display: try common terminal emulators
			for _, term := range []string{"x-terminal-emulator", "gnome-terminal", "konsole", "xterm"} {
				if _, err := exec.LookPath(term); err == nil {
					if term == "gnome-terminal" || term == "konsole" {
						proc = exec.Command(term, "--", self, "remote")
					} else {
						proc = exec.Command(term, "-e", self, "remote")
					}
					break
				}
			}
		default:
			// macOS or fallback
			proc = exec.Command("open", "-a", "Terminal", self, "--args", "remote")
		}

		if proc == nil {
			return LaunchClaudeResultMsg{Err: fmt.Errorf("no terminal emulator found")}
		}

		if err := proc.Start(); err != nil {
			return LaunchClaudeResultMsg{Err: fmt.Errorf("failed to launch: %v", err)}
		}

		return LaunchClaudeResultMsg{}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func envExists(key string) bool {
	_, ok := os.LookupEnv(key)
	return ok
}
