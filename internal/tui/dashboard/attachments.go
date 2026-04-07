package dashboard

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sgeraldes/claude2kiro/internal/attachments"
	"github.com/sgeraldes/claude2kiro/internal/config"
)

// AttachmentBrowserModel displays all attachments stored in the attachment store
type AttachmentBrowserModel struct {
	store         *attachments.Store
	attachments   []*attachments.AttachmentMeta
	selectedIndex int
	viewport      viewport.Model
	width         int
	height        int
	focused       bool
	showDetail    bool
	confirmDelete bool // Show delete confirmation
}

// DeleteAttachmentMsg signals that an attachment should be deleted
type DeleteAttachmentMsg struct {
	Hash string
}

// OpenFileMsg signals that a file should be opened in the default app
type OpenFileMsg struct {
	Path string
}

// NewAttachmentBrowser creates a new attachment browser
func NewAttachmentBrowser(store *attachments.Store) AttachmentBrowserModel {
	vp := viewport.New(80, 20)
	vp.SetContent("")

	m := AttachmentBrowserModel{
		store:         store,
		attachments:   make([]*attachments.AttachmentMeta, 0),
		selectedIndex: 0,
		viewport:      vp,
		width:         80,
		height:        24,
		focused:       false,
		showDetail:    true,
	}

	m.Refresh()
	return m
}

