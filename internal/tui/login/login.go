package login

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// savedIdCCredentials holds saved SSO credentials from token file
type savedIdCCredentials struct {
	StartUrl string `json:"startUrl"`
	Region   string `json:"region"`
}

// getSavedIdCCredentials reads previously used IdC credentials from token file
func getSavedIdCCredentials() (string, string) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	tokenPath := filepath.Join(homeDir, ".aws", "sso", "cache", "kiro-auth-token.json")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return "", ""
	}
	var creds savedIdCCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", ""
	}
	return creds.StartUrl, creds.Region
}

// LoginMethod identifies the login method
type LoginMethod int

const (
	MethodGithub LoginMethod = iota
	MethodGoogle
	MethodBuilderID
	MethodIdC
)

// ViewState represents the current view within login
type ViewState int

const (
	ViewMethodSelect ViewState = iota
	ViewIdCForm
)

// LoginStartMsg signals to start the login process
type LoginStartMsg struct {
	Method   LoginMethod
	StartUrl string
	Region   string
}

// BackToMenuMsg signals return to main menu
type BackToMenuMsg struct{}

// MenuItem represents a menu entry
type MenuItem struct {
	method      LoginMethod
	title       string
	description string
}

func (i MenuItem) Title() string       { return i.title }
func (i MenuItem) Description() string { return i.description }
func (i MenuItem) FilterValue() string { return i.title }

// ItemDelegate handles rendering of menu items
type ItemDelegate struct {
	styles ItemStyles
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
		title = d.styles.Selected.Render("* " + i.Title())
		desc = d.styles.Desc.Render(i.Description())
	} else {
		title = d.styles.Normal.Render("  " + i.Title())
		desc = d.styles.DescDim.Render(i.Description())
	}

	fmt.Fprintf(w, "%s\n%s", title, desc)
}

// Model represents the login component
type Model struct {
	viewState    ViewState
	list         list.Model
	urlInput     textinput.Model
	regionInput  textinput.Model
	focusedInput int
	width        int
	height       int
}

// New creates a new login model
func New(width, height int) Model {
	items := []list.Item{
		MenuItem{MethodGithub, "GitHub", "Login with your GitHub account"},
		MenuItem{MethodGoogle, "Google", "Login with your Google account"},
		MenuItem{MethodBuilderID, "AWS Builder ID", "Free AWS developer account"},
		MenuItem{MethodIdC, "Enterprise Identity Center", "Organization SSO (requires start URL)"},
	}

	delegate := NewItemDelegate()
	listWidth := width - 8
	listHeight := height - 12

	l := list.New(items, delegate, listWidth, listHeight)
	l.Title = ""
	l.SetShowStatusBar(false)
	l.SetShowFilter(false)
	l.SetShowHelp(true)
	l.SetShowPagination(false)
	l.Styles.HelpStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#626262")).
		MarginTop(1)

	urlInput := textinput.New()
	urlInput.Placeholder = "e.g., myorg or https://myorg.awsapps.com/start"
	urlInput.CharLimit = 256
	urlInput.Width = 50

	regionInput := textinput.New()
	regionInput.Placeholder = "e.g., us-east-1"
	regionInput.CharLimit = 20
	regionInput.Width = 20

	// Pre-populate from saved credentials if available
	savedUrl, savedRegion := getSavedIdCCredentials()
	if savedUrl != "" {
		urlInput.SetValue(savedUrl)
	}
	if savedRegion != "" {
		regionInput.SetValue(savedRegion)
	}

	return Model{
		viewState:    ViewMethodSelect,
		list:         l,
		urlInput:     urlInput,
		regionInput:  regionInput,
		focusedInput: 0,
		width:        width,
		height:       height,
	}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch m.viewState {
		case ViewMethodSelect:
			switch msg.String() {
			case "enter":
				if item, ok := m.list.SelectedItem().(MenuItem); ok {
					if item.method == MethodIdC {
						m.viewState = ViewIdCForm
						m.urlInput.Focus()
						return m, textinput.Blink
					}
					return m, func() tea.Msg {
						return LoginStartMsg{Method: item.method}
					}
				}
			case "esc", "q":
				return m, func() tea.Msg { return BackToMenuMsg{} }
			}

			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			cmds = append(cmds, cmd)

		case ViewIdCForm:
			switch msg.String() {
			case "tab", "down":
				if m.focusedInput == 0 {
					m.focusedInput = 1
					m.urlInput.Blur()
					m.regionInput.Focus()
				} else {
					m.focusedInput = 0
					m.regionInput.Blur()
					m.urlInput.Focus()
				}
				return m, textinput.Blink

			case "shift+tab", "up":
				if m.focusedInput == 1 {
					m.focusedInput = 0
					m.regionInput.Blur()
					m.urlInput.Focus()
				} else {
					m.focusedInput = 1
					m.urlInput.Blur()
					m.regionInput.Focus()
				}
				return m, textinput.Blink

			case "enter":
				url := strings.TrimSpace(m.urlInput.Value())
				region := strings.TrimSpace(m.regionInput.Value())
				if region == "" {
					region = "us-east-1"
				}
				if url != "" {
					return m, func() tea.Msg {
						return LoginStartMsg{
							Method:   MethodIdC,
							StartUrl: url,
							Region:   region,
						}
					}
				}

			case "esc":
				m.viewState = ViewMethodSelect
				m.urlInput.Reset()
				m.regionInput.Reset()
				return m, nil
			}

			var cmd tea.Cmd
			if m.focusedInput == 0 {
				m.urlInput, cmd = m.urlInput.Update(msg)
			} else {
				m.regionInput, cmd = m.regionInput.Update(msg)
			}
			cmds = append(cmds, cmd)
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(msg.Width-8, msg.Height-12)
	}

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

	subtitleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#A0A0A0"))

	switch m.viewState {
	case ViewMethodSelect:
		header := lipgloss.JoinVertical(lipgloss.Left,
			titleStyle.Render("Login"),
			subtitleStyle.Render("Choose your authentication method"),
			"",
		)
		content := lipgloss.JoinVertical(lipgloss.Left, header, m.list.View())
		return boxStyle.Render(content)

	case ViewIdCForm:
		header := lipgloss.JoinVertical(lipgloss.Left,
			titleStyle.Render("Enterprise Identity Center"),
			subtitleStyle.Render("Enter your SSO details"),
			"",
		)

		focusedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#7D56F4"))
		unfocusedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))

		var urlLabel, regionLabel string
		if m.focusedInput == 0 {
			urlLabel = focusedStyle.Render("> Start URL:")
		} else {
			urlLabel = unfocusedStyle.Render("  Start URL:")
		}

		if m.focusedInput == 1 {
			regionLabel = focusedStyle.Render("> Region:")
		} else {
			regionLabel = unfocusedStyle.Render("  Region:")
		}

		helpStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262")).MarginTop(2)
		help := helpStyle.Render("tab: switch field | enter: login | esc: back")

		form := lipgloss.JoinVertical(lipgloss.Left,
			header, "", urlLabel, m.urlInput.View(), "",
			regionLabel, m.regionInput.View(), "", help,
		)

		return boxStyle.Render(form)
	}

	return ""
}

func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
	m.list.SetSize(width-8, height-12)
}

type KeyMap struct {
	Enter key.Binding
	Back  key.Binding
	Quit  key.Binding
}

func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Enter, k.Back, k.Quit}
}

func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Enter, k.Back, k.Quit}}
}
