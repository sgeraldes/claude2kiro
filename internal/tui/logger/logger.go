package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// LogType categorizes log entries
type LogType int

const (
	LogTypeReq LogType = iota
	LogTypeRes
	LogTypeErr
	LogTypeInf
)

// String returns a short string representation of the log type
func (t LogType) String() string {
	switch t {
	case LogTypeReq:
		return "REQ"
	case LogTypeRes:
		return "RES"
	case LogTypeErr:
		return "ERR"
	case LogTypeInf:
		return "INF"
	default:
		return "???"
	}
}

// LogEntry represents a single log entry
type LogEntry struct {
	Timestamp time.Time
	Type      LogType
	Model     string        // For requests: the model being used
	Method    string        // HTTP method
	Path      string        // URL path
	Status    int           // HTTP status code (for responses)
	Duration  time.Duration // Request duration (for responses)
	Preview   string        // Truncated body preview for list
	Body      string        // Full body content for detail view
}

// LogEntryMsg carries a log entry to the TUI
type LogEntryMsg struct {
	Entry LogEntry
}

// Color definitions for log formatting
var (
	colorReq     = lipgloss.Color("#7D56F4") // Purple
	colorRes     = lipgloss.Color("#04B575") // Green
	colorErr     = lipgloss.Color("#FF5555") // Red
	colorInf     = lipgloss.Color("#626262") // Gray
	colorTime    = lipgloss.Color("#626262") // Gray
	colorPreview = lipgloss.Color("#A0A0A0") // Light gray
)

// Format returns a single-line formatted log entry
func (e LogEntry) Format(maxWidth int) string {
	timestamp := e.Timestamp.Format("15:04:05")

	// Style the type badge based on log type
	var typeStyle lipgloss.Style
	switch e.Type {
	case LogTypeReq:
		typeStyle = lipgloss.NewStyle().Bold(true).Foreground(colorReq)
	case LogTypeRes:
		typeStyle = lipgloss.NewStyle().Bold(true).Foreground(colorRes)
	case LogTypeErr:
		typeStyle = lipgloss.NewStyle().Bold(true).Foreground(colorErr)
	default:
		typeStyle = lipgloss.NewStyle().Bold(true).Foreground(colorInf)
	}

	timeStyle := lipgloss.NewStyle().Foreground(colorTime)
	previewStyle := lipgloss.NewStyle().Foreground(colorPreview)

	// Build the details section based on type
	var details string
	switch e.Type {
	case LogTypeReq:
		model := e.Model
		if len(model) > 20 {
			model = model[:17] + "..."
		}
		details = fmt.Sprintf("%s %s %s", model, e.Method, e.Path)
	case LogTypeRes:
		details = fmt.Sprintf("%d %s (%v)", e.Status, e.Path, e.Duration.Round(time.Millisecond))
	case LogTypeErr:
		details = e.Preview
	default:
		details = e.Preview
	}

	// Calculate available width for preview
	// Format: [HH:MM:SS] [TYP] details | preview
	baseLen := len(timestamp) + 3 + 5 + len(details) + 3 // brackets, spaces, pipe
	previewWidth := maxWidth - baseLen
	if previewWidth < 10 {
		previewWidth = 10
	}

	preview := truncate(e.Preview, previewWidth)

	// Only show preview for requests (body content)
	if e.Type == LogTypeReq && preview != "" {
		return fmt.Sprintf("%s %s %s | %s",
			timeStyle.Render("["+timestamp+"]"),
			typeStyle.Render("["+e.Type.String()+"]"),
			details,
			previewStyle.Render(preview),
		)
	}

	return fmt.Sprintf("%s %s %s",
		timeStyle.Render("["+timestamp+"]"),
		typeStyle.Render("["+e.Type.String()+"]"),
		details,
	)
}

