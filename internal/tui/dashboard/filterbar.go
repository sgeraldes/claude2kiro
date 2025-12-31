package dashboard

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// FilterState represents the current filter settings (passed to LogViewer)
type FilterState struct {
	ShowReq    bool
	ShowRes    bool
	ShowInf    bool
	ShowErr    bool
	SearchText string
	AfterDate  time.Time
}

// FilterChangedMsg signals that filters have changed
type FilterChangedMsg struct {
	State FilterState
}

// FocusLogListMsg signals that the user wants to move down from the filter bar to the log list
type FocusLogListMsg struct{}

// FilterBarModel represents the filter bar component
type FilterBarModel struct {
	// Type filters (checkboxes)
	showReq bool
	showRes bool
	showInf bool
	showErr bool

	// Search
	searchInput   textinput.Model
	searchFocused bool

	// After date (persisted)
	afterDate time.Time

	// UI state
	width           int
	focused         bool
	selectedElement int // 0=req, 1=res, 2=inf, 3=err, 4=search (for left/right navigation)
}

// NewFilterBarModel creates a new filter bar
func NewFilterBarModel() FilterBarModel {
	ti := textinput.New()
	ti.Placeholder = "search..."
	ti.CharLimit = 50
	ti.Width = 20

	// Load persisted filters from config
	cfg := config.Get()
	afterDate := cfg.Filter.ClearAfter

	// Load type filters with defaults
	showReq := true
	showRes := true
	showInf := false
	showErr := false
	if cfg.Filter.ShowReq != nil {
		showReq = *cfg.Filter.ShowReq
	}
	if cfg.Filter.ShowRes != nil {
		showRes = *cfg.Filter.ShowRes
	}
	if cfg.Filter.ShowInf != nil {
		showInf = *cfg.Filter.ShowInf
	}
	if cfg.Filter.ShowErr != nil {
		showErr = *cfg.Filter.ShowErr
	}

	return FilterBarModel{
		showReq:         showReq,
		showRes:         showRes,
		showInf:         showInf,
		showErr:         showErr,
		searchInput:     ti,
		afterDate:       afterDate,
		selectedElement: 4, // Default to search field
	}
}

// Init initializes the filter bar
func (m FilterBarModel) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (m FilterBarModel) Update(msg tea.Msg) (FilterBarModel, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		// If search is focused (typing mode), handle text input
		if m.searchFocused {
			switch msg.String() {
			case "esc":
				// Exit search, stay on filter bar
				m.searchFocused = false
				m.searchInput.Blur()
				return m, m.emitFilterChanged()
			case "enter", "down":
				// Exit search and move to log list
				m.searchFocused = false
				m.searchInput.Blur()
				return m, tea.Batch(m.emitFilterChanged(), func() tea.Msg { return FocusLogListMsg{} })
			case "left":
				// If cursor is at start, navigate left to filters
				if m.searchInput.Position() == 0 {
					m.searchFocused = false
					m.searchInput.Blur()
					m.selectedElement = 3 // Go to err checkbox
					return m, m.emitFilterChanged()
				}
				// Otherwise let textinput handle cursor movement
				m.searchInput, cmd = m.searchInput.Update(msg)
				return m, cmd
			default:
				m.searchInput, cmd = m.searchInput.Update(msg)
				return m, tea.Batch(cmd, m.emitFilterChanged())
			}
		}

		// Handle filter bar keys when focused but not in search typing mode
		if m.focused {
			switch msg.String() {
			case "left":
				// Navigate left: search -> err -> inf -> res -> req -> search (wrap)
				m.selectedElement--
				if m.selectedElement < 0 {
					m.selectedElement = 4 // wrap to search
				}
				return m, nil

			case "right":
				// Navigate right: req -> res -> inf -> err -> search -> req (wrap)
				m.selectedElement++
				if m.selectedElement > 4 {
					m.selectedElement = 0 // wrap to req
				}
				return m, nil

			case "down":
				// Move focus down to log list
				return m, func() tea.Msg { return FocusLogListMsg{} }

			case "enter", " ":
				// Toggle current checkbox or focus search
				switch m.selectedElement {
				case 0:
					m.showReq = !m.showReq
					m.saveTypeFilters()
					return m, m.emitFilterChanged()
				case 1:
					m.showRes = !m.showRes
					m.saveTypeFilters()
					return m, m.emitFilterChanged()
				case 2:
					m.showInf = !m.showInf
					m.saveTypeFilters()
					return m, m.emitFilterChanged()
				case 3:
					m.showErr = !m.showErr
					m.saveTypeFilters()
					return m, m.emitFilterChanged()
				case 4:
					// Focus search for typing
					m.searchFocused = true
					m.searchInput.Focus()
					return m, textinput.Blink
				}

			case "/", "s":
				// Quick jump to search and focus it
				m.selectedElement = 4
				m.searchFocused = true
				m.searchInput.Focus()
				return m, textinput.Blink

			case "1":
				m.selectedElement = 0
				m.showReq = !m.showReq
				m.saveTypeFilters()
				return m, m.emitFilterChanged()
			case "2":
				m.selectedElement = 1
				m.showRes = !m.showRes
				m.saveTypeFilters()
				return m, m.emitFilterChanged()
			case "3":
				m.selectedElement = 2
				m.showInf = !m.showInf
				m.saveTypeFilters()
				return m, m.emitFilterChanged()
			case "4":
				m.selectedElement = 3
				m.showErr = !m.showErr
				m.saveTypeFilters()
				return m, m.emitFilterChanged()

			case "x", "X":
				// Clear AfterDate filter
				m.afterDate = time.Time{}
				m.saveAfterDate()
				return m, m.emitFilterChanged()
			}
		}
	}

	return m, nil
}

