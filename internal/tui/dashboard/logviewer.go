package dashboard

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bestk/kiro2cc/internal/tui/logger"
)

// LogViewerModel represents the split-pane log viewer
type LogViewerModel struct {
	entries       []logger.LogEntry
	selectedIndex int
	listOffset    int // For scrolling the list
	detailView    viewport.Model
	width         int
	height        int
	focused       bool
	detailFocused bool // true = detail pane focused, false = list focused
	listWidth     int
	detailWidth   int
}

// NewLogViewerModel creates a new split-pane log viewer
func NewLogViewerModel(width, height int) LogViewerModel {
	listW := width * 40 / 100    // 40% for list
	detailW := width - listW - 3 // Rest for detail (minus divider)

	vp := viewport.New(detailW, height)
	vp.SetContent("")

	return LogViewerModel{
		entries:       make([]logger.LogEntry, 0),
		selectedIndex: 0,
		listOffset:    0,
		detailView:    vp,
		width:         width,
		height:        height,
		listWidth:     listW,
		detailWidth:   detailW,
	}
}

// Init initializes the log viewer
func (m LogViewerModel) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (m LogViewerModel) Update(msg tea.Msg) (LogViewerModel, tea.Cmd) {
	var cmd tea.Cmd
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case logger.LogEntryMsg:
		m.entries = append(m.entries, msg.Entry)
		// Auto-select new entry if we were at the bottom
		if m.selectedIndex == len(m.entries)-2 || len(m.entries) == 1 {
			m.selectedIndex = len(m.entries) - 1
			m.ensureVisible()
		}
		m.updateDetailContent()

	case tea.KeyMsg:
		if m.focused {
			switch msg.String() {
			case "tab":
				// Toggle focus between list and detail pane
				m.detailFocused = !m.detailFocused
			case "up", "k":
				if m.detailFocused {
					m.detailView, cmd = m.detailView.Update(tea.KeyMsg{Type: tea.KeyUp})
					cmds = append(cmds, cmd)
				} else {
					if m.selectedIndex > 0 {
						m.selectedIndex--
						m.ensureVisible()
						m.updateDetailContent()
					}
				}
			case "down", "j":
				if m.detailFocused {
					m.detailView, cmd = m.detailView.Update(tea.KeyMsg{Type: tea.KeyDown})
					cmds = append(cmds, cmd)
				} else {
					if m.selectedIndex < len(m.entries)-1 {
						m.selectedIndex++
						m.ensureVisible()
						m.updateDetailContent()
					}
				}
			case "pgup", "ctrl+u":
				if m.detailFocused {
					m.detailView, cmd = m.detailView.Update(tea.KeyMsg{Type: tea.KeyPgUp})
					cmds = append(cmds, cmd)
				} else {
					m.selectedIndex -= m.height - 2
					if m.selectedIndex < 0 { m.selectedIndex = 0 }
					m.ensureVisible()
					m.updateDetailContent()
				}
			case "pgdown", "ctrl+d":
				if m.detailFocused {
					m.detailView, cmd = m.detailView.Update(tea.KeyMsg{Type: tea.KeyPgDown})
					cmds = append(cmds, cmd)
				} else {
					m.selectedIndex += m.height - 2
					if m.selectedIndex >= len(m.entries) { m.selectedIndex = len(m.entries) - 1 }
					if m.selectedIndex < 0 { m.selectedIndex = 0 }
					m.ensureVisible()
					m.updateDetailContent()
				}
			case "home", "g":
				if m.detailFocused { m.detailView.GotoTop() } else {
					m.selectedIndex = 0
					m.ensureVisible()
					m.updateDetailContent()
				}
			case "end", "G":
				if m.detailFocused { m.detailView.GotoBottom() } else {
					if len(m.entries) > 0 {
						m.selectedIndex = len(m.entries) - 1
						m.ensureVisible()
						m.updateDetailContent()
					}
				}
			}
		}

	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
	}

	return m, tea.Batch(cmds...)
}

