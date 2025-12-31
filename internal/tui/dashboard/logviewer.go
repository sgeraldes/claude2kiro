package dashboard

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// Panel focus states
type PanelFocus int

const (
	FocusList PanelFocus = iota
	FocusDetail
)

// ViewMode defines how content is displayed in the detail panel
type ViewMode int

const (
	ViewModeParsed ViewMode = iota // Structured display (default)
	ViewModeJSON                   // JSON pretty-printed with syntax highlighting
	ViewModeRaw                    // Raw content as-is
)

// String returns the display name of the view mode
func (v ViewMode) String() string {
	switch v {
	case ViewModeParsed:
		return "Parsed"
	case ViewModeJSON:
		return "JSON"
	case ViewModeRaw:
		return "Raw"
	default:
		return "?"
	}
}

// FocusFilterBarMsg signals that the user wants to move up from the log list to the filter bar
type FocusFilterBarMsg struct{}

// Styles for composable views
var (
	// Unfocused panel - hidden border (same space as focused, but invisible)
	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.HiddenBorder()).
			Padding(0, 1)

	// Focused panel - visible border
	focusedPanelStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#7D56F4")).
				Padding(0, 1)

	// Colors
	dimColor      = lipgloss.Color("#626262")
	normalColor   = lipgloss.Color("#A0A0A0")
	brightColor   = lipgloss.Color("#FAFAFA")
	accentColor   = lipgloss.Color("#7D56F4")
	successColor  = lipgloss.Color("#6BFF6B")
	infoColor     = lipgloss.Color("#6B9FFF")
	errorColor    = lipgloss.Color("#FF6B6B")
	warnColor     = lipgloss.Color("#FFFF6B")
	selectedBg    = lipgloss.Color("#3D3D5C")

	// Session color palette - distinct colors for different sessions
	sessionColors = []lipgloss.Color{
		lipgloss.Color("#FF6B6B"), // Red
		lipgloss.Color("#4ECDC4"), // Teal
		lipgloss.Color("#FFE66D"), // Yellow
		lipgloss.Color("#95E1D3"), // Mint
		lipgloss.Color("#F38181"), // Coral
		lipgloss.Color("#AA96DA"), // Lavender
		lipgloss.Color("#FCBAD3"), // Pink
		lipgloss.Color("#A8D8EA"), // Light blue
		lipgloss.Color("#FF9F43"), // Orange
		lipgloss.Color("#78E08F"), // Green
	}
)

// SessionMetadata holds metadata about a session extracted from conversation
type SessionMetadata struct {
	SessionID  string // The session ID
	WorkingDir string // Working directory path
	FolderName string // Just the folder name (extracted from WorkingDir)
	Title      string // Conversation title from Claude's response
}

// LogViewerModel represents the split-pane log viewer with composable views
type LogViewerModel struct {
	allEntries    []logger.LogEntry // All log entries (unfiltered)
	entries       []logger.LogEntry // Filtered entries for display
	selectedIndex int
	listOffset    int
	detailView    viewport.Model
	width         int
	height        int
	focused       bool
	panelFocus    PanelFocus
	listWidth     int
	detailWidth   int
	// Session filtering
	sessions        []string // Unique session IDs (empty string = "All")
	selectedSession int      // Index into sessions (-1 or 0 = All)
	// Session colors - maps session ID to color index
	sessionColorMap map[string]int
	// Session friendly names - maps session ID to folder name extracted from "Working directory:"
	sessionNameMap map[string]string
	// Session metadata - maps session ID to full metadata (title, folder, etc.)
	sessionMetadataMap map[string]*SessionMetadata
	// Filter state (from filter bar)
	filterState FilterState
	// View mode for detail panel content display
	viewMode ViewMode
	// Expand content - show full text without truncation
	expandContent bool
}