// FormatPlain returns a plain text formatted log entry (for file logging)
func (e LogEntry) FormatPlain() string {
	timestamp := e.Timestamp.Format("2006-01-02 15:04:05.000")

	var details string
	switch e.Type {
	case LogTypeReq:
		details = fmt.Sprintf("%s %s %s", e.Model, e.Method, e.Path)
	case LogTypeRes:
		details = fmt.Sprintf("%d %s (%v)", e.Status, e.Path, e.Duration.Round(time.Millisecond))
	default:
		details = e.Preview
	}

	if e.Type == LogTypeReq && e.Preview != "" {
		return fmt.Sprintf("[%s] [%s] %s | %s", timestamp, e.Type.String(), details, truncate(e.Preview, 200))
	}

	return fmt.Sprintf("[%s] [%s] %s", timestamp, e.Type.String(), details)
}

// truncate shortens a string to maxLen characters
func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// Logger manages log entries with a ring buffer
type Logger struct {
	entries    []LogEntry
	maxEntries int
	mu         sync.RWMutex
	program    *tea.Program
	fileWriter *os.File
	filePath   string
}

// NewLogger creates a new logger with a maximum number of entries
func NewLogger(maxEntries int) *Logger {
	return &Logger{
		entries:    make([]LogEntry, 0, maxEntries),
		maxEntries: maxEntries,
	}
}

// SetProgram sets the Bubble Tea program for sending messages
func (l *Logger) SetProgram(p *tea.Program) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.program = p
}

// GetProgram returns the Bubble Tea program reference
func (l *Logger) GetProgram() *tea.Program {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.program
}

// EnableFileLogging enables writing logs to a file
func (l *Logger) EnableFileLogging(logDir string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Create log directory if it doesn't exist
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// Create log file with date-based name
	filename := time.Now().Format("2006-01-02") + ".log"
	l.filePath = filepath.Join(logDir, filename)

	file, err := os.OpenFile(l.filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	l.fileWriter = file
	return nil
}

// DisableFileLogging closes the log file
func (l *Logger) DisableFileLogging() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.fileWriter != nil {
		l.fileWriter.Close()
		l.fileWriter = nil
	}
}

// Log adds a new log entry
func (l *Logger) Log(entry LogEntry) {
	l.mu.Lock()

	// Ring buffer: remove oldest if at capacity
	if len(l.entries) >= l.maxEntries {
		l.entries = l.entries[1:]
	}
	l.entries = append(l.entries, entry)

	// Write to file if enabled
	if l.fileWriter != nil {
		l.fileWriter.WriteString(entry.FormatPlain() + "\n")
	}

	// Get program reference while holding lock
	program := l.program

	l.mu.Unlock()

	// Send to TUI (outside lock to avoid deadlock)
	if program != nil {
		program.Send(LogEntryMsg{Entry: entry})
	}
}

// LogRequest is a convenience method for logging requests
func (l *Logger) LogRequest(model, method, path, body string) {
	// Create truncated preview for list view
	preview := body
	if len(preview) > 100 {
		preview = preview[:97] + "..."
	}
	l.Log(LogEntry{
		Timestamp: time.Now(),
		Type:      LogTypeReq,
		Model:     model,
		Method:    method,
		Path:      path,
		Preview:   preview,
		Body:      body,
	})
}

// LogResponse is a convenience method for logging responses
func (l *Logger) LogResponse(status int, path string, duration time.Duration) {
	l.Log(LogEntry{
		Timestamp: time.Now(),
		Type:      LogTypeRes,
		Status:    status,
		Path:      path,
		Duration:  duration,
		Preview:   fmt.Sprintf("%d %s (%v)", status, path, duration.Round(time.Millisecond)),
	})
}

// LogError is a convenience method for logging errors
func (l *Logger) LogError(message string) {
	l.Log(LogEntry{
		Timestamp: time.Now(),
		Type:      LogTypeErr,
		Preview:   message,
	})
}

// LogInfo is a convenience method for logging info messages
func (l *Logger) LogInfo(message string) {
	l.Log(LogEntry{
		Timestamp: time.Now(),
		Type:      LogTypeInf,
		Preview:   message,
	})
}

// GetEntries returns a copy of all log entries
func (l *Logger) GetEntries() []LogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()

	result := make([]LogEntry, len(l.entries))
	copy(result, l.entries)
	return result
}

// Clear removes all log entries
func (l *Logger) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = l.entries[:0]
}

// Count returns the number of log entries
func (l *Logger) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.entries)
}

// FilePath returns the current log file path, if file logging is enabled
func (l *Logger) FilePath() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.filePath
}