// View renders the filter bar
func (m FilterBarModel) View() string {
	// Styles
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
	activeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6BFF6B")).Bold(true)
	inactiveStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
	selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#7D56F4")).Bold(true).Underline(true)
	searchStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A0A0A0"))
	dateStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFAA00"))

	// Type checkboxes with selection highlighting
	var checkboxes []string

	reqCheck := m.renderCheckbox("req", m.showReq, m.focused && m.selectedElement == 0, activeStyle, inactiveStyle, selectedStyle)
	resCheck := m.renderCheckbox("res", m.showRes, m.focused && m.selectedElement == 1, activeStyle, inactiveStyle, selectedStyle)
	infCheck := m.renderCheckbox("inf", m.showInf, m.focused && m.selectedElement == 2, activeStyle, inactiveStyle, selectedStyle)
	errCheck := m.renderCheckbox("err", m.showErr, m.focused && m.selectedElement == 3, activeStyle, inactiveStyle, selectedStyle)

	checkboxes = append(checkboxes, reqCheck, resCheck, infCheck, errCheck)
	typeSection := strings.Join(checkboxes, " ")

	// Search field with selection highlighting
	searchPrefix := "/ "
	if m.focused && m.selectedElement == 4 && !m.searchFocused {
		searchPrefix = selectedStyle.Render("/ ")
	} else {
		searchPrefix = searchStyle.Render("/ ")
	}
	searchSection := searchPrefix + m.searchInput.View()

	// After date section
	var dateSection string
	if !m.afterDate.IsZero() {
		dateStr := m.afterDate.Format("15:04:05")
		dateSection = labelStyle.Render("After: ") + dateStyle.Render(dateStr) + labelStyle.Render(" [x clear]")
	}

	// Combine sections
	var parts []string
	parts = append(parts, typeSection)
	parts = append(parts, searchSection)
	if dateSection != "" {
		parts = append(parts, dateSection)
	}

	content := strings.Join(parts, labelStyle.Render(" │ "))

	// Border style
	borderColor := lipgloss.Color("#626262")
	if m.focused {
		borderColor = lipgloss.Color("#7D56F4")
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1)

	if m.width > 0 {
		boxStyle = boxStyle.Width(m.width - 3) // Account for border (2) + padding (2) - 1 wider
	}

	return boxStyle.Render(content)
}