// ensureVisible ensures the selected item is visible in the list
func (m *LogViewerModel) ensureVisible() {
	visibleLines := m.height - 2
	if m.selectedIndex < m.listOffset {
		m.listOffset = m.selectedIndex
	} else if m.selectedIndex >= m.listOffset+visibleLines {
		m.listOffset = m.selectedIndex - visibleLines + 1
	}
}

// updateDetailContent updates the detail view with selected entry
func (m *LogViewerModel) updateDetailContent() {
	if len(m.entries) == 0 || m.selectedIndex < 0 || m.selectedIndex >= len(m.entries) {
		m.detailView.SetContent(lipgloss.NewStyle().
			Foreground(lipgloss.Color("#626262")).
			Render("No entry selected"))
		return
	}

	entry := m.entries[m.selectedIndex]

	// Style definitions
	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#7D56F4")).
		Bold(true)

	valueStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FAFAFA"))

	dimStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#626262"))

	// Build detail content
	var lines []string

	// Header with timestamp and type
	typeColor := "#FAFAFA"
	switch entry.Type {
	case logger.LogTypeReq:
		typeColor = "#6BFF6B"
	case logger.LogTypeRes:
		typeColor = "#6B9FFF"
	case logger.LogTypeErr:
		typeColor = "#FF6B6B"
	case logger.LogTypeInf:
		typeColor = "#FFFF6B"
	}

	typeStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(typeColor)).
		Bold(true)

	lines = append(lines, fmt.Sprintf("%s %s",
		dimStyle.Render(entry.Timestamp.Format("15:04:05")),
		typeStyle.Render(fmt.Sprintf("[%s]", entry.Type.String())),
	))
	lines = append(lines, "")

	// Details based on entry type
	if entry.Method != "" {
		lines = append(lines, fmt.Sprintf("%s %s", labelStyle.Render("Method:"), valueStyle.Render(entry.Method)))
	}
	if entry.Path != "" {
		lines = append(lines, fmt.Sprintf("%s %s", labelStyle.Render("Path:"), valueStyle.Render(entry.Path)))
	}
	if entry.Model != "" {
		lines = append(lines, fmt.Sprintf("%s %s", labelStyle.Render("Model:"), valueStyle.Render(entry.Model)))
	}
	if entry.Status != 0 {
		statusColor := "#6BFF6B"
		if entry.Status >= 400 {
			statusColor = "#FFFF6B"
		}
		if entry.Status >= 500 {
			statusColor = "#FF6B6B"
		}
		lines = append(lines, fmt.Sprintf("%s %s",
			labelStyle.Render("Status:"),
			lipgloss.NewStyle().Foreground(lipgloss.Color(statusColor)).Render(fmt.Sprintf("%d", entry.Status)),
		))
	}
	if entry.Duration > 0 {
		lines = append(lines, fmt.Sprintf("%s %s", labelStyle.Render("Duration:"), valueStyle.Render(entry.Duration.String())))
	}

	// Preview/body
	if entry.Body != "" {
		lines = append(lines, "")
		lines = append(lines, labelStyle.Render("Content:"))
		// Word-wrap the preview
		wrapped := wrapText(entry.Body, m.detailWidth-4)
		lines = append(lines, dimStyle.Render(wrapped))
	}

	content := strings.Join(lines, "\n")
	m.detailView.SetContent(content)
	m.detailView.GotoTop()
}

// wrapText wraps text to fit within a given width
func wrapText(text string, width int) string {
	if width <= 0 {
		width = 40
	}

	var lines []string
	for len(text) > width {
		// Find a good break point
		breakAt := width
		for i := width; i > width/2; i-- {
			if text[i] == ' ' || text[i] == ',' || text[i] == '}' || text[i] == ']' {
				breakAt = i + 1
				break
			}
		}
		lines = append(lines, text[:breakAt])
		text = text[breakAt:]
	}
	if len(text) > 0 {
		lines = append(lines, text)
	}
	return strings.Join(lines, "\n")
}