// NewLogViewerModel creates a new split-pane log viewer
func NewLogViewerModel(width, height int) LogViewerModel {
	cfg := config.Get()
	listW := width * cfg.Display.ListWidthPercent / 100
	detailW := width - listW - 1 // Rest for detail (minus 1 for gap space)

	// Panel: rendered width = detailW, text area = detailW - 4 (border + padding)
	vpWidth := detailW - 4
	if vpWidth < 10 {
		vpWidth = 10
	}
	vp := viewport.New(vpWidth, height-4)
	vp.SetContent("")

	// Load persisted settings from config
	cfg2 := config.Get()

	// Determine initial view mode
	var initialViewMode ViewMode
	switch cfg2.Display.DefaultViewMode {
	case "parsed":
		initialViewMode = ViewModeParsed
	case "json":
		initialViewMode = ViewModeJSON
	case "raw":
		initialViewMode = ViewModeRaw
	case "last":
		// Load last used from filter config
		switch cfg2.Filter.LastViewMode {
		case "json":
			initialViewMode = ViewModeJSON
		case "raw":
			initialViewMode = ViewModeRaw
		default:
			initialViewMode = ViewModeParsed
		}
	default:
		initialViewMode = ViewModeParsed
	}

	// Determine initial expand mode
	var initialExpand bool
	switch cfg2.Display.DefaultExpandMode {
	case "expanded":
		initialExpand = true
	case "compact":
		initialExpand = false
	case "last":
		// Load last used from filter config
		initialExpand = cfg2.Filter.LastExpandMode == "expanded"
	default:
		initialExpand = false
	}

	return LogViewerModel{
		allEntries:         make([]logger.LogEntry, 0),
		entries:            make([]logger.LogEntry, 0),
		selectedIndex:      0,
		listOffset:         0,
		detailView:         vp,
		width:              width,
		height:             height,
		listWidth:          listW,
		detailWidth:        detailW,
		panelFocus:         FocusList,
		sessions:           []string{""}, // Start with "All" only
		selectedSession:    0,            // "All" selected
		sessionColorMap:    make(map[string]int),
		sessionNameMap:     make(map[string]string),
		sessionMetadataMap: make(map[string]*SessionMetadata),
		viewMode:           initialViewMode,
		expandContent:      initialExpand,
		filterState: FilterState{
			ShowReq:   true,
			ShowRes:   true,
			ShowInf:   false,
			ShowErr:   false,
			AfterDate: cfg2.Filter.ClearAfter,
		},
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
		// Add to all entries
		cfg := config.Get()
		m.allEntries = append(m.allEntries, msg.Entry)
		if len(m.allEntries) > cfg.Logging.MaxEntries {
			excess := len(m.allEntries) - cfg.Logging.MaxEntries
			m.allEntries = m.allEntries[excess:]
		}

		// Update sessions list if this is a new session
		if msg.Entry.SessionID != "" {
			if _, exists := m.sessionColorMap[msg.Entry.SessionID]; !exists {
				// Assign a color to this session
				colorIndex := len(m.sessionColorMap) % len(sessionColors)
				m.sessionColorMap[msg.Entry.SessionID] = colorIndex
				m.sessions = append(m.sessions, msg.Entry.SessionID)
			}

			// Try to extract working directory as friendly session name
			m.extractSessionName(msg.Entry)
			// Try to extract title from response entries
			m.extractSessionTitle(msg.Entry)
		}

		// Re-filter entries based on current session
		m.applySessionFilter()

		// Auto-select new entry if at bottom and entry matches filter
		if m.selectedSession == 0 || msg.Entry.SessionID == m.sessions[m.selectedSession] {
			if m.selectedIndex == len(m.entries)-2 || len(m.entries) == 1 {
				m.selectedIndex = len(m.entries) - 1
				m.ensureVisible()
			}
		}
		m.updateDetailContent()

	case tea.KeyMsg:
		if m.focused {
			switch msg.String() {
			case "tab":
				// Toggle focus between list and detail panel
				if m.panelFocus == FocusList {
					m.panelFocus = FocusDetail
				} else {
					m.panelFocus = FocusList
				}

			case "left", "h":
				// In list panel: cycle sessions left (toward "All")
				if m.panelFocus == FocusList && len(m.sessions) > 1 {
					m.selectedSession--
					if m.selectedSession < 0 {
						m.selectedSession = len(m.sessions) - 1
					}
					m.applySessionFilter()
					m.selectedIndex = 0
					m.listOffset = 0
					m.updateDetailContent()
				}

			case "right", "l":
				// In list panel: cycle sessions right
				if m.panelFocus == FocusList && len(m.sessions) > 1 {
					m.selectedSession = (m.selectedSession + 1) % len(m.sessions)
					m.applySessionFilter()
					m.selectedIndex = 0
					m.listOffset = 0
					m.updateDetailContent()
				}

			// Note: "c" and "s" keys are now handled in dashboard

			case "up":
				if m.panelFocus == FocusDetail {
					m.detailView, cmd = m.detailView.Update(tea.KeyMsg{Type: tea.KeyUp})
					cmds = append(cmds, cmd)
				} else {
					if m.selectedIndex > 0 {
						m.selectedIndex--
						m.ensureVisible()
						m.updateDetailContent()
					} else {
						// At first entry, signal to focus filter bar
						return m, func() tea.Msg { return FocusFilterBarMsg{} }
					}
				}

			case "down":
				if m.panelFocus == FocusDetail {
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
				if m.panelFocus == FocusDetail {
					m.detailView, cmd = m.detailView.Update(tea.KeyMsg{Type: tea.KeyPgUp})
					cmds = append(cmds, cmd)
				} else {
					m.selectedIndex -= m.height - 2
					if m.selectedIndex < 0 {
						m.selectedIndex = 0
					}
					m.ensureVisible()
					m.updateDetailContent()
				}

			case "pgdown", "ctrl+d":
				if m.panelFocus == FocusDetail {
					m.detailView, cmd = m.detailView.Update(tea.KeyMsg{Type: tea.KeyPgDown})
					cmds = append(cmds, cmd)
				} else {
					m.selectedIndex += m.height - 2
					if m.selectedIndex >= len(m.entries) {
						m.selectedIndex = len(m.entries) - 1
					}
					if m.selectedIndex < 0 {
						m.selectedIndex = 0
					}
					m.ensureVisible()
					m.updateDetailContent()
				}

			case "home", "g":
				if m.panelFocus == FocusDetail {
					m.detailView.GotoTop()
				} else {
					m.selectedIndex = 0
					m.ensureVisible()
					m.updateDetailContent()
				}

			case "end", "G":
				if m.panelFocus == FocusDetail {
					m.detailView.GotoBottom()
				} else {
					if len(m.entries) > 0 {
						m.selectedIndex = len(m.entries) - 1
						m.ensureVisible()
						m.updateDetailContent()
					}
				}

			case "v":
				// Cycle view mode: Parsed -> JSON -> Raw -> Parsed
				m.viewMode = (m.viewMode + 1) % 3
				m.updateDetailContent()
				m.saveViewModeToConfig()

			case "e":
				// Toggle expand content (show full text without truncation)
				m.expandContent = !m.expandContent
				m.updateDetailContent()
				m.saveExpandModeToConfig()

			case "y":
				// Copy current entry to clipboard
				if content := m.getExportContent(); content != "" {
					return m, copyToClipboard(content)
				}

			case "o":
				// Open current entry in external editor
				if content := m.getExportContent(); content != "" {
					return m, openInEditor(content, m.viewMode)
				}

			case "r":
				// Navigate forward: req -> res (same SeqNum), res -> next req
				if idx := m.findNextInPair(); idx >= 0 {
					m.selectedIndex = idx
					m.ensureVisible()
					m.updateDetailContent()
				}

			case "R":
				// Navigate backward: res -> req (same SeqNum), req -> prev res
				if idx := m.findPrevInPair(); idx >= 0 {
					m.selectedIndex = idx
					m.ensureVisible()
					m.updateDetailContent()
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
	// Calculate visible lines to match renderListPanel exactly:
	// View() passes (m.height - 2) to renderListPanel
	// renderListPanel: height = m.height - 2
	// If sessions > 1: height -= 2 → m.height - 4
	// entriesHeight = height - 2 → m.height - 6 (with sessions) or m.height - 4 (without)
	visibleLines := m.height - 4 // Base: account for panel borders and pagination

	// Account for session tabs (2 lines if multiple sessions)
	if len(m.sessions) > 1 {
		visibleLines -= 2
	}

	if visibleLines < 1 {
		visibleLines = 1
	}

	if m.selectedIndex < m.listOffset {
		m.listOffset = m.selectedIndex
	} else if m.selectedIndex >= m.listOffset+visibleLines {
		m.listOffset = m.selectedIndex - visibleLines + 1
	}
}

// applySessionFilter filters allEntries based on selected session and filter state
func (m *LogViewerModel) applySessionFilter() {
	// Start with all entries or session-filtered entries
	var baseEntries []logger.LogEntry
	if m.selectedSession == 0 || len(m.sessions) == 0 || m.sessions[m.selectedSession] == "" {
		// "All" selected - use all entries
		baseEntries = m.allEntries
	} else {
		// Filter by selected session
		sessionID := m.sessions[m.selectedSession]
		baseEntries = make([]logger.LogEntry, 0, len(m.allEntries))
		for _, entry := range m.allEntries {
			if entry.SessionID == sessionID {
				baseEntries = append(baseEntries, entry)
			}
		}
	}

	// Apply all filters from filter state
	m.entries = make([]logger.LogEntry, 0, len(baseEntries))
	for _, entry := range baseEntries {
		if m.filterState.MatchesEntry(entry) {
			m.entries = append(m.entries, entry)
		}
	}

	// Adjust selection if needed
	if m.selectedIndex >= len(m.entries) {
		m.selectedIndex = len(m.entries) - 1
		if m.selectedIndex < 0 {
			m.selectedIndex = 0
		}
	}
	m.ensureVisible()
}

// ApplyFilters updates the filter state and re-filters entries
func (m *LogViewerModel) ApplyFilters(state FilterState) {
	m.filterState = state
	m.updateVisibleSessions() // Filter sessions that have no matching entries
	m.applySessionFilter()
	m.updateDetailContent()
}

// updateVisibleSessions rebuilds the sessions list to only include sessions with matching entries
func (m *LogViewerModel) updateVisibleSessions() {
	// Always keep "All" as first option
	newSessions := []string{""}

	// Check each session for matching entries
	for sessionID, colorIdx := range m.sessionColorMap {
		hasMatchingEntry := false
		for _, entry := range m.allEntries {
			if entry.SessionID == sessionID && m.filterState.MatchesEntry(entry) {
				hasMatchingEntry = true
				break
			}
		}
		if hasMatchingEntry {
			newSessions = append(newSessions, sessionID)
			// Keep the color mapping (it's already there)
			_ = colorIdx
		}
	}

	// Update sessions list
	oldSelectedSession := ""
	if m.selectedSession > 0 && m.selectedSession < len(m.sessions) {
		oldSelectedSession = m.sessions[m.selectedSession]
	}

	m.sessions = newSessions

	// Try to keep the same session selected
	m.selectedSession = 0 // Default to "All"
	if oldSelectedSession != "" {
		for i, s := range m.sessions {
			if s == oldSelectedSession {
				m.selectedSession = i
				break
			}
		}
	}
}

// GetFilterState returns the current filter state
func (m LogViewerModel) GetFilterState() FilterState {
	return m.filterState
}

// GetCurrentSession returns the currently selected session ID (empty = All)
func (m LogViewerModel) GetCurrentSession() string {
	if m.selectedSession < 0 || m.selectedSession >= len(m.sessions) {
		return ""
	}
	return m.sessions[m.selectedSession]
}

// GetSessionCount returns the number of unique sessions
func (m LogViewerModel) GetSessionCount() int {
	return len(m.sessions) - 1 // Exclude "All"
}

// GetCurrentSessionMetadata returns metadata for the currently selected session
// Returns nil if "All" is selected or if no metadata exists
func (m LogViewerModel) GetCurrentSessionMetadata() *SessionMetadata {
	sessionID := m.GetCurrentSession()
	if sessionID == "" {
		return nil // "All" selected
	}
	if meta, exists := m.sessionMetadataMap[sessionID]; exists {
		return meta
	}
	// Return basic metadata with just the session ID
	return &SessionMetadata{SessionID: sessionID}
}

// GetViewMode returns the current view mode
func (m LogViewerModel) GetViewMode() ViewMode {
	return m.viewMode
}

// updateDetailContent updates the detail view with selected entry
func (m *LogViewerModel) updateDetailContent() {
	if len(m.entries) == 0 || m.selectedIndex < 0 || m.selectedIndex >= len(m.entries) {
		m.detailView.SetContent(lipgloss.NewStyle().
			Foreground(dimColor).
			Render("No entry selected"))
		return
	}

	entry := m.entries[m.selectedIndex]

	labelStyle := lipgloss.NewStyle().Foreground(accentColor).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(brightColor)
	dimStyle := lipgloss.NewStyle().Foreground(dimColor)

	var lines []string

	// Type with color
	typeColor := brightColor
	switch entry.Type {
	case logger.LogTypeReq:
		typeColor = successColor
	case logger.LogTypeRes:
		typeColor = infoColor
	case logger.LogTypeErr:
		typeColor = errorColor
	case logger.LogTypeInf:
		typeColor = warnColor
	}
	typeStyle := lipgloss.NewStyle().Foreground(typeColor).Bold(true)

	lines = append(lines, fmt.Sprintf("%s %s",
		dimStyle.Render(entry.Timestamp.Format("15:04:05")),
		typeStyle.Render(fmt.Sprintf("[%s]", entry.Type.String())),
	))
	lines = append(lines, "")

	// Session/Request IDs
	if entry.SessionID != "" || entry.RequestID != "" {
		idStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFAA00"))
		var idParts []string
		if entry.SessionID != "" {
			idParts = append(idParts, "session:"+entry.SessionID)
		}
		if entry.RequestID != "" {
			idParts = append(idParts, "req:"+entry.RequestID)
		}
		lines = append(lines, labelStyle.Render("ID: ")+idStyle.Render(strings.Join(idParts, " ")))
	}

	// Details
	if entry.Method != "" {
		lines = append(lines, labelStyle.Render("Method: ")+valueStyle.Render(entry.Method))
	}
	if entry.Path != "" {
		lines = append(lines, labelStyle.Render("Path: ")+valueStyle.Render(entry.Path))
	}
	if entry.Model != "" {
		lines = append(lines, labelStyle.Render("Model: ")+valueStyle.Render(entry.Model))
	}
	if entry.Status != 0 {
		statusColor := successColor
		if entry.Status >= 400 {
			statusColor = warnColor
		}
		if entry.Status >= 500 {
			statusColor = errorColor
		}
		lines = append(lines, labelStyle.Render("Status: ")+
			lipgloss.NewStyle().Foreground(statusColor).Render(fmt.Sprintf("%d", entry.Status)))
	}
	if entry.Duration > 0 {
		lines = append(lines, labelStyle.Render("Duration: ")+valueStyle.Render(entry.Duration.String()))
	}

	// View mode indicator
	modeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262")).Italic(true)
	expandStr := ""
	if m.expandContent {
		expandStr = " • [e] Full"
	} else {
		expandStr = " • [e] Compact"
	}
	lines = append(lines, modeStyle.Render(fmt.Sprintf("[v] View: %s%s", m.viewMode.String(), expandStr)))

	// Body content - parse and display based on entry type and view mode
	content := entry.Body
	if content == "" {
		content = entry.Preview
	}
	if content != "" {
		lines = append(lines, "")

		switch m.viewMode {
		case ViewModeRaw:
			// Raw content - show as-is
			lines = append(lines, labelStyle.Render("Content (Raw):"))
			lines = append(lines, valueStyle.Render(wrapTextSimple(content, m.detailWidth-8)))

		case ViewModeJSON:
			// JSON pretty-printed with syntax highlighting
			lines = append(lines, labelStyle.Render("Content (JSON):"))
			formatted := formatContent(content, m.detailWidth-8)
			lines = append(lines, formatted)

		case ViewModeParsed:
			// Structured parsed view based on entry type
			if entry.Type == logger.LogTypeReq {
				structuredLines := formatRequestContent(content, m.detailWidth-8, m.expandContent)
				lines = append(lines, structuredLines...)
			} else if entry.Type == logger.LogTypeRes {
				structuredLines := formatResponseContent(content, m.detailWidth-8, m.expandContent)
				lines = append(lines, structuredLines...)
			} else {
				// For INF/ERR, show as plain text
				lines = append(lines, labelStyle.Render("Message:"))
				lines = append(lines, valueStyle.Render(wrapTextSimple(content, m.detailWidth-8)))
			}
		}
	}

	m.detailView.SetContent(strings.Join(lines, "\n"))
	m.detailView.GotoTop()
}

// formatRequestContent parses and formats an Anthropic API request
// When expand is true, shows full content without truncation
func formatRequestContent(content string, maxWidth int, expand bool) []string {
	// Styles
	headerStyle := lipgloss.NewStyle().Foreground(accentColor).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(dimColor)
	valueStyle := lipgloss.NewStyle().Foreground(brightColor)
	dimStyle := lipgloss.NewStyle().Foreground(dimColor)
	toolStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF9F43")).Bold(true)
	toolDimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#B8860B"))
	userStyle := lipgloss.NewStyle().Foreground(successColor).Bold(true)
	assistantStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6BB3FF")).Bold(true)
	systemTagStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Italic(true)
	separatorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#444444"))

	var lines []string

	// Try to parse as JSON
	var req map[string]interface{}
	if err := json.Unmarshal([]byte(content), &req); err != nil {
		// Not valid JSON, show as plain text
		lines = append(lines, headerStyle.Render("Content:"))
		lines = append(lines, valueStyle.Render(wrapTextSimple(content, maxWidth)))
		return lines
	}

	// ═══════════════════════════════════════════════════════════════════════
	// HEADER SECTION - Model info in compact format
	// ═══════════════════════════════════════════════════════════════════════
	// Create full-width header
	headerText := "─── Request "
	remainingWidth := maxWidth - lipgloss.Width(headerText) - 2
	if remainingWidth < 0 {
		remainingWidth = 0
	}
	lines = append(lines, headerStyle.Render("╭"+headerText+strings.Repeat("─", remainingWidth)+"╮"))

	// Build header info line
	var headerParts []string
	if model, ok := req["model"].(string); ok {
		// Shorten model name if too long
		shortModel := model
		if len(shortModel) > 30 {
			shortModel = shortModel[:27] + "..."
		}
		headerParts = append(headerParts, valueStyle.Render(shortModel))
	}
	if maxTokens, ok := req["max_tokens"].(float64); ok {
		headerParts = append(headerParts, labelStyle.Render(fmt.Sprintf("max:%d", int(maxTokens))))
	}
	if stream, ok := req["stream"].(bool); ok && stream {
		headerParts = append(headerParts, labelStyle.Render("stream"))
	}
	if temp, ok := req["temperature"].(float64); ok {
		headerParts = append(headerParts, labelStyle.Render(fmt.Sprintf("temp:%.1f", temp)))
	}
	if len(headerParts) > 0 {
		lines = append(lines, strings.Join(headerParts, labelStyle.Render(" • ")))
	}
	lines = append(lines, "")

	// ═══════════════════════════════════════════════════════════════════════
	// TOOLS SECTION - Compact list (limited to 10 in compact mode)
	// ═══════════════════════════════════════════════════════════════════════
	if tools, ok := req["tools"].([]interface{}); ok && len(tools) > 0 {
		toolsHeaderText := fmt.Sprintf("─── Tools (%d) ", len(tools))
		toolsRemaining := maxWidth - lipgloss.Width(toolsHeaderText) - 2
		if toolsRemaining < 0 {
			toolsRemaining = 0
		}
		lines = append(lines, headerStyle.Render("╭"+toolsHeaderText+strings.Repeat("─", toolsRemaining)+"╮"))

		// In compact mode, show only first 10 tools
		maxTools := len(tools)
		if !expand && maxTools > 10 {
			maxTools = 10
		}

		for i := 0; i < maxTools; i++ {
			tool := tools[i]
			if toolMap, ok := tool.(map[string]interface{}); ok {
				name := "?"
				if n, ok := toolMap["name"].(string); ok {
					name = n
				}
				lines = append(lines, toolStyle.Render("  • ")+toolDimStyle.Render(name))
			}
		}

		// Show "...and N more" if truncated
		if !expand && len(tools) > 10 {
			remaining := len(tools) - 10
			lines = append(lines, dimStyle.Render(fmt.Sprintf("  ...and %d more", remaining)))
		}
		lines = append(lines, "")
	}

	// ═══════════════════════════════════════════════════════════════════════
	// SYSTEM SECTION - Handle system instructions
	// ═══════════════════════════════════════════════════════════════════════
	if system, ok := req["system"]; ok {
		systemTexts := extractSystemTexts(system)
		if len(systemTexts) > 0 {
			sysHeaderText := "─── System "
			sysRemaining := maxWidth - lipgloss.Width(sysHeaderText) - 2
			if sysRemaining < 0 {
				sysRemaining = 0
			}
			lines = append(lines, headerStyle.Render("╭"+sysHeaderText+strings.Repeat("─", sysRemaining)+"╮"))
			for _, sysText := range systemTexts {
				formatted := formatSystemText(sysText, maxWidth, expand, systemTagStyle, dimStyle)
				lines = append(lines, formatted...)
			}
			lines = append(lines, "")
		}
	}

	// ═══════════════════════════════════════════════════════════════════════
	// MESSAGES SECTION - Chat-like conversation view
	// ═══════════════════════════════════════════════════════════════════════
	if messages, ok := req["messages"].([]interface{}); ok && len(messages) > 0 {
		convHeaderText := fmt.Sprintf("─── Conversation (%d) ", len(messages))
		convRemaining := maxWidth - lipgloss.Width(convHeaderText) - 2
		if convRemaining < 0 {
			convRemaining = 0
		}
		lines = append(lines, headerStyle.Render("╭"+convHeaderText+strings.Repeat("─", convRemaining)+"╮"))
		lines = append(lines, "")

		for i, msg := range messages {
			if msgMap, ok := msg.(map[string]interface{}); ok {
				role := "unknown"
				if r, ok := msgMap["role"].(string); ok {
					role = r
				}

				// Format role header
				roleLabel := ""
				roleStyle := dimStyle
				if role == "user" {
					roleLabel = fmt.Sprintf("◀ USER [%d]", i+1)
					roleStyle = userStyle
				} else if role == "assistant" {
					roleLabel = fmt.Sprintf("▶ ASSISTANT [%d]", i+1)
					roleStyle = assistantStyle
				} else {
					roleLabel = fmt.Sprintf("? %s [%d]", strings.ToUpper(role), i+1)
				}
				lines = append(lines, roleStyle.Render(roleLabel))

				// Format message content
				msgLines := formatMessageContent(msgMap["content"], maxWidth, expand, valueStyle, toolStyle, toolDimStyle, dimStyle, systemTagStyle)
				lines = append(lines, msgLines...)

				// Add separator between messages
				if i < len(messages)-1 {
					lines = append(lines, separatorStyle.Render(strings.Repeat("─", maxWidth/2)))
				}
				lines = append(lines, "")
			}
		}
	}

	return lines
}

// extractSystemTexts extracts text content from system field (string or array)
func extractSystemTexts(system interface{}) []string {
	var texts []string
	switch s := system.(type) {
	case string:
		texts = append(texts, s)
	case []interface{}:
		for _, sysMsg := range s {
			if msgMap, ok := sysMsg.(map[string]interface{}); ok {
				if text, ok := msgMap["text"].(string); ok {
					texts = append(texts, text)
				}
			}
		}
	}
	return texts
}

// formatSystemText formats system instruction text with special handling for tags
func formatSystemText(text string, maxWidth int, expand bool, tagStyle, dimStyle lipgloss.Style) []string {
	var lines []string

	// Check for system-reminder tags and format them specially
	if strings.Contains(text, "<system-reminder>") {
		parts := splitByTags(text, "system-reminder")
		for _, part := range parts {
			if part.isTag {
				// Show system-reminder in collapsed/dimmed form
				if expand {
					lines = append(lines, tagStyle.Render("  ┌─ <system-reminder>"))
					wrapped := wrapTextToLines(part.content, maxWidth-4)
					for _, line := range wrapped {
						lines = append(lines, dimStyle.Render("  │ "+line))
					}
					lines = append(lines, tagStyle.Render("  └─ </system-reminder>"))
				} else {
					// Compact: show just first line and indicator
					preview := part.content
					if len(preview) > 60 {
						preview = preview[:57] + "..."
					}
					// Clean up whitespace
					preview = strings.Join(strings.Fields(preview), " ")
					lines = append(lines, tagStyle.Render(fmt.Sprintf("  [system-reminder: %s]", preview)))
				}
			} else if strings.TrimSpace(part.content) != "" {
				// Regular text - wrap nicely
				wrapped := wrapTextToLines(part.content, maxWidth-2)
				for _, line := range wrapped {
					lines = append(lines, dimStyle.Render("  "+line))
				}
			}
		}
	} else {
		// No special tags - just wrap the text
		maxChars := maxWidth * 10
		if expand {
			maxChars = 1000000
		}
		displayText := text
		if len(displayText) > maxChars {
			displayText = displayText[:maxChars-3] + "..."
		}
		wrapped := wrapTextToLines(displayText, maxWidth-2)
		for _, line := range wrapped {
			lines = append(lines, dimStyle.Render("  "+line))
		}
	}

	return lines
}

// tagPart represents a part of text that may be inside or outside a tag
type tagPart struct {
	content string
	isTag   bool
}

// splitByTags splits text by XML-like tags
func splitByTags(text, tagName string) []tagPart {
	var parts []tagPart
	openTag := "<" + tagName + ">"
	closeTag := "</" + tagName + ">"

	for len(text) > 0 {
		openIdx := strings.Index(text, openTag)
		if openIdx == -1 {
			// No more tags, add remaining text
			if text != "" {
				parts = append(parts, tagPart{content: text, isTag: false})
			}
			break
		}

		// Add text before tag
		if openIdx > 0 {
			parts = append(parts, tagPart{content: text[:openIdx], isTag: false})
		}

		// Find closing tag
		afterOpen := text[openIdx+len(openTag):]
		closeIdx := strings.Index(afterOpen, closeTag)
		if closeIdx == -1 {
			// No closing tag, treat rest as content
			parts = append(parts, tagPart{content: afterOpen, isTag: true})
			break
		}

		// Add tag content
		parts = append(parts, tagPart{content: afterOpen[:closeIdx], isTag: true})
		text = afterOpen[closeIdx+len(closeTag):]
	}

	return parts
}

// formatMessageContent formats the content field of a message
func formatMessageContent(content interface{}, maxWidth int, expand bool, valueStyle, toolStyle, toolDimStyle, dimStyle, tagStyle lipgloss.Style) []string {
	var lines []string

	switch c := content.(type) {
	case string:
		// Simple string content
		formatted := formatUserText(c, maxWidth, expand, valueStyle, tagStyle, dimStyle)
		lines = append(lines, formatted...)

	case []interface{}:
		// Array of content blocks
		for _, block := range c {
			if blockMap, ok := block.(map[string]interface{}); ok {
				blockType := "unknown"
				if t, ok := blockMap["type"].(string); ok {
					blockType = t
				}

				switch blockType {
				case "text":
					if text, ok := blockMap["text"].(string); ok {
						formatted := formatUserText(text, maxWidth, expand, valueStyle, tagStyle, dimStyle)
						lines = append(lines, formatted...)
					}

				case "tool_use":
					lines = append(lines, formatToolUseBlock(blockMap, maxWidth, expand, toolStyle, toolDimStyle, dimStyle)...)

				case "tool_result":
					lines = append(lines, formatToolResultBlock(blockMap, maxWidth, expand, toolStyle, toolDimStyle, dimStyle)...)

				default:
					lines = append(lines, dimStyle.Render(fmt.Sprintf("  [%s block]", blockType)))
				}
			}
		}
	}

	return lines
}

// formatUserText formats user text with special handling for system-reminder tags
func formatUserText(text string, maxWidth int, expand bool, valueStyle, tagStyle, dimStyle lipgloss.Style) []string {
	var lines []string

	if strings.Contains(text, "<system-reminder>") {
		parts := splitByTags(text, "system-reminder")
		for _, part := range parts {
			if part.isTag {
				if expand {
					lines = append(lines, tagStyle.Render("  ┌─ <system-reminder>"))
					wrapped := wrapTextToLines(part.content, maxWidth-4)
					for _, line := range wrapped {
						lines = append(lines, dimStyle.Render("  │ "+line))
					}
					lines = append(lines, tagStyle.Render("  └─ </system-reminder>"))
				} else {
					preview := strings.Join(strings.Fields(part.content), " ")
					if len(preview) > 50 {
						preview = preview[:47] + "..."
					}
					lines = append(lines, tagStyle.Render(fmt.Sprintf("  [system-reminder: %s]", preview)))
				}
			} else if strings.TrimSpace(part.content) != "" {
				wrapped := wrapTextToLines(strings.TrimSpace(part.content), maxWidth-2)
				for _, line := range wrapped {
					lines = append(lines, valueStyle.Render("  "+line))
				}
			}
		}
	} else {
		// Regular text - format nicely
		maxChars := maxWidth * 15
		if expand {
			maxChars = 1000000
		}
		displayText := text
		if len(displayText) > maxChars {
			displayText = displayText[:maxChars-3] + "..."
		}
		wrapped := wrapTextToLines(displayText, maxWidth-2)
		for _, line := range wrapped {
			lines = append(lines, valueStyle.Render("  "+line))
		}
	}

	return lines
}

// formatToolUseBlock formats a tool_use content block
func formatToolUseBlock(blockMap map[string]interface{}, maxWidth int, expand bool, toolStyle, toolDimStyle, dimStyle lipgloss.Style) []string {
	var lines []string

	name := "unknown"
	if n, ok := blockMap["name"].(string); ok {
		name = n
	}
	id := ""
	if i, ok := blockMap["id"].(string); ok {
		id = i
	}

	lines = append(lines, toolStyle.Render("  ┌─ Tool Call: ")+toolDimStyle.Render(name))
	if id != "" {
		lines = append(lines, dimStyle.Render("  │ ID: "+id))
	}

	if input, ok := blockMap["input"]; ok {
		lines = append(lines, dimStyle.Render("  │ Input:"))
		inputJSON, _ := json.MarshalIndent(input, "  │   ", "  ")
		inputStr := string(inputJSON)

		maxChars := maxWidth * 5
		if expand {
			maxChars = 1000000
		}
		if len(inputStr) > maxChars {
			inputStr = inputStr[:maxChars-3] + "..."
		}

		inputLines := strings.Split(inputStr, "\n")
		for i, line := range inputLines {
			if len(line) > maxWidth-4 && !expand {
				line = line[:maxWidth-7] + "..."
			}
			// First line doesn't get the prefix from MarshalIndent, add it manually
			if i == 0 {
				lines = append(lines, dimStyle.Render("  │   "+line))
			} else {
				lines = append(lines, dimStyle.Render(line))
			}
		}
	}
	lines = append(lines, toolStyle.Render("  └─────"))

	return lines
}

// formatToolResultBlock formats a tool_result content block
func formatToolResultBlock(blockMap map[string]interface{}, maxWidth int, expand bool, toolStyle, toolDimStyle, dimStyle lipgloss.Style) []string {
	var lines []string

	toolUseID := ""
	if id, ok := blockMap["tool_use_id"].(string); ok {
		toolUseID = id
	}

	lines = append(lines, toolStyle.Render("  ┌─ Tool Result"))
	if toolUseID != "" {
		lines = append(lines, dimStyle.Render("  │ For: "+toolUseID))
	}

	// Content can be string or array
	if result, ok := blockMap["content"].(string); ok {
		maxChars := maxWidth * 5
		if expand {
			maxChars = 1000000
		}
		if len(result) > maxChars {
			result = result[:maxChars-3] + "..."
		}
		wrapped := wrapTextToLines(result, maxWidth-4)
		for _, line := range wrapped {
			lines = append(lines, dimStyle.Render("  │ "+line))
		}
	} else if contentArr, ok := blockMap["content"].([]interface{}); ok {
		for _, item := range contentArr {
			if itemMap, ok := item.(map[string]interface{}); ok {
				if text, ok := itemMap["text"].(string); ok {
					maxChars := maxWidth * 5
					if expand {
						maxChars = 1000000
					}
					if len(text) > maxChars {
						text = text[:maxChars-3] + "..."
					}
					wrapped := wrapTextToLines(text, maxWidth-4)
					for _, line := range wrapped {
						lines = append(lines, dimStyle.Render("  │ "+line))
					}
				}
			}
		}
	}
	lines = append(lines, toolStyle.Render("  └─────"))

	return lines
}

// wrapTextToLines wraps text to specified width, returning array of lines
func wrapTextToLines(text string, width int) []string {
	if width <= 0 {
		width = 40
	}

	var lines []string
	// Split by newlines first to preserve intentional line breaks
	paragraphs := strings.Split(text, "\n")

	for _, para := range paragraphs {
		if para == "" {
			lines = append(lines, "")
			continue
		}

		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}

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
	}

	return lines
}

// formatResponseContent parses and formats an Anthropic API response
func formatResponseContent(content string, maxWidth int, expand bool) []string {
	labelStyle := lipgloss.NewStyle().Foreground(accentColor).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(brightColor)
	dimStyle := lipgloss.NewStyle().Foreground(dimColor)
	toolStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF9F43"))

	// Set truncation limits based on expand mode
	textMaxChars := maxWidth * 20 // Allow longer text in responses
	toolInputMax := maxWidth * 10
	if expand {
		textMaxChars = 1000000  // Effectively unlimited
		toolInputMax = 1000000 // Effectively unlimited
	}

	var lines []string

	// Try to parse as JSON
	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		// Not valid JSON, show as plain text (common for streamed responses)
		lines = append(lines, labelStyle.Render("Response:"))
		text := content
		if !expand && len(text) > textMaxChars {
			text = text[:textMaxChars-3] + "..."
		}
		lines = append(lines, valueStyle.Render(wrapTextSimple(text, maxWidth)))
		return lines
	}

	// Full-width response header
	respHeaderText := "─── Response "
	respRemaining := maxWidth - lipgloss.Width(respHeaderText) - 2
	if respRemaining < 0 {
		respRemaining = 0
	}
	lines = append(lines, labelStyle.Render("╭"+respHeaderText+strings.Repeat("─", respRemaining)+"╮"))
	lines = append(lines, "")

	// ID
	if id, ok := resp["id"].(string); ok {
		lines = append(lines, labelStyle.Render("ID: ")+dimStyle.Render(id))
	}

	// Type
	if respType, ok := resp["type"].(string); ok {
		lines = append(lines, labelStyle.Render("Type: ")+dimStyle.Render(respType))
	}

	// Model
	if model, ok := resp["model"].(string); ok {
		lines = append(lines, labelStyle.Render("Model: ")+valueStyle.Render(model))
	}

	// Stop reason
	if stopReason, ok := resp["stop_reason"].(string); ok {
		reasonStyle := dimStyle
		if stopReason == "end_turn" {
			reasonStyle = lipgloss.NewStyle().Foreground(successColor)
		} else if stopReason == "tool_use" {
			reasonStyle = toolStyle
		} else if stopReason == "max_tokens" {
			reasonStyle = lipgloss.NewStyle().Foreground(warnColor)
		}
		lines = append(lines, labelStyle.Render("Stop Reason: ")+reasonStyle.Render(stopReason))
	}

	// Usage
	if usage, ok := resp["usage"].(map[string]interface{}); ok {
		var usageParts []string
		if input, ok := usage["input_tokens"].(float64); ok {
			usageParts = append(usageParts, fmt.Sprintf("in:%d", int(input)))
		}
		if output, ok := usage["output_tokens"].(float64); ok {
			usageParts = append(usageParts, fmt.Sprintf("out:%d", int(output)))
		}
		if len(usageParts) > 0 {
			lines = append(lines, labelStyle.Render("Tokens: ")+dimStyle.Render(strings.Join(usageParts, " ")))
		}
	}

	lines = append(lines, "")

	// Content blocks
	if contentArr, ok := resp["content"].([]interface{}); ok && len(contentArr) > 0 {
		contHeaderText := "─── Content "
		contRemaining := maxWidth - lipgloss.Width(contHeaderText) - 2
		if contRemaining < 0 {
			contRemaining = 0
		}
		lines = append(lines, labelStyle.Render("╭"+contHeaderText+strings.Repeat("─", contRemaining)+"╮"))

		for _, block := range contentArr {
			if blockMap, ok := block.(map[string]interface{}); ok {
				blockType := "unknown"
				if t, ok := blockMap["type"].(string); ok {
					blockType = t
				}

				switch blockType {
				case "text":
					if text, ok := blockMap["text"].(string); ok {
						// Truncate if not in expand mode
						if !expand && len(text) > textMaxChars {
							text = text[:textMaxChars-3] + "..."
						}
						lines = append(lines, "")
						wrapped := wrapTextSimple(text, maxWidth)
						for _, line := range strings.Split(wrapped, "\n") {
							lines = append(lines, valueStyle.Render(line))
						}
					}
				case "tool_use":
					name := "unknown"
					if n, ok := blockMap["name"].(string); ok {
						name = n
					}
					id := ""
					if i, ok := blockMap["id"].(string); ok {
						id = i
					}
					lines = append(lines, "")
					lines = append(lines, toolStyle.Render("┌─ Tool Call: "+name))
					if id != "" {
						lines = append(lines, dimStyle.Render("│ ID: "+id))
					}
					if input, ok := blockMap["input"]; ok {
						inputJSON, _ := json.MarshalIndent(input, "│ ", "  ")
						inputStr := string(inputJSON)
						// Truncate if not in expand mode
						if !expand && len(inputStr) > toolInputMax {
							inputStr = inputStr[:toolInputMax-3] + "..."
						}
						// Show input nicely formatted
						lines = append(lines, dimStyle.Render("│ Input:"))
						for _, line := range strings.Split(inputStr, "\n") {
							if !expand && len(line) > maxWidth-2 {
								line = line[:maxWidth-5] + "..."
							}
							lines = append(lines, dimStyle.Render(line))
						}
					}
					lines = append(lines, toolStyle.Render("└─────────────"))
				default:
					lines = append(lines, dimStyle.Render(fmt.Sprintf("[%s block]", blockType)))
				}
			}
		}
	}

	// Error (if present)
	if errObj, ok := resp["error"].(map[string]interface{}); ok {
		lines = append(lines, "")
		errHeaderText := "─── ERROR "
		errRemaining := maxWidth - lipgloss.Width(errHeaderText) - 2
		if errRemaining < 0 {
			errRemaining = 0
		}
		lines = append(lines, lipgloss.NewStyle().Foreground(errorColor).Bold(true).Render("╭"+errHeaderText+strings.Repeat("─", errRemaining)+"╮"))
		if errType, ok := errObj["type"].(string); ok {
			lines = append(lines, lipgloss.NewStyle().Foreground(errorColor).Render("Type: "+errType))
		}
		if errMsg, ok := errObj["message"].(string); ok {
			lines = append(lines, lipgloss.NewStyle().Foreground(errorColor).Render("Message: "+errMsg))
		}
	}

	return lines
}

// truncateText truncates text to maxChars and wraps to width
func truncateText(text string, maxChars int, width int) string {
	if len(text) > maxChars {
		text = text[:maxChars-3] + "..."
	}
	return wrapTextSimple(text, width)
}

// wrapText wraps text to fit within a given width
func wrapText(text string, width int) string {
	if width <= 0 {
		width = 40
	}

	var lines []string
	for len(text) > width {
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

// formatContent formats content for display - detects JSON and formats it nicely
func formatContent(content string, maxWidth int) string {
	if maxWidth < 30 {
		maxWidth = 30
	}

	// Trim whitespace
	content = strings.TrimSpace(content)

	// Check if it looks like JSON
	if (strings.HasPrefix(content, "{") && strings.HasSuffix(content, "}")) ||
		(strings.HasPrefix(content, "[") && strings.HasSuffix(content, "]")) {
		// Try to parse as JSON
		var parsed interface{}
		if err := json.Unmarshal([]byte(content), &parsed); err == nil {
			// Successfully parsed - format it nicely
			return formatJSONValue(parsed, 0, maxWidth)
		}
	}

	// Not JSON or failed to parse - wrap as plain text
	return wrapText(content, maxWidth)
}

// formatJSONValue formats a JSON value with syntax highlighting
func formatJSONValue(v interface{}, indent int, maxWidth int) string {
	// Styles for syntax highlighting
	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#7D56F4")).Bold(true)
	stringStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6BFF6B"))
	numberStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFAA00"))
	boolStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B"))
	nullStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
	bracketStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A0A0A0"))

	indentStr := strings.Repeat("  ", indent)
	nextIndent := strings.Repeat("  ", indent+1)

	switch val := v.(type) {
	case nil:
		return nullStyle.Render("null")

	case bool:
		if val {
			return boolStyle.Render("true")
		}
		return boolStyle.Render("false")

	case float64:
		// Check if it's an integer
		if val == float64(int64(val)) {
			return numberStyle.Render(fmt.Sprintf("%d", int64(val)))
		}
		return numberStyle.Render(fmt.Sprintf("%g", val))

	case string:
		// For long strings, wrap them nicely
		if len(val) > maxWidth-indent*2-4 {
			return formatLongString(val, indent, maxWidth, stringStyle)
		}
		return stringStyle.Render(fmt.Sprintf("%q", val))

	case []interface{}:
		if len(val) == 0 {
			return bracketStyle.Render("[]")
		}

		var lines []string
		lines = append(lines, bracketStyle.Render("["))
		for i, item := range val {
			itemStr := formatJSONValue(item, indent+1, maxWidth)
			comma := ""
			if i < len(val)-1 {
				comma = bracketStyle.Render(",")
			}
			// Handle multi-line items
			itemLines := strings.Split(itemStr, "\n")
			for j, line := range itemLines {
				if j == 0 {
					lines = append(lines, nextIndent+line)
				} else {
					lines = append(lines, line)
				}
			}
			// Add comma to last line of item
			if comma != "" && len(lines) > 0 {
				lines[len(lines)-1] = lines[len(lines)-1] + comma
			}
		}
		lines = append(lines, indentStr+bracketStyle.Render("]"))
		return strings.Join(lines, "\n")

	case map[string]interface{}:
		if len(val) == 0 {
			return bracketStyle.Render("{}")
		}

		var lines []string
		lines = append(lines, bracketStyle.Render("{"))

		// Get keys and sort them for consistent output
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}

		for i, k := range keys {
			keyStr := keyStyle.Render(fmt.Sprintf("%q", k))
			valueStr := formatJSONValue(val[k], indent+1, maxWidth)
			comma := ""
			if i < len(keys)-1 {
				comma = bracketStyle.Render(",")
			}

			// Handle multi-line values
			valueLines := strings.Split(valueStr, "\n")
			if len(valueLines) == 1 {
				// Single line value
				lines = append(lines, nextIndent+keyStr+bracketStyle.Render(": ")+valueLines[0]+comma)
			} else {
				// Multi-line value
				lines = append(lines, nextIndent+keyStr+bracketStyle.Render(": ")+valueLines[0])
				for j := 1; j < len(valueLines)-1; j++ {
					lines = append(lines, valueLines[j])
				}
				lines = append(lines, valueLines[len(valueLines)-1]+comma)
			}
		}
		lines = append(lines, indentStr+bracketStyle.Render("}"))
		return strings.Join(lines, "\n")

	default:
		return fmt.Sprintf("%v", val)
	}
}

// formatLongString formats a long string value with wrapping
func formatLongString(s string, indent int, maxWidth int, style lipgloss.Style) string {
	// Calculate available width for string content
	availWidth := maxWidth - indent*2 - 4
	if availWidth < 20 {
		availWidth = 20
	}

	// If it contains newlines, handle them specially
	if strings.Contains(s, "\n") || strings.Contains(s, "\\n") {
		// Replace escaped newlines with actual newlines for display
		displayStr := strings.ReplaceAll(s, "\\n", "\n")
		lines := strings.Split(displayStr, "\n")

		var result []string
		indentStr := strings.Repeat("  ", indent+1)

		for i, line := range lines {
			if i == 0 {
				result = append(result, style.Render("\""))
			}
			// Wrap long lines
			wrapped := wrapTextSimple(line, availWidth)
			for _, wl := range strings.Split(wrapped, "\n") {
				if i == 0 && len(result) == 1 {
					result[0] = result[0] + style.Render(wl)
				} else {
					result = append(result, indentStr+style.Render(wl))
				}
			}
		}
		if len(result) > 0 {
			result[len(result)-1] = result[len(result)-1] + style.Render("\"")
		}
		return strings.Join(result, "\n")
	}

	// Simple long string - just wrap it
	wrapped := wrapTextSimple(s, availWidth)
	lines := strings.Split(wrapped, "\n")
	if len(lines) == 1 {
		return style.Render(fmt.Sprintf("%q", s))
	}

	var result []string
	indentStr := strings.Repeat("  ", indent+1)
	for i, line := range lines {
		if i == 0 {
			result = append(result, style.Render("\""+line))
		} else if i == len(lines)-1 {
			result = append(result, indentStr+style.Render(line+"\""))
		} else {
			result = append(result, indentStr+style.Render(line))
		}
	}
	return strings.Join(result, "\n")
}

// wrapTextSimple wraps text at word boundaries
func wrapTextSimple(text string, width int) string {
	if width <= 0 || len(text) <= width {
		return text
	}

	var lines []string
	words := strings.Fields(text)
	if len(words) == 0 {
		return text
	}

	currentLine := words[0]
	for _, word := range words[1:] {
		if len(currentLine)+1+len(word) <= width {
			currentLine += " " + word
		} else {
			lines = append(lines, currentLine)
			currentLine = word
		}
	}
	if currentLine != "" {
		lines = append(lines, currentLine)
	}

	// If no word breaks, force break at width
	if len(lines) == 0 {
		for len(text) > width {
			lines = append(lines, text[:width])
			text = text[width:]
		}
		if len(text) > 0 {
			lines = append(lines, text)
		}
	}

	return strings.Join(lines, "\n")
}

// formatBodySize formats body size into a compact human-readable string
func formatBodySize(bytes int) string {
	const (
		KB = 1024
		MB = KB * 1024
	)

	switch {
	case bytes >= MB:
		return fmt.Sprintf("%.1fM", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1fK", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

// View renders the split-pane log viewer using composable views pattern
func (m LogViewerModel) View() string {
	// Calculate panel heights (account for borders when focused)
	listHeight := m.height
	detailHeight := m.height

	// Build list panel content
	listContent := m.renderListPanel(listHeight - 2)

	// Build detail panel content
	detailContent := m.renderDetailPanel(detailHeight - 2)

	// Apply styles based on focus
	var listPanel, detailPanel string

	// Width() in lipgloss includes padding but not border
	// Panel style has border(2) so: Width(w) + 2 = total rendered width
	// For panel to render as m.listWidth: Width(m.listWidth - 2)
	listW := m.listWidth - 2
	listH := listHeight - 2
	detailW := m.detailWidth - 2
	detailH := detailHeight - 2

	if m.focused && m.panelFocus == FocusList {
		listPanel = focusedPanelStyle.
			Width(listW).
			Height(listH).
			Render(listContent)
		detailPanel = panelStyle.
			Width(detailW).
			Height(detailH).
			Render(detailContent)
	} else if m.focused && m.panelFocus == FocusDetail {
		listPanel = panelStyle.
			Width(listW).
			Height(listH).
			Render(listContent)
		detailPanel = focusedPanelStyle.
			Width(detailW).
			Height(detailH).
			Render(detailContent)
	} else {
		// Not focused - hidden borders on both
		listPanel = panelStyle.
			Width(listW).
			Height(listH).
			Render(listContent)
		detailPanel = panelStyle.
			Width(detailW).
			Height(detailH).
			Render(detailContent)
	}

	// Join panels horizontally with 1-char gap
	return lipgloss.JoinHorizontal(lipgloss.Top, listPanel, " ", detailPanel)
}

// renderListPanel renders the log entries list with fixed columns
func (m LogViewerModel) renderListPanel(height int) string {
	if height < 1 {
		height = 1
	}

	cfg := config.Get()
	dimStyle := lipgloss.NewStyle().Foreground(dimColor)
	selectedStyle := lipgloss.NewStyle().
		Background(selectedBg).
		Foreground(brightColor)
	headerStyle := lipgloss.NewStyle().
		Foreground(accentColor).
		Bold(true)
	// Pagination indicator style (italic like Claude Code)
	paginationStyle := lipgloss.NewStyle().Foreground(dimColor).Italic(true)

	var lines []string

	// Available content width (account for border and padding)
	contentWidth := m.listWidth - 4

	// Session tabs (only show if there are multiple sessions)
	if len(m.sessions) > 1 {
		var tabs []string
		for i, session := range m.sessions {
			var tabLabel string
			if session == "" {
				tabLabel = "All"
			} else {
				// Use friendly name if available, otherwise session ID
				displayName := m.getSessionDisplayName(session)
				// Truncate long names
				if len(displayName) > 12 {
					tabLabel = displayName[:10] + ".."
				} else {
					tabLabel = displayName
				}
			}

			// Style based on selection
			var tabStyled string
			if i == m.selectedSession {
				// Selected tab: highlighted with brackets
				if session == "" {
					tabStyled = headerStyle.Render("[" + tabLabel + "]")
				} else if colorIdx, ok := m.sessionColorMap[session]; ok {
					tabStyled = lipgloss.NewStyle().Foreground(sessionColors[colorIdx]).Bold(true).Render("[" + tabLabel + "]")
				} else {
					tabStyled = headerStyle.Render("[" + tabLabel + "]")
				}
			} else {
				// Unselected tab: just the label, colored if session
				if session == "" {
					tabStyled = dimStyle.Render(" " + tabLabel + " ")
				} else if colorIdx, ok := m.sessionColorMap[session]; ok {
					tabStyled = lipgloss.NewStyle().Foreground(sessionColors[colorIdx]).Render(" " + tabLabel + " ")
				} else {
					tabStyled = dimStyle.Render(" " + tabLabel + " ")
				}
			}
			tabs = append(tabs, tabStyled)
		}

		tabsLine := strings.Join(tabs, dimStyle.Render("│"))
		// Add left/right hint
		tabsLine = tabsLine + dimStyle.Render("  ←/→")
		lines = append(lines, tabsLine)
		lines = append(lines, dimStyle.Render(strings.Repeat("─", contentWidth)))
		height -= 2 // Reduce available height for entries
	}

	// m.entries is already filtered by session and ShowSystemMessages in applySessionFilter()

	// Always reserve 2 lines for pagination indicators (to match ensureVisible calculation)
	// This prevents the mismatch where selection appears too high when at list boundaries
	entriesHeight := height - 2
	if entriesHeight < 1 {
		entriesHeight = 1
	}

	// Determine pagination state
	hasMoreAbove := m.listOffset > 0
	hasMoreBelow := m.listOffset+entriesHeight < len(m.entries)

	if len(m.entries) == 0 {
		lines = append(lines, dimStyle.Render("Waiting for requests..."))
	} else {
		// Add "more above" indicator or empty line (always reserve the space)
		if hasMoreAbove {
			lines = append(lines, paginationStyle.Render("↑ more above"))
		} else {
			lines = append(lines, "") // Reserve space even when not showing
		}

		// Render visible entries
		visibleCount := 0
		for i := m.listOffset; i < len(m.entries) && visibleCount < entriesHeight; i++ {
			entry := m.entries[i]
			isSelected := i == m.selectedIndex

			// Build line with fixed columns
			line := m.renderEntryLine(entry, contentWidth, cfg, isSelected)

			if isSelected {
				// Pad to full width for selection highlight
				padWidth := contentWidth - lipgloss.Width(line)
				if padWidth < 0 {
					padWidth = 0
				}
				padded := line + strings.Repeat(" ", padWidth)
				lines = append(lines, selectedStyle.Render(padded))
			} else {
				lines = append(lines, line)
			}
			visibleCount++
		}

		// Add "more below" indicator or empty line (always reserve the space)
		if hasMoreBelow {
			lines = append(lines, paginationStyle.Render("↓ more below"))
		} else {
			lines = append(lines, "") // Reserve space even when not showing
		}
	}

	// Pad to full height
	for len(lines) < height {
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// renderEntryLine renders a single log entry with fixed columns
// Column layout: │ TIME T # STA SIZE DUR PREVIEW
func (m LogViewerModel) renderEntryLine(entry logger.LogEntry, contentWidth int, cfg *config.Config, selected bool) string {
	// When selected, use bright color for all text so selection background shows through
	var dimStyle, normalTextStyle lipgloss.Style
	if selected {
		dimStyle = lipgloss.NewStyle().Foreground(brightColor)
		normalTextStyle = lipgloss.NewStyle().Foreground(brightColor)
	} else {
		dimStyle = lipgloss.NewStyle().Foreground(dimColor)
		normalTextStyle = lipgloss.NewStyle().Foreground(normalColor)
	}

	// Session color indicator - colored bar at start of line (1 char)
	sessionIndicator := "│"
	var sessionColor lipgloss.Color
	if selected {
		sessionColor = brightColor
	} else if entry.SessionID != "" {
		if colorIdx, ok := m.sessionColorMap[entry.SessionID]; ok {
			sessionColor = sessionColors[colorIdx]
		} else {
			sessionColor = dimColor
		}
	} else {
		sessionColor = normalColor
	}

	// Type indicator (1 char)
	typeChar := "○"
	var typeColor lipgloss.Color
	if selected {
		typeColor = brightColor
	} else {
		typeColor = dimColor
		switch entry.Type {
		case logger.LogTypeReq:
			typeChar = "▶"
			typeColor = successColor
		case logger.LogTypeRes:
			typeChar = "◀"
			typeColor = infoColor
		case logger.LogTypeErr:
			typeChar = "✖"
			typeColor = errorColor
		case logger.LogTypeInf:
			typeChar = "●"
			typeColor = warnColor
		}
	}
	// Set type char even when selected
	switch entry.Type {
	case logger.LogTypeReq:
		typeChar = "▶"
	case logger.LogTypeRes:
		typeChar = "◀"
	case logger.LogTypeErr:
		typeChar = "✖"
	case logger.LogTypeInf:
		typeChar = "●"
	}

	// TIME column (8 chars)
	timeStr := entry.Timestamp.Format("15:04:05")

	// Build columns based on settings
	var columns []string

	// Session bar + space + time + space + type = "│ HH:MM:SS T"
	columns = append(columns, fmt.Sprintf("%s %s %s",
		lipgloss.NewStyle().Foreground(sessionColor).Render(sessionIndicator),
		dimStyle.Render(timeStr),
		lipgloss.NewStyle().Foreground(typeColor).Render(typeChar),
	))

	// Track width used for preview calculation
	usedWidth := 12 // "│ " (2) + "HH:MM:SS" (8) + " " (1) + "T" (1)

	// # column - request number (3 chars: #XX) - only for REQ/RES
	if cfg.Display.ShowRequestNumber {
		var numStr string
		if (entry.Type == logger.LogTypeReq || entry.Type == logger.LogTypeRes) && entry.SeqNum > 0 {
			numStr = fmt.Sprintf("#%02d", entry.SeqNum%100) // Max 99, wraps
		} else {
			numStr = "---"
		}
		columns = append(columns, dimStyle.Render(numStr))
		usedWidth += 4 // " #XX"
	}

	// STA column - status (3 chars)
	if cfg.Display.ShowStatusInList {
		var staStr string
		var staStyle lipgloss.Style
		if selected {
			staStyle = normalTextStyle
		}
		switch entry.Type {
		case logger.LogTypeReq:
			// REQ: show OK or error status
			if entry.Status == 0 {
				staStr = " OK"
				if !selected {
					staStyle = lipgloss.NewStyle().Foreground(successColor)
				}
			} else {
				staStr = fmt.Sprintf("%3d", entry.Status)
				if !selected {
					staStyle = lipgloss.NewStyle().Foreground(warnColor)
				}
			}
		case logger.LogTypeRes:
			// RES: show HTTP status code
			staStr = fmt.Sprintf("%3d", entry.Status)
			if !selected {
				if entry.Status >= 500 {
					staStyle = lipgloss.NewStyle().Foreground(errorColor)
				} else if entry.Status >= 400 {
					staStyle = lipgloss.NewStyle().Foreground(warnColor)
				} else {
					staStyle = lipgloss.NewStyle().Foreground(successColor)
				}
			}
		case logger.LogTypeErr:
			staStr = "ERR"
			if !selected {
				staStyle = lipgloss.NewStyle().Foreground(errorColor)
			}
		default:
			staStr = "---"
			staStyle = dimStyle
		}
		columns = append(columns, staStyle.Render(staStr))
		usedWidth += 4 // " XXX"
	}

	// SIZE column (4 chars: X.XK or XXXB)
	if cfg.Display.ShowBodySize {
		var sizeStr string
		if (entry.Type == logger.LogTypeReq || entry.Type == logger.LogTypeRes) && entry.BodySize > 0 {
			sizeStr = fmt.Sprintf("%4s", formatBodySize(entry.BodySize))
		} else {
			sizeStr = " ---"
		}
		columns = append(columns, dimStyle.Render(sizeStr))
		usedWidth += 5 // " XXXX"
	}

	// DUR column - duration (5 chars: XXXms or X.Xs)
	if cfg.Display.ShowDurationInList {
		var durStr string
		if (entry.Type == logger.LogTypeReq || entry.Type == logger.LogTypeRes) && entry.Duration > 0 {
			durStr = formatDuration(entry.Duration)
		} else {
			durStr = "  ---"
		}
		columns = append(columns, dimStyle.Render(durStr))
		usedWidth += 6 // " XXXXX"
	}

	// PREVIEW column (rest of line)
	maxPreview := contentWidth - usedWidth - 1 // -1 for space before preview
	if maxPreview < 5 {
		maxPreview = 5
	}
	preview := entry.Preview
	if len(preview) > maxPreview {
		preview = preview[:maxPreview-3] + "..."
	}
	if preview != "" {
		columns = append(columns, normalTextStyle.Render(preview))
	}

	return strings.Join(columns, " ")
}

// formatDuration formats duration into a 5-char string (XXXms or X.Xs or XXs)
func formatDuration(d time.Duration) string {
	if d < time.Second {
		ms := d.Milliseconds()
		return fmt.Sprintf("%3dms", ms)
	} else if d < 10*time.Second {
		return fmt.Sprintf("%4.1fs", d.Seconds())
	} else {
		return fmt.Sprintf("%4ds", int(d.Seconds()))
	}
}

// renderDetailPanel renders the detail view
func (m LogViewerModel) renderDetailPanel(height int) string {
	if height < 1 {
		height = 1
	}

	originalHeight := height // Save for padding at the end

	// Pagination indicator style (italic like Claude Code)
	paginationStyle := lipgloss.NewStyle().Foreground(dimColor).Italic(true)

	var resultLines []string

	// Check if there's content above/below the viewport
	totalLines := m.detailView.TotalLineCount()
	hasMoreAbove := m.detailView.YOffset > 0
	hasMoreBelow := m.detailView.YOffset+m.detailView.Height < totalLines

	// Calculate scroll position as percentage
	scrollPercent := 0
	if totalLines > m.detailView.Height {
		scrollPercent = (m.detailView.YOffset * 100) / (totalLines - m.detailView.Height)
		if scrollPercent > 100 {
			scrollPercent = 100
		}
	}

	// Calculate available content space
	contentHeight := height
	if hasMoreAbove {
		contentHeight-- // Reserve line for "more above"
	}
	if hasMoreBelow {
		contentHeight-- // Reserve line for "more below"
	}
	if contentHeight < 1 {
		contentHeight = 1
	}

	// Add "more above" indicator if needed
	if hasMoreAbove {
		resultLines = append(resultLines, paginationStyle.Render(fmt.Sprintf("↑ more above (%d%%)", scrollPercent)))
	}

	// Add content lines
	content := m.detailView.View()
	lines := strings.Split(content, "\n")
	for i := 0; i < contentHeight && i < len(lines); i++ {
		resultLines = append(resultLines, lines[i])
	}

	// Add "more below" indicator if needed (show remaining percentage)
	if hasMoreBelow {
		resultLines = append(resultLines, paginationStyle.Render(fmt.Sprintf("↓ more below (%d%%)", scrollPercent)))
	}

	// Pad to full height
	for len(resultLines) < originalHeight {
		resultLines = append(resultLines, "")
	}

	return strings.Join(resultLines, "\n")
}

// SetFocused sets the focus state
func (m *LogViewerModel) SetFocused(focused bool) {
	m.focused = focused
}

// IsFocused returns the focus state
func (m LogViewerModel) IsFocused() bool {
	return m.focused
}

// GetPanelFocus returns the current panel focus (list or detail)
func (m LogViewerModel) GetPanelFocus() PanelFocus {
	return m.panelFocus
}

// saveViewModeToConfig saves the current view mode to config (for "last" setting)
func (m *LogViewerModel) saveViewModeToConfig() {
	cfg := config.Get()
	switch m.viewMode {
	case ViewModeParsed:
		cfg.Filter.LastViewMode = "parsed"
	case ViewModeJSON:
		cfg.Filter.LastViewMode = "json"
	case ViewModeRaw:
		cfg.Filter.LastViewMode = "raw"
	}
	cfg.Save()
}

// saveExpandModeToConfig saves the current expand mode to config (for "last" setting)
func (m *LogViewerModel) saveExpandModeToConfig() {
	cfg := config.Get()
	if m.expandContent {
		cfg.Filter.LastExpandMode = "expanded"
	} else {
		cfg.Filter.LastExpandMode = "compact"
	}
	cfg.Save()
}

// SetSize updates the dimensions
func (m *LogViewerModel) SetSize(width, height int) {
	cfg := config.Get()
	m.width = width
	m.height = height
	m.listWidth = width * cfg.Display.ListWidthPercent / 100
	m.detailWidth = width - m.listWidth - 1 // minus 1 for gap space

	// Calculate viewport width to match panel content area
	// Panel: Width(w) + border(2) = rendered width, so w = rendered - 2
	// Inside w: padding(2) is subtracted, so text area = w - 2 = rendered - 4
	vpWidth := m.detailWidth - 4
	if vpWidth < 10 {
		vpWidth = 10
	}
	vpHeight := height - 4
	if vpHeight < 3 {
		vpHeight = 3
	}
	m.detailView.Width = vpWidth
	m.detailView.Height = vpHeight
	m.updateDetailContent()
}

// AddEntry adds a log entry
func (m *LogViewerModel) AddEntry(entry logger.LogEntry) {
	cfg := config.Get()
	// Add to all entries
	m.allEntries = append(m.allEntries, entry)
	if len(m.allEntries) > cfg.Logging.MaxEntries {
		excess := len(m.allEntries) - cfg.Logging.MaxEntries
		m.allEntries = m.allEntries[excess:]
	}

	// Update sessions list if this is a new session
	if entry.SessionID != "" {
		if _, exists := m.sessionColorMap[entry.SessionID]; !exists {
			// Assign a color to this session
			colorIndex := len(m.sessionColorMap) % len(sessionColors)
			m.sessionColorMap[entry.SessionID] = colorIndex
			m.sessions = append(m.sessions, entry.SessionID)
		}

		// Try to extract working directory as friendly session name
		m.extractSessionName(entry)
		// Try to extract title from response entries
		m.extractSessionTitle(entry)
	}

	// Re-filter entries based on current session
	m.applySessionFilter()

	// Auto-select new entry if at bottom and entry matches filter
	if m.selectedSession == 0 || entry.SessionID == m.sessions[m.selectedSession] {
		if m.selectedIndex == len(m.entries)-2 || len(m.entries) == 1 {
			m.selectedIndex = len(m.entries) - 1
			m.ensureVisible()
		}
	}
	m.updateDetailContent()
}

// Clear removes all entries
func (m *LogViewerModel) Clear() {
	m.allEntries = m.allEntries[:0]
	m.entries = m.entries[:0]
	m.sessions = []string{""}
	m.selectedSession = 0
	m.selectedIndex = 0
	m.listOffset = 0
	m.sessionColorMap = make(map[string]int)
	m.sessionNameMap = make(map[string]string)
	m.sessionMetadataMap = make(map[string]*SessionMetadata)
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

// SetEntries sets the log entries (for loading from file)
func (m *LogViewerModel) SetEntries(entries []logger.LogEntry) {
	m.allEntries = make([]logger.LogEntry, len(entries))
	copy(m.allEntries, entries)

	// Rebuild sessions list, color map, name map, and metadata map
	m.sessions = []string{""}
	m.sessionColorMap = make(map[string]int)
	m.sessionNameMap = make(map[string]string)
	m.sessionMetadataMap = make(map[string]*SessionMetadata)
	for _, entry := range entries {
		if entry.SessionID != "" {
			if _, exists := m.sessionColorMap[entry.SessionID]; !exists {
				colorIndex := len(m.sessionColorMap) % len(sessionColors)
				m.sessionColorMap[entry.SessionID] = colorIndex
				m.sessions = append(m.sessions, entry.SessionID)
			}
			// Try to extract working directory as friendly session name
			m.extractSessionName(entry)
			// Try to extract title from response entries
			m.extractSessionTitle(entry)
		}
	}

	// Apply filter
	m.selectedSession = 0
	m.applySessionFilter()
	m.selectedIndex = 0
	m.listOffset = 0
	m.updateDetailContent()
}

// TotalEntryCount returns the total number of log entries (unfiltered)
func (m LogViewerModel) TotalEntryCount() int {
	return len(m.allEntries)
}

// ClipboardResultMsg signals clipboard operation result
type ClipboardResultMsg struct {
	Success bool
	Error   error
}

// EditorResultMsg signals editor operation result
type EditorResultMsg struct {
	Success bool
	Error   error
}

// getExportContent returns the content to export based on current view mode
func (m *LogViewerModel) getExportContent() string {
	if len(m.entries) == 0 || m.selectedIndex < 0 || m.selectedIndex >= len(m.entries) {
		return ""
	}

	entry := m.entries[m.selectedIndex]
	content := entry.Body
	if content == "" {
		content = entry.Preview
	}

	switch m.viewMode {
	case ViewModeRaw:
		return content
	case ViewModeJSON:
		// Try to format as JSON
		var parsed interface{}
		if err := json.Unmarshal([]byte(content), &parsed); err == nil {
			formatted, err := json.MarshalIndent(parsed, "", "  ")
			if err == nil {
				return string(formatted)
			}
		}
		return content
	case ViewModeParsed:
		// For parsed view, export the structured content as readable text
		// This strips ANSI color codes and gives clean text
		lines := m.getExportParsedContent(entry, content)
		return strings.Join(lines, "\n")
	}
	return content
}

// getExportParsedContent returns clean text for parsed view export
func (m *LogViewerModel) getExportParsedContent(entry logger.LogEntry, content string) []string {
	var lines []string

	// Header
	lines = append(lines, fmt.Sprintf("=== %s [%s] ===", entry.Timestamp.Format("2006-01-02 15:04:05"), entry.Type.String()))
	if entry.SessionID != "" {
		lines = append(lines, fmt.Sprintf("Session: %s", entry.SessionID))
	}
	if entry.RequestID != "" {
		lines = append(lines, fmt.Sprintf("Request: %s", entry.RequestID))
	}
	if entry.Method != "" {
		lines = append(lines, fmt.Sprintf("Method: %s", entry.Method))
	}
	if entry.Path != "" {
		lines = append(lines, fmt.Sprintf("Path: %s", entry.Path))
	}
	if entry.Model != "" {
		lines = append(lines, fmt.Sprintf("Model: %s", entry.Model))
	}
	if entry.Status != 0 {
		lines = append(lines, fmt.Sprintf("Status: %d", entry.Status))
	}
	if entry.Duration > 0 {
		lines = append(lines, fmt.Sprintf("Duration: %s", entry.Duration.String()))
	}
	lines = append(lines, "")

	// Try to parse as JSON for structured output
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(content), &parsed); err == nil {
		formatted, _ := json.MarshalIndent(parsed, "", "  ")
		lines = append(lines, string(formatted))
	} else {
		lines = append(lines, content)
	}

	return lines
}

// extractSessionName tries to extract a friendly session name from the entry body
// Looks for working directory patterns and extracts the folder name
func (m *LogViewerModel) extractSessionName(entry logger.LogEntry) {
	// Ensure metadata exists for this session
	if _, exists := m.sessionMetadataMap[entry.SessionID]; !exists {
		m.sessionMetadataMap[entry.SessionID] = &SessionMetadata{
			SessionID: entry.SessionID,
		}
	}
	meta := m.sessionMetadataMap[entry.SessionID]

	// Only look for working directory if we don't have it yet
	if meta.WorkingDir == "" {
		content := entry.Body
		if content == "" {
			content = entry.Preview
		}

		var fullPath string

		// Pattern: "Working directory: /path/to/folder" followed by \n (JSON escape) or real newline
		markers := []string{"Working directory:", "working directory:"}
		for _, marker := range markers {
			idx := strings.Index(content, marker)
			if idx != -1 {
				pathStart := idx + len(marker)
				// Skip any leading whitespace
				for pathStart < len(content) && (content[pathStart] == ' ' || content[pathStart] == '\t') {
					pathStart++
				}
				// Find the end of the path - look for:
				// - Real newline \n (char code 10)
				// - JSON escaped newline (backslash followed by 'n')
				// - Quote characters
				pathEnd := pathStart
				for pathEnd < len(content) {
					c := content[pathEnd]
					// Real newline
					if c == '\n' || c == '\r' {
						break
					}
					// Quote - end of value
					if c == '"' || c == '\'' {
						break
					}
					// JSON escaped newline: backslash followed by 'n'
					if c == '\\' && pathEnd+1 < len(content) && content[pathEnd+1] == 'n' {
						break
					}
					pathEnd++
				}
				if pathEnd > pathStart {
					fullPath = strings.TrimSpace(content[pathStart:pathEnd])
					// Clean up JSON-escaped backslashes: C:\\Users -> C:\Users
					fullPath = strings.ReplaceAll(fullPath, "\\\\", "\\")
					if isValidFilePath(fullPath) {
						break
					}
					fullPath = "" // Reset if not valid
				}
			}
		}

		// If we found a valid path, store it
		if fullPath != "" && isValidFilePath(fullPath) {
			meta.WorkingDir = fullPath

			// Extract just the folder name (last component of the path)
			folderName := filepath.Base(fullPath)
			if folderName != "" && folderName != "." && folderName != "/" && folderName != "\\" {
				meta.FolderName = folderName
				m.sessionNameMap[entry.SessionID] = folderName
			}
		}
	}
}

// isValidFilePath checks if a string looks like a valid file path
// Path should already be unescaped (JSON backslash escapes resolved)
func isValidFilePath(path string) bool {
	if path == "" || len(path) < 2 {
		return false
	}

	// Reject paths containing JSON-like characters (malformed extractions)
	if strings.ContainsAny(path, "{}\"'`,;[]<>") {
		return false
	}

	// Reject paths that are too short
	if len(path) < 3 {
		return false
	}

	// Unix absolute path (starts with /)
	if strings.HasPrefix(path, "/") {
		return true
	}

	// Windows absolute path: C:\ or C:/
	if len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/') {
		return true
	}

	// Windows UNC path (\\server\share)
	if strings.HasPrefix(path, "\\\\") && len(path) > 4 {
		return true
	}

	// Relative path starting with ./ or ../
	if strings.HasPrefix(path, "./") || strings.HasPrefix(path, "../") {
		return true
	}

	// Home directory path (Unix/macOS)
	if strings.HasPrefix(path, "~/") {
		return true
	}

	return false
}

// extractSessionTitle extracts the conversation title from a response entry
// The title comes from Claude's response to "Please write a 5-10 word title for the following conversation"
func (m *LogViewerModel) extractSessionTitle(entry logger.LogEntry) {
	// Only process response entries
	if entry.Type != logger.LogTypeRes {
		return
	}

	// Ensure metadata exists for this session
	if _, exists := m.sessionMetadataMap[entry.SessionID]; !exists {
		m.sessionMetadataMap[entry.SessionID] = &SessionMetadata{
			SessionID: entry.SessionID,
		}
	}
	meta := m.sessionMetadataMap[entry.SessionID]

	// Only extract if we don't have a title yet
	if meta.Title != "" {
		return
	}

	// Look in the preview first (it often contains the title response)
	preview := entry.Preview
	body := entry.Body

	// Check if this looks like a title response (short text, no special formatting)
	// Title responses are typically 5-10 words without code blocks or special chars
	checkForTitle := func(text string) string {
		text = strings.TrimSpace(text)
		// Title responses are typically short (under 100 chars) and descriptive
		if len(text) > 0 && len(text) < 150 {
			// Skip if it contains JSON, code blocks, or looks like a regular response
			if strings.Contains(text, "{") || strings.Contains(text, "}") ||
				strings.Contains(text, "```") || strings.Contains(text, "import ") ||
				strings.Contains(text, "function ") || strings.Contains(text, "def ") {
				return ""
			}
			// Count words - title should be 3-15 words
			words := strings.Fields(text)
			if len(words) >= 3 && len(words) <= 15 {
				return text
			}
		}
		return ""
	}

	// Try preview first
	if title := checkForTitle(preview); title != "" {
		// Verify by looking at the request - should contain "title" prompt
		if m.isResponseToTitleRequest(entry) {
			meta.Title = title
			return
		}
	}

	// Try to parse body as JSON and extract text content
	if body != "" {
		var resp map[string]interface{}
		if err := json.Unmarshal([]byte(body), &resp); err == nil {
			if content, ok := resp["content"].([]interface{}); ok {
				for _, block := range content {
					if blockMap, ok := block.(map[string]interface{}); ok {
						if blockType, ok := blockMap["type"].(string); ok && blockType == "text" {
							if text, ok := blockMap["text"].(string); ok {
								if title := checkForTitle(text); title != "" {
									if m.isResponseToTitleRequest(entry) {
										meta.Title = title
										return
									}
								}
							}
						}
					}
				}
			}
		}
	}
}

// isResponseToTitleRequest checks if this response corresponds to a title generation request
func (m *LogViewerModel) isResponseToTitleRequest(respEntry logger.LogEntry) bool {
	// Look for the matching request with the same SeqNum
	for _, entry := range m.allEntries {
		if entry.Type == logger.LogTypeReq &&
			entry.SessionID == respEntry.SessionID &&
			entry.SeqNum == respEntry.SeqNum {

			content := entry.Body
			if content == "" {
				content = entry.Preview
			}
			// Check if request contains the title generation prompt
			if strings.Contains(content, "5-10 word title") ||
				strings.Contains(content, "write a title") ||
				strings.Contains(content, "generate a title") {
				return true
			}
			break
		}
	}
	return false
}

// getSessionDisplayName returns the display name for a session
// Uses the folder name if available, otherwise returns the session ID
func (m *LogViewerModel) getSessionDisplayName(sessionID string) string {
	if name, exists := m.sessionNameMap[sessionID]; exists {
		return name
	}
	return sessionID
}

// findNextInPair finds the next entry in the req/res cycle
// From request: find matching response (same SeqNum)
// From response: find next request (SeqNum+1)
func (m *LogViewerModel) findNextInPair() int {
	if len(m.entries) == 0 || m.selectedIndex < 0 || m.selectedIndex >= len(m.entries) {
		return -1
	}

	current := m.entries[m.selectedIndex]

	if current.Type == logger.LogTypeReq {
		// From request: find matching response with same SeqNum
		for i := m.selectedIndex + 1; i < len(m.entries); i++ {
			entry := m.entries[i]
			if entry.Type == logger.LogTypeRes && entry.SeqNum == current.SeqNum {
				return i
			}
			// If we find a request with higher SeqNum, the response doesn't exist
			if entry.Type == logger.LogTypeReq && entry.SeqNum > current.SeqNum {
				// Jump to that next request instead
				return i
			}
		}
		// No matching response found, find next request
		for i := m.selectedIndex + 1; i < len(m.entries); i++ {
			if m.entries[i].Type == logger.LogTypeReq {
				return i
			}
		}
	} else if current.Type == logger.LogTypeRes {
		// From response: find next request (SeqNum+1 or just next req)
		nextSeqNum := current.SeqNum + 1
		for i := m.selectedIndex + 1; i < len(m.entries); i++ {
			entry := m.entries[i]
			if entry.Type == logger.LogTypeReq {
				return i
			}
		}
		// Also search from beginning if at end (wrap around)
		_ = nextSeqNum // Suppress unused warning
	}

	return -1
}

// findPrevInPair finds the previous entry in the req/res cycle
// From response: find matching request (same SeqNum)
// From request: find previous response (SeqNum-1)
func (m *LogViewerModel) findPrevInPair() int {
	if len(m.entries) == 0 || m.selectedIndex < 0 || m.selectedIndex >= len(m.entries) {
		return -1
	}

	current := m.entries[m.selectedIndex]

	if current.Type == logger.LogTypeRes {
		// From response: find matching request with same SeqNum
		for i := m.selectedIndex - 1; i >= 0; i-- {
			entry := m.entries[i]
			if entry.Type == logger.LogTypeReq && entry.SeqNum == current.SeqNum {
				return i
			}
		}
	} else if current.Type == logger.LogTypeReq {
		// From request: find previous response
		for i := m.selectedIndex - 1; i >= 0; i-- {
			entry := m.entries[i]
			if entry.Type == logger.LogTypeRes {
				return i
			}
		}
	}

	return -1
}

// copyToClipboard copies text to system clipboard
func copyToClipboard(text string) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd

		switch runtime.GOOS {
		case "windows":
			cmd = exec.Command("cmd", "/c", "clip")
		case "darwin":
			cmd = exec.Command("pbcopy")
		default: // linux
			// Try xclip first, fall back to xsel
			if _, err := exec.LookPath("xclip"); err == nil {
				cmd = exec.Command("xclip", "-selection", "clipboard")
			} else {
				cmd = exec.Command("xsel", "--clipboard", "--input")
			}
		}

		cmd.Stdin = strings.NewReader(text)
		err := cmd.Run()

		return ClipboardResultMsg{Success: err == nil, Error: err}
	}
}

// openInEditor opens content in external editor (VS Code or default)
func openInEditor(content string, viewMode ViewMode) tea.Cmd {
	return func() tea.Msg {
		// Determine file extension based on view mode
		ext := ".txt"
		switch viewMode {
		case ViewModeJSON:
			ext = ".json"
		case ViewModeRaw, ViewModeParsed:
			ext = ".txt"
		}

		// Create temp file
		tmpDir := os.TempDir()
		tmpFile := filepath.Join(tmpDir, fmt.Sprintf("kiro2cc-export-%d%s", time.Now().UnixNano(), ext))

		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			return EditorResultMsg{Success: false, Error: err}
		}

		// Try to open with VS Code first, fall back to default editor
		var cmd *exec.Cmd

		// Check if VS Code is available
		if _, err := exec.LookPath("code"); err == nil {
			cmd = exec.Command("code", tmpFile)
		} else {
			// Fall back to system default
			switch runtime.GOOS {
			case "windows":
				cmd = exec.Command("cmd", "/c", "start", "", tmpFile)
			case "darwin":
				cmd = exec.Command("open", tmpFile)
			default: // linux
				cmd = exec.Command("xdg-open", tmpFile)
			}
		}

		err := cmd.Start()
		return EditorResultMsg{Success: err == nil, Error: err}
	}
}