// Init initializes the attachment browser
func (m AttachmentBrowserModel) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (m AttachmentBrowserModel) Update(msg tea.Msg) (AttachmentBrowserModel, tea.Cmd) {
	var cmd tea.Cmd
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if !m.focused {
			return m, nil
		}

		// Handle delete confirmation
		if m.confirmDelete {
			switch msg.String() {
			case "y", "Y":
				// Delete confirmed
				if m.selectedIndex >= 0 && m.selectedIndex < len(m.attachments) {
					hash := m.attachments[m.selectedIndex].Hash
					cmds = append(cmds, func() tea.Msg {
						return DeleteAttachmentMsg{Hash: hash}
					})
				}
				m.confirmDelete = false
			case "n", "N", "esc":
				// Cancel delete
				m.confirmDelete = false
			}
			return m, tea.Batch(cmds...)
		}

		// Normal key handling
		switch msg.String() {
		case "up", "k":
			if m.selectedIndex > 0 {
				m.selectedIndex--
			}

		case "down", "j":
			if m.selectedIndex < len(m.attachments)-1 {
				m.selectedIndex++
			}

		case "enter":
			m.showDetail = !m.showDetail

		case "o":
			// Open file in default app
			if m.selectedIndex >= 0 && m.selectedIndex < len(m.attachments) {
				hash := m.attachments[m.selectedIndex].Hash
				data, meta, err := m.store.Get(hash)
				if err == nil {
					// Create a temporary file
					tmpFile, err := os.CreateTemp("", "attachment-*"+meta.Extension)
					if err == nil {
						tmpFile.Write(data)
						tmpFile.Close()
						cmds = append(cmds, openFile(tmpFile.Name()))
					}
				}
			}

		case "y":
			// Copy hash to clipboard
			if m.selectedIndex >= 0 && m.selectedIndex < len(m.attachments) {
				hash := m.attachments[m.selectedIndex].Hash
				cmds = append(cmds, copyToClipboard(hash))
			}

		case "d":
			// Show delete confirmation
			m.confirmDelete = true

		case "esc":
			// Signal to go back to dashboard
			// This will be handled by parent
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateLayout()
	}

	// Update viewport for detail scrolling
	if m.showDetail {
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// View renders the attachment browser
func (m AttachmentBrowserModel) View() string {
	if len(m.attachments) == 0 {
		return m.renderEmpty()
	}

	// Calculate stats
	totalFiles := len(m.attachments)
	var totalSize int64
	var savedByDedup int64
	for _, meta := range m.attachments {
		totalSize += meta.Size
		savedByDedup += meta.Size * int64(meta.ReuseCount)
	}

	// Header
	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#626262")).
		Bold(true)

	header := fmt.Sprintf("Attachments (%d files, %s, saved %s)",
		totalFiles,
		config.FormatBytes(totalSize),
		config.FormatBytes(savedByDedup))

	// List section
	listHeight := m.height - 12 // Reserve space for header, detail, footer
	if listHeight < 5 {
		listHeight = 5
	}
	listView := m.renderList(listHeight)

	// Detail section (if enabled)
	var detailView string
	if m.showDetail {
		detailView = m.renderDetail()
	}

	// Footer (actions or confirmation)
	var footerView string
	if m.confirmDelete {
		confirmStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF6B6B")).
			Bold(true)
		footerView = confirmStyle.Render("Delete this attachment? [y] Yes  [n] No")
	} else {
		dimStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#626262"))
		footerView = dimStyle.Render("[o] Open  [y] Copy hash  [d] Delete  [Esc] Back")
	}

	// Assemble view
	var sections []string
	sections = append(sections, headerStyle.Render(header))
	sections = append(sections, listView)
	if m.showDetail {
		sections = append(sections, strings.Repeat("─", m.width-4))
		sections = append(sections, detailView)
	}
	sections = append(sections, strings.Repeat("─", m.width-4))
	sections = append(sections, footerView)

	content := strings.Join(sections, "\n")

	// Wrap in panel style
	var panelStyle lipgloss.Style
	if m.focused {
		panelStyle = focusedPanelStyle.Width(m.width - 2)
	} else {
		panelStyle = panelStyle.Width(m.width - 2)
	}

	return panelStyle.Render(content)
}

// renderEmpty shows a message when no attachments exist
func (m AttachmentBrowserModel) renderEmpty() string {
	dimStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#626262")).
		Italic(true)

	msg := "No attachments stored yet.\n\nAttachments will appear here when Claude sends files in requests."

	return lipgloss.NewStyle().
		Width(m.width-4).
		Height(m.height-4).
		Align(lipgloss.Center, lipgloss.Center).
		Render(dimStyle.Render(msg))
}

// renderList renders the list of attachments
func (m AttachmentBrowserModel) renderList(height int) string {
	var lines []string

	// Calculate visible range
	start := m.selectedIndex - height/2
	if start < 0 {
		start = 0
	}
	end := start + height
	if end > len(m.attachments) {
		end = len(m.attachments)
		start = end - height
		if start < 0 {
			start = 0
		}
	}

	for i := start; i < end; i++ {
		meta := m.attachments[i]
		lines = append(lines, m.renderListItem(meta, i == m.selectedIndex))
	}

	// Pad to height
	for len(lines) < height {
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// renderListItem renders a single attachment in the list
func (m AttachmentBrowserModel) renderListItem(meta *attachments.AttachmentMeta, selected bool) string {
	icon := getMediaTypeIcon(meta.MediaType)

	// Truncate hash to first 12 chars
	shortHash := meta.Hash
	if len(shortHash) > 12 {
		shortHash = shortHash[:12] + "..."
	}

	// Format size
	sizeStr := config.FormatBytes(meta.Size)

	// Format reuse count
	reuseStr := fmt.Sprintf("%d× reused", meta.ReuseCount)
	if meta.ReuseCount == 0 {
		reuseStr = "new"
	} else if meta.ReuseCount == 1 {
		reuseStr = "1× reused"
	}

	// Build line
	line := fmt.Sprintf("%s %s  %s  %s  %s",
		icon,
		shortHash,
		padRight(meta.MediaType, 20),
		padLeft(sizeStr, 10),
		reuseStr)

	// Style based on selection
	if selected {
		style := lipgloss.NewStyle().
			Background(selectedBg).
			Foreground(brightColor).
			Bold(true)
		return "▶ " + style.Render(line)
	}

	style := lipgloss.NewStyle().
		Foreground(normalColor)
	return "  " + style.Render(line)
}

// renderDetail renders the detail panel for the selected attachment
func (m AttachmentBrowserModel) renderDetail() string {
	if m.selectedIndex < 0 || m.selectedIndex >= len(m.attachments) {
		return ""
	}

	meta := m.attachments[m.selectedIndex]

	labelStyle := lipgloss.NewStyle().Foreground(dimColor)
	valueStyle := lipgloss.NewStyle().Foreground(brightColor)

	var lines []string
	lines = append(lines, labelStyle.Render("Hash: ")+valueStyle.Render(meta.Hash))
	lines = append(lines, labelStyle.Render("Type: ")+valueStyle.Render(meta.MediaType))
	lines = append(lines, labelStyle.Render("Size: ")+valueStyle.Render(fmt.Sprintf("%s (%d bytes)", config.FormatBytes(meta.Size), meta.Size)))
	lines = append(lines, labelStyle.Render("Extension: ")+valueStyle.Render(meta.Extension))
	lines = append(lines, labelStyle.Render("First seen: ")+valueStyle.Render(meta.FirstSeen.Format("2006-01-02 15:04:05")))
	lines = append(lines, labelStyle.Render("Last seen: ")+valueStyle.Render(meta.LastSeen.Format("2006-01-02 15:04:05")))

	savedBytes := meta.Size * int64(meta.ReuseCount)
	lines = append(lines, labelStyle.Render(fmt.Sprintf("Reused: %d times (saved %s)", meta.ReuseCount, config.FormatBytes(savedBytes))))

	return strings.Join(lines, "\n")
}

// SetSize updates the component size
func (m *AttachmentBrowserModel) SetSize(width, height int) {
	m.width = width
	m.height = height
	m.updateLayout()
}

// updateLayout recalculates component sizes
func (m *AttachmentBrowserModel) updateLayout() {
	// Update viewport size for detail panel
	vpWidth := m.width - 6
	if vpWidth < 20 {
		vpWidth = 20
	}
	vpHeight := 7 // Fixed height for detail panel
	m.viewport.Width = vpWidth
	m.viewport.Height = vpHeight
}

// SetFocused sets the focus state
func (m *AttachmentBrowserModel) SetFocused(focused bool) {
	m.focused = focused
}

// Refresh reloads attachments from the store
func (m *AttachmentBrowserModel) Refresh() {
	m.attachments = m.store.GetAll()

	// Sort by last seen (most recent first)
	sort.Slice(m.attachments, func(i, j int) bool {
		return m.attachments[i].LastSeen.After(m.attachments[j].LastSeen)
	})

	// Reset selection if out of bounds
	if m.selectedIndex >= len(m.attachments) {
		m.selectedIndex = len(m.attachments) - 1
	}
	if m.selectedIndex < 0 && len(m.attachments) > 0 {
		m.selectedIndex = 0
	}
}

// getMediaTypeIcon returns an emoji icon for the given media type
func getMediaTypeIcon(mediaType string) string {
	if strings.HasPrefix(mediaType, "image/") {
		return "🖼️"
	}
	if strings.HasPrefix(mediaType, "video/") {
		return "🎬"
	}
	if strings.HasPrefix(mediaType, "audio/") {
		return "🔊"
	}
	if mediaType == "application/pdf" {
		return "📄"
	}
	return "📎"
}

// openFile opens a file in the default application
func openFile(path string) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "windows":
			cmd = exec.Command("cmd", "/c", "start", "", path)
		case "darwin":
			cmd = exec.Command("open", path)
		default: // linux
			cmd = exec.Command("xdg-open", path)
		}

		err := cmd.Start()
		if err != nil {
			return EditorResultMsg{Success: false, Error: err}
		}

		return EditorResultMsg{Success: true}
	}
}

// padRight pads a string to the right to reach the specified width
func padRight(s string, width int) string {
	if len(s) >= width {
		return s[:width]
	}
	return s + strings.Repeat(" ", width-len(s))
}

// padLeft pads a string to the left to reach the specified width
func padLeft(s string, width int) string {
	if len(s) >= width {
		return s[:width]
	}
	return strings.Repeat(" ", width-len(s)) + s
}