// renderCheckbox renders a single checkbox with optional selection highlighting
func (m FilterBarModel) renderCheckbox(label string, checked bool, selected bool, activeStyle, inactiveStyle, selectedStyle lipgloss.Style) string {
	var style lipgloss.Style
	if selected {
		style = selectedStyle
	} else if checked {
		style = activeStyle
	} else {
		style = inactiveStyle
	}

	check := "[ ]"
	if checked {
		check = "[x]"
	}
	return style.Render(check + label)
}

// emitFilterChanged returns a command that emits the filter changed message
func (m FilterBarModel) emitFilterChanged() tea.Cmd {
	return func() tea.Msg {
		return FilterChangedMsg{State: m.GetFilters()}
	}
}

// GetFilters returns the current filter state
func (m FilterBarModel) GetFilters() FilterState {
	return FilterState{
		ShowReq:    m.showReq,
		ShowRes:    m.showRes,
		ShowInf:    m.showInf,
		ShowErr:    m.showErr,
		SearchText: m.searchInput.Value(),
		AfterDate:  m.afterDate,
	}
}

// SetAfterDate sets the after date filter and persists it
func (m *FilterBarModel) SetAfterDate(t time.Time) {
	m.afterDate = t
	m.saveAfterDate()
}

// ClearAfterDate clears the after date filter
func (m *FilterBarModel) ClearAfterDate() {
	m.afterDate = time.Time{}
	m.saveAfterDate()
}

// saveAfterDate persists the AfterDate to config
func (m *FilterBarModel) saveAfterDate() {
	cfg := config.Get()
	cfg.Filter.ClearAfter = m.afterDate
	cfg.Save()
}

// saveTypeFilters persists the type filter settings to config
func (m *FilterBarModel) saveTypeFilters() {
	cfg := config.Get()
	cfg.Filter.ShowReq = &m.showReq
	cfg.Filter.ShowRes = &m.showRes
	cfg.Filter.ShowInf = &m.showInf
	cfg.Filter.ShowErr = &m.showErr
	cfg.Save()
}

// SetFocused sets the focus state
func (m *FilterBarModel) SetFocused(focused bool) {
	m.focused = focused
	if !focused {
		m.searchFocused = false
		m.searchInput.Blur()
	}
}

// IsFocused returns the focus state
func (m FilterBarModel) IsFocused() bool {
	return m.focused
}

// IsSearchFocused returns true if the search input is focused
func (m FilterBarModel) IsSearchFocused() bool {
	return m.searchFocused
}

// SetWidth sets the width
func (m *FilterBarModel) SetWidth(width int) {
	m.width = width
}

// FocusSearch focuses the search input
func (m *FilterBarModel) FocusSearch() tea.Cmd {
	m.searchFocused = true
	m.searchInput.Focus()
	return textinput.Blink
}

// MatchesEntry returns true if the entry passes all filters
func (state FilterState) MatchesEntry(entry logger.LogEntry) bool {
	// Type filter
	switch entry.Type {
	case logger.LogTypeReq:
		if !state.ShowReq {
			return false
		}
	case logger.LogTypeRes:
		if !state.ShowRes {
			return false
		}
	case logger.LogTypeInf:
		if !state.ShowInf {
			return false
		}
	case logger.LogTypeErr:
		if !state.ShowErr {
			return false
		}
	}

	// After date filter
	if !state.AfterDate.IsZero() && entry.Timestamp.Before(state.AfterDate) {
		return false
	}

	// Search text filter (case-insensitive)
	if state.SearchText != "" {
		searchLower := strings.ToLower(state.SearchText)
		previewLower := strings.ToLower(entry.Preview)
		bodyLower := strings.ToLower(entry.Body)
		if !strings.Contains(previewLower, searchLower) && !strings.Contains(bodyLower, searchLower) {
			return false
		}
	}

	return true
}