// View renders the split-pane log viewer
func (m LogViewerModel) View() string {
	// Styles
	selectedStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#3D3D5C")).
		Foreground(lipgloss.Color("#FAFAFA"))

	normalStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#A0A0A0"))

	dimStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#626262"))

	// Build list view
	var listLines []string
	visibleLines := m.height - 2
	if visibleLines < 1 {
		visibleLines = 1
	}

	if len(m.entries) == 0 {
		listLines = append(listLines, dimStyle.Render("No logs yet..."))
	} else {
		for i := m.listOffset; i < len(m.entries) && i < m.listOffset+visibleLines; i++ {
			entry := m.entries[i]

			// Type indicator
			typeChar := "o"
			typeColor := "#626262"
			switch entry.Type {
			case logger.LogTypeReq:
				typeChar = ">"
				typeColor = "#6BFF6B"
			case logger.LogTypeRes:
				typeChar = "<"
				typeColor = "#6B9FFF"
			case logger.LogTypeErr:
				typeChar = "x"
				typeColor = "#FF6B6B"
			case logger.LogTypeInf:
				typeChar = "o"
				typeColor = "#FFFF6B"
			}

			// Build line: [time] [type] preview
			timeStr := entry.Timestamp.Format("15:04:05")
			preview := entry.Preview
			maxPreview := m.listWidth - 14 // Leave room for time and type
			if maxPreview < 10 {
				maxPreview = 10
			}
			if len(preview) > maxPreview {
				preview = preview[:maxPreview-3] + "..."
			}

			line := fmt.Sprintf("%s %s %s",
				dimStyle.Render(timeStr),
				lipgloss.NewStyle().Foreground(lipgloss.Color(typeColor)).Render(typeChar),
				preview,
			)

			if i == m.selectedIndex {
				listLines = append(listLines, selectedStyle.Render(line))
			} else {
				listLines = append(listLines, normalStyle.Render(line))
			}
		}
	}

	// Pad list to full height
	for len(listLines) < visibleLines {
		listLines = append(listLines, "")
	}

	listContent := strings.Join(listLines, "\n")

	// Divider
	dividerLines := make([]string, visibleLines)
	for i := range dividerLines {
		dividerLines[i] = "|"
	}
	divider := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#383838")).
		Render(strings.Join(dividerLines, "\n"))

	// Detail view
	detailContent := m.detailView.View()

	// Join horizontally
	return lipgloss.JoinHorizontal(lipgloss.Top,
		listContent,
		" "+divider+" ",
		detailContent,
	)
}

// SetFocused sets the focus state
func (m *LogViewerModel) SetFocused(focused bool) {
	m.focused = focused
}

// IsFocused returns the focus state
func (m LogViewerModel) IsFocused() bool {
	return m.focused
}

// SetSize updates the dimensions
func (m *LogViewerModel) SetSize(width, height int) {
	m.width = width
	m.height = height
	m.listWidth = width * 40 / 100
	m.detailWidth = width - m.listWidth - 3

	m.detailView.Width = m.detailWidth
	m.detailView.Height = height - 2
	m.updateDetailContent()
}

// AddEntry adds a log entry
func (m *LogViewerModel) AddEntry(entry logger.LogEntry) {
	m.entries = append(m.entries, entry)
	// Auto-select new entry if at bottom
	if m.selectedIndex == len(m.entries)-2 || len(m.entries) == 1 {
		m.selectedIndex = len(m.entries) - 1
		m.ensureVisible()
	}
	m.updateDetailContent()
}

// Clear removes all entries
func (m *LogViewerModel) Clear() {
	m.entries = m.entries[:0]
	m.selectedIndex = 0
	m.listOffset = 0
	m.updateDetailContent()
}

// ScrollInfo returns scroll position info
func (m LogViewerModel) ScrollInfo() string {
	if len(m.entries) == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", m.selectedIndex+1, len(m.entries))
}

// AtBottom returns true if at the last entry
func (m LogViewerModel) AtBottom() bool {
	return m.selectedIndex >= len(m.entries)-1
}

// EntryCount returns the number of log entries
func (m LogViewerModel) EntryCount() int {
	return len(m.entries)
}
