package loginprogress

import (
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// LoginOutputMsg carries a line of login output
type LoginOutputMsg struct {
	Line string
}

// LoginCompleteMsg indicates login has finished
type LoginCompleteMsg struct {
	Success bool
	Error   string
}

// Model represents the login progress view
type Model struct {
	spinner  spinner.Model
	viewport viewport.Model
	output   []string
	width    int
	height   int
	done     bool
	success  bool
	error    string
}

// New creates a new login progress model
func New(width, height int) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#7D56F4"))

	vp := viewport.New(width-8, height-12)
	vp.Style = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#626262")).
		Padding(0, 1)

	return Model{
		spinner:  s,
		viewport: vp,
		output:   []string{},
		width:    width,
		height:   height,
	}
}

func (m Model) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case LoginOutputMsg:
		m.output = append(m.output, msg.Line)
		m.viewport.SetContent(strings.Join(m.output, "\n"))
		m.viewport.GotoBottom()
		return m, nil

	case LoginCompleteMsg:
		m.done = true
		m.success = msg.Success
		m.error = msg.Error
		if msg.Success {
			m.output = append(m.output, "", lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575")).Render("Login successful!"))
		} else {
			m.output = append(m.output, "", lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F56")).Render("Login failed: "+msg.Error))
		}
		m.viewport.SetContent(strings.Join(m.output, "\n"))
		m.viewport.GotoBottom()
		return m, nil

	case spinner.TickMsg:
		if !m.done {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = msg.Width - 8
		m.viewport.Height = msg.Height - 12

	case tea.KeyMsg:
		switch msg.String() {
		case "enter", "esc", "q":
			if m.done {
				// Return to menu
				return m, func() tea.Msg { return BackToMenuMsg{} }
			}
		}
	}

	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7D56F4")).
		Padding(1, 2).
		Width(m.width - 4)

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#7D56F4"))

	var status string
	if m.done {
		if m.success {
			status = lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575")).Render("Complete")
		} else {
			status = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F56")).Render("Failed")
		}
	} else {
		status = m.spinner.View() + " Authenticating..."
	}

	header := lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("Login Progress"),
		status,
		"",
	)

	// Output panel
	outputStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#626262")).
		Padding(0, 1).
		Width(m.width - 10).
		Height(m.height - 16)

	outputContent := strings.Join(m.output, "\n")
	if outputContent == "" {
		outputContent = lipgloss.NewStyle().Foreground(lipgloss.Color("#626262")).Render("Waiting for login to start...")
	}
	outputPanel := outputStyle.Render(outputContent)

	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#626262")).
		MarginTop(1)

	var help string
	if m.done {
		help = helpStyle.Render("Press Enter or Esc to continue")
	} else {
		help = helpStyle.Render("Please complete authentication in your browser...")
	}

	content := lipgloss.JoinVertical(lipgloss.Left, header, outputPanel, help)
	return boxStyle.Render(content)
}

func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
	m.viewport.Width = width - 8
	m.viewport.Height = height - 12
}

// IsDone returns true if login is complete
func (m Model) IsDone() bool {
	return m.done
}

// IsSuccess returns true if login was successful
func (m Model) IsSuccess() bool {
	return m.success
}

// BackToMenuMsg signals return to main menu
type BackToMenuMsg struct{}
