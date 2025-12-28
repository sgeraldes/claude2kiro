package tui

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bestk/kiro2cc/cmd"
	"github.com/bestk/kiro2cc/internal/tui/dashboard"
	"github.com/bestk/kiro2cc/internal/tui/logger"
	"github.com/bestk/kiro2cc/internal/tui/login"
	"github.com/bestk/kiro2cc/internal/tui/menu"
)

// Commands holds the command functions from main
type Commands struct {
	Login           func() tea.Msg
	StartServer     func(port string, lg *logger.Logger, program *tea.Program) func() tea.Msg
	RefreshToken    func() tea.Msg
	ViewToken       func() tea.Msg
	ExportEnv       func() tea.Msg
	ConfigureClaude func() tea.Msg
	ViewCredits     func() tea.Msg
	Logout          func() tea.Msg
	GetTokenExpiry  func() time.Time
	HasToken        func() bool
}

// Model is the root TUI model
type Model struct {
	state              AppState
	menu               menu.Model
	login              login.Model
	dashboard          dashboard.Model
	width              int
	height             int
	commands           Commands
	logger             *logger.Logger
	program            *tea.Program
	serverRunning      bool
	serverPort         string
	autoStartAttempted bool
}

// NewRootModel creates a new root model
func NewRootModel(cmds Commands) Model {
	lg := logger.NewLogger(50)

	port := "8080"
	tokenExpiry := time.Time{}
	if cmds.GetTokenExpiry != nil {
		tokenExpiry = cmds.GetTokenExpiry()
	}

	return Model{
		state:      StateDashboard,
		menu:       menu.New(80, 24),
		dashboard:  dashboard.New(port, tokenExpiry, lg),
		commands:   cmds,
		logger:     lg,
		serverPort: port,
	}
}

// SetProgram sets the tea.Program reference
func (m *Model) SetProgram(p *tea.Program) {
	m.program = p
	m.logger.SetProgram(p)
}

// Init initializes the model
func (m Model) Init() tea.Cmd {
	return m.dashboard.Init()
}

// Update handles messages
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		// Auto-start server on first WindowSizeMsg
		if !m.autoStartAttempted && m.state == StateDashboard {
			m.autoStartAttempted = true
			if m.commands.HasToken != nil && m.commands.HasToken() && m.commands.StartServer != nil {
				serverFunc := m.commands.StartServer(m.serverPort, m.logger, m.program)
				cmds = append(cmds, func() tea.Msg { return serverFunc() })
			}
		}

		switch m.state {
		case StateMenu:
			m.menu.SetSize(msg.Width, msg.Height)
		case StateLogin:
			m.login.SetSize(msg.Width, msg.Height)
		case StateDashboard:
			m.dashboard, _ = m.dashboard.Update(msg)
		}

	case NavigateToMenuMsg:
		m.state = StateMenu
		m.menu = menu.New(m.width, m.height)
		return m, m.menu.Init()

	case NavigateToDashboardMsg:
		m.state = StateDashboard
		m.dashboard = dashboard.New(msg.Port, msg.TokenExpiry, m.logger)
		m.dashboard, _ = m.dashboard.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		return m, m.dashboard.Init()

	case dashboard.ServerStartedMsg:
		m.serverRunning = true
		m.serverPort = msg.Port
		if m.state == StateDashboard {
			var cmd tea.Cmd
			dm, cmd := m.dashboard.Update(msg)
			m.dashboard = dm
			cmds = append(cmds, cmd)
		}

	case dashboard.ServerStoppedMsg:
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
		m.serverRunning = false
		m.serverPort = ""
		m.state = StateMenu
		m.menu = menu.New(m.width, m.height)
		m.menu.SetStatus("Server stopped", false)
		return m, tea.Batch(
			m.menu.Init(),
			tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearStatusMsg{}
			}),
		)

	case login.BackToMenuMsg:
		m.state = StateMenu
		m.menu = menu.New(m.width, m.height)
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

		executable, err := os.Executable()
		if err != nil {
			executable = os.Args[0]
		}

		loginCmd := exec.Command(executable, args...)
		// Note: Don't set Stdin/Stdout/Stderr - tea.ExecProcess handles this automatically

		return m, tea.ExecProcess(loginCmd, func(err error) tea.Msg {
			if err != nil {
				return StatusMsg{Message: fmt.Sprintf("Login failed: %v", err), IsError: true}
			}
			return StatusMsg{Message: "Login successful!", IsError: false}
		})

	case logger.LogEntryMsg:
		if m.state == StateDashboard {
			var cmd tea.Cmd
			dm, cmd := m.dashboard.Update(msg)
			m.dashboard = dm
			cmds = append(cmds, cmd)
		}

	case StatusMsg:
		if m.state == StateLogin {
			m.state = StateMenu
			m.menu = menu.New(m.width, m.height)
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
		if m.state == StateDashboard {
			var cmd tea.Cmd
			dm, cmd := m.dashboard.Update(msg)
			m.dashboard = dm
			cmds = append(cmds, cmd)
		}

	case dashboard.TickMsg:
		if m.serverRunning {
			var cmd tea.Cmd
			dm, cmd := m.dashboard.Update(msg)
			m.dashboard = dm
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
		case StateDashboard:
			var cmd tea.Cmd
			dm, cmd := m.dashboard.Update(msg)
			m.dashboard = dm
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
		if m.serverRunning {
			m.state = StateDashboard
			m.dashboard, _ = m.dashboard.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
			return m, nil
		}

		if m.commands.HasToken != nil && !m.commands.HasToken() {
			m.menu.SetStatus("No token found. Please login first.", true)
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
				return clearStatusMsg{}
			})
		}

		if m.commands.StartServer != nil {
			port := "8080"
			tokenExpiry := time.Time{}
			if m.commands.GetTokenExpiry != nil {
				tokenExpiry = m.commands.GetTokenExpiry()
			}

			m.state = StateDashboard
			m.serverPort = port
			m.dashboard = dashboard.New(port, tokenExpiry, m.logger)
			m.dashboard, _ = m.dashboard.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})

			serverFunc := m.commands.StartServer(port, m.logger, m.program)

			return m, tea.Batch(
				m.dashboard.Init(),
				func() tea.Msg { return serverFunc() },
			)
		}

	case menu.ActionDashboard:
		if m.serverRunning {
			m.state = StateDashboard
			m.dashboard, _ = m.dashboard.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
			return m, nil
		}

	case menu.ActionRefreshToken:
		if m.commands.RefreshToken != nil {
			return m, func() tea.Msg { return m.commands.RefreshToken() }
		}

	case menu.ActionViewToken:
		if m.commands.ViewToken != nil {
			return m, func() tea.Msg { return m.commands.ViewToken() }
		}

	case menu.ActionExportEnv:
		if m.commands.ExportEnv != nil {
			return m, func() tea.Msg { return m.commands.ExportEnv() }
		}

	case menu.ActionConfigureClaude:
		if m.commands.ConfigureClaude != nil {
			return m, func() tea.Msg { return m.commands.ConfigureClaude() }
		}

	case menu.ActionViewCredits:
		if m.commands.ViewCredits != nil {
			return m, func() tea.Msg { return m.commands.ViewCredits() }
		}

	case menu.ActionLogout:
		if m.commands.Logout != nil {
			return m, func() tea.Msg { return m.commands.Logout() }
		}

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
	case StateDashboard:
		return m.dashboard.View()
	}
	return ""
}

func (m Model) GetLogger() *logger.Logger {
	return m.logger
}
