package tui

import "github.com/charmbracelet/lipgloss"

// Color palette - dark theme
var (
	ColorPrimary    = lipgloss.Color("#7D56F4") // Purple
	ColorSecondary  = lipgloss.Color("#04B575") // Green
	ColorAccent     = lipgloss.Color("#F25D94") // Pink
	ColorMuted      = lipgloss.Color("#626262") // Gray
	ColorBorder     = lipgloss.Color("#383838") // Dark gray
	ColorBorderFocus= lipgloss.Color("#7D56F4") // Purple (focused)
	ColorText       = lipgloss.Color("#FAFAFA") // White
	ColorTextMuted  = lipgloss.Color("#A0A0A0") // Light gray
	ColorError      = lipgloss.Color("#FF5555") // Red
	ColorWarning    = lipgloss.Color("#FFAA00") // Orange
	ColorSuccess    = lipgloss.Color("#04B575") // Green
	ColorInfo       = lipgloss.Color("#7D56F4") // Purple
)

// Base styles
var (
	// App frame
	AppStyle = lipgloss.NewStyle().
			Padding(1, 2)

	// Title style
	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary).
			MarginBottom(1)

	// Bordered box - unfocused
	BoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder).
			Padding(0, 1)

	// Bordered box - focused
	BoxFocusedStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorderFocus).
			Padding(0, 1)

	// Help bar style
	HelpStyle = lipgloss.NewStyle().
			Foreground(ColorTextMuted).
			MarginTop(1)

	// Status bar style
	StatusStyle = lipgloss.NewStyle().
			Foreground(ColorText).
			Background(ColorPrimary).
			Padding(0, 1)
)

// Log entry type styles
var (
	LogTypeRequest = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorInfo)

	LogTypeResponse = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorSuccess)

	LogTypeError = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorError)

	LogTypeInfo = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorTextMuted)

	LogTimestamp = lipgloss.NewStyle().
			Foreground(ColorMuted)

	LogPreview = lipgloss.NewStyle().
			Foreground(ColorTextMuted)
)

// Menu styles
var (
	MenuItemStyle = lipgloss.NewStyle().
			Foreground(ColorText).
			PaddingLeft(2)

	MenuItemSelectedStyle = lipgloss.NewStyle().
				Foreground(ColorPrimary).
				Bold(true).
				PaddingLeft(2)

	MenuDescriptionStyle = lipgloss.NewStyle().
				Foreground(ColorTextMuted)
)

// Session info styles
var (
	SessionLabelStyle = lipgloss.NewStyle().
				Foreground(ColorTextMuted).
				Width(14)

	SessionValueStyle = lipgloss.NewStyle().
				Foreground(ColorText)

	SessionValueGoodStyle = lipgloss.NewStyle().
				Foreground(ColorSuccess)

	SessionValueWarnStyle = lipgloss.NewStyle().
				Foreground(ColorWarning)

	SessionValueErrorStyle = lipgloss.NewStyle().
				Foreground(ColorError)
)

// Helper function to create a titled box
func TitledBox(title string, content string, focused bool) string {
	style := BoxStyle
	if focused {
		style = BoxFocusedStyle
	}

	titleRendered := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorPrimary).
		Render(title)

	return lipgloss.JoinVertical(
		lipgloss.Left,
		titleRendered,
		style.Render(content),
	)
}
