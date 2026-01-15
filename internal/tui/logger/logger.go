package logger

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sgeraldes/claude2kiro/internal/attachments"
	"github.com/sgeraldes/claude2kiro/internal/config"
)

// LogType categorizes log entries
type LogType int

const (
	LogTypeReq LogType = iota
	LogTypeRes
	LogTypeErr
	LogTypeInf
	LogTypeCmp
)

// Preview generation thresholds
const (
	// PreviewFastModeThreshold is the body size (bytes) above which we use
	// fast string matching instead of full JSON parsing to generate previews.
	// Chosen to balance accuracy (JSON parsing) vs. performance (string matching).
	PreviewFastModeThreshold = 50000 // 50KB

	// PreviewVeryLargeThreshold is the body size (bytes) above which we skip
	// all preview processing and just truncate. Prevents excessive CPU usage.
	PreviewVeryLargeThreshold = 100000 // 100KB

	// PreviewSearchBufferSize is the max bytes searched for user message text
	// in large request bodies. Balances finding relevant content vs. performance.
	PreviewSearchBufferSize = 20000 // 20KB

	// PreviewMetadataReserve is chars reserved for metadata (tool count, etc)
	// when building preview text. Ensures room for "[5 tools, 10 msgs]" suffix.
	PreviewMetadataReserve = 30

	// SSEPreviewSearchLimit is the max bytes searched in SSE content for text
	// extraction. Prevents slow scanning of very large streaming responses.
	SSEPreviewSearchLimit = 10240 // 10KB

	// ToolsSectionLimit is the max bytes searched for tool names in request body.
	ToolsSectionLimit = 51200 // ~50KB
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
	case LogTypeCmp:
		return "CMP"
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
	Status    int           // HTTP status code (for responses), or parse status for requests (0=OK, 400=parse error)
	Duration  time.Duration // Request duration (for responses), or parse time (for requests)
	Preview   string        // Truncated body preview for list
	Body      string        // Full body content for detail view
	SessionID string        // Short session identifier (last 8 chars of UUID)
	FullUUID  string        // Full session UUID from metadata (for --resume)
	RequestID string        // Unique request ID for correlation (6 chars)
	BodySize  int           // Original body size in bytes (for display)
	SeqNum    int           // Sequential request number for display (#01, #02) - same for matching req/res pairs
}

// EstimatedMemorySize returns an estimate of memory used by this entry in bytes
func (e LogEntry) EstimatedMemorySize() int {
	// Base struct overhead (approx 128 bytes for fields, pointers, etc.)
	size := 128
	// Add string lengths (Go strings are counted by their length)
	size += len(e.Model)
	size += len(e.Method)
	size += len(e.Path)
	size += len(e.Preview)
	size += len(e.Body)
	size += len(e.SessionID)
	size += len(e.RequestID)
	return size
}

// LogEntryMsg carries a log entry to the TUI
type LogEntryMsg struct {
	Entry LogEntry
}

// AttachmentCounter tracks attachment counts for filename generation
type AttachmentCounter struct {
	mu      sync.Mutex
	counter int
}

// Color definitions for log formatting
var (
	colorReq     = lipgloss.Color("#7D56F4") // Purple
	colorRes     = lipgloss.Color("#04B575") // Green
	colorErr     = lipgloss.Color("#FF5555") // Red
	colorInf     = lipgloss.Color("#626262") // Gray
	colorCmp     = lipgloss.Color("#FFB86C") // Orange (for comparison mode)
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
	case LogTypeCmp:
		typeStyle = lipgloss.NewStyle().Bold(true).Foreground(colorCmp)
	default:
		typeStyle = lipgloss.NewStyle().Bold(true).Foreground(colorInf)
	}

	timeStyle := lipgloss.NewStyle().Foreground(colorTime)
	previewStyle := lipgloss.NewStyle().Foreground(colorPreview)

	// Build session/request ID prefix for requests, responses, and comparisons
	var idPrefix string
	if (e.Type == LogTypeReq || e.Type == LogTypeRes || e.Type == LogTypeCmp) && (e.SessionID != "" || e.RequestID != "") {
		idStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
		if e.SessionID != "" && e.RequestID != "" {
			idPrefix = idStyle.Render(fmt.Sprintf("[%s:%s] ", e.SessionID, e.RequestID))
		} else if e.RequestID != "" {
			idPrefix = idStyle.Render(fmt.Sprintf("[%s] ", e.RequestID))
		}
	}

	// Build the details section based on type
	var details string
	switch e.Type {
	case LogTypeReq:
		model := e.Model
		if len(model) > 20 {
			model = model[:17] + "..."
		}
		details = fmt.Sprintf("%s%s %s %s", idPrefix, model, e.Method, e.Path)
	case LogTypeRes:
		details = fmt.Sprintf("%s%d %s (%v)", idPrefix, e.Status, e.Path, e.Duration.Round(time.Millisecond))
	case LogTypeCmp:
		details = idPrefix + e.Preview
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

	// Show preview for requests and responses with body content
	if (e.Type == LogTypeReq || e.Type == LogTypeRes) && preview != "" {
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

	// Build ID prefix for requests, responses, and comparisons
	var idPrefix string
	if (e.Type == LogTypeReq || e.Type == LogTypeRes || e.Type == LogTypeCmp) && (e.SessionID != "" || e.RequestID != "") {
		if e.SessionID != "" && e.RequestID != "" {
			idPrefix = fmt.Sprintf("[%s:%s] ", e.SessionID, e.RequestID)
		} else if e.RequestID != "" {
			idPrefix = fmt.Sprintf("[%s] ", e.RequestID)
		}
	}

	var details string
	switch e.Type {
	case LogTypeReq:
		details = fmt.Sprintf("%s%s %s %s", idPrefix, e.Model, e.Method, e.Path)
	case LogTypeRes:
		details = fmt.Sprintf("%s%d %s (%v)", idPrefix, e.Status, e.Path, e.Duration.Round(time.Millisecond))
	case LogTypeCmp:
		details = idPrefix + e.Preview
	default:
		details = e.Preview
	}

	// Include body content for REQ and RES entries (use Body for full content, fallback to Preview)
	content := e.Body
	if content == "" {
		content = e.Preview
	}
	if (e.Type == LogTypeReq || e.Type == LogTypeRes) && content != "" {
		cfg := config.Get()
		// FileContentLen = 0 means unlimited
		if cfg.Logging.FileContentLen > 0 {
			content = truncate(content, cfg.Logging.FileContentLen)
		}
		// Include original body size as @size:N metadata before the body
		// This preserves the original size even when body is truncated
		sizeMarker := ""
		if e.BodySize > 0 {
			sizeMarker = fmt.Sprintf("@size:%d@", e.BodySize)
		}
		return fmt.Sprintf("[%s] [%s] %s | %s%s", timestamp, e.Type.String(), details, sizeMarker, content)
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
	entries           []LogEntry
	maxEntries        int
	mu                sync.RWMutex
	program           *tea.Program
	fileWriter        *os.File
	filePath          string
	requestCount      uint64         // Counter for generating unique request IDs
	seqNumMap         map[string]int // Maps session ID to current sequential number
	attachmentCounter AttachmentCounter
	attachmentStore   *attachments.Store // Store for deduplicating attachments
}

// NewLogger creates a new logger with a maximum number of entries
func NewLogger(maxEntries int) *Logger {
	return &Logger{
		entries:    make([]LogEntry, 0, maxEntries),
		maxEntries: maxEntries,
		seqNumMap:  make(map[string]int),
	}
}

// SetProgram sets the Bubble Tea program for sending messages
func (l *Logger) SetProgram(p *tea.Program) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.program = p
}

// SetAttachmentStore sets the attachment store for deduplication
func (l *Logger) SetAttachmentStore(store *attachments.Store) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attachmentStore = store
}

// GetProgram returns the Bubble Tea program reference
func (l *Logger) GetProgram() *tea.Program {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.program
}

// DeduplicateBody processes a body string and replaces base64 attachments with references
// This can be called from main.go to deduplicate request bodies before logging
func (l *Logger) DeduplicateBody(body string) string {
	l.mu.RLock()
	store := l.attachmentStore
	l.mu.RUnlock()

	if store == nil {
		return body // No store configured, return as-is
	}

	// Use extractAndSaveAttachments logic with current timestamp
	processed, err := l.extractAndSaveAttachments(body, time.Now())
	if err != nil {
		return body // On error, return original
	}
	return processed
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

	// Store original body size BEFORE any truncation
	originalBodySize := len(entry.Body)
	if originalBodySize > 0 {
		entry.BodySize = originalBodySize
	}

	cfg := config.Get()

	// Process attachments based on mode
	bodyForFile := entry.Body
	bodyForMemory := entry.Body

	switch cfg.Logging.AttachmentMode {
	case "full":
		// Mode 1: Full base64 in both file and memory (default, large files)
		// No processing needed - use original body for everything
		bodyForFile = entry.Body
		bodyForMemory = entry.Body

	case "placeholder":
		// Mode 2: Replace base64 with placeholders everywhere (small files, lose data)
		if cfg.Logging.MaxBodySizeKB > 0 {
			processed := replaceBase64WithPlaceholders(entry.Body, cfg.Logging.MaxBodySizeKB)
			bodyForFile = processed
			bodyForMemory = processed
		}

	case "separate":
		// Mode 3: Save attachments to separate files, use references in logs
		processed, err := l.extractAndSaveAttachments(entry.Body, entry.Timestamp)
		if err != nil {
			// Log error but continue with original body
			bodyForFile = entry.Body
			bodyForMemory = entry.Body
		} else {
			bodyForFile = processed
			bodyForMemory = processed
		}

	default:
		// Unknown mode - default to placeholder behavior
		if cfg.Logging.MaxBodySizeKB > 0 {
			processed := replaceBase64WithPlaceholders(entry.Body, cfg.Logging.MaxBodySizeKB)
			bodyForFile = processed
			bodyForMemory = processed
		}
	}

	// Create a copy of entry for file logging with processed body
	fileEntry := entry
	fileEntry.Body = bodyForFile

	// Write to file with processed body
	if l.fileWriter != nil {
		l.fileWriter.WriteString(fileEntry.FormatPlain() + "\n")
	}

	// Update entry for memory storage
	entry.Body = bodyForMemory

	// Ring buffer: remove oldest if at capacity
	if len(l.entries) >= l.maxEntries {
		l.entries = l.entries[1:]
	}
	l.entries = append(l.entries, entry)

	// Get program reference while holding lock
	program := l.program

	l.mu.Unlock()

	// Send to TUI (outside lock to avoid deadlock)
	if program != nil {
		program.Send(LogEntryMsg{Entry: entry})
	}
}

// sanitizePreview creates a single-line preview from text
func sanitizePreview(text string, maxLen int) string {
	// Replace newlines and tabs with single space
	preview := strings.ReplaceAll(text, "\n", " ")
	preview = strings.ReplaceAll(preview, "\r", " ")
	preview = strings.ReplaceAll(preview, "\t", " ")
	// Collapse multiple spaces
	for strings.Contains(preview, "  ") {
		preview = strings.ReplaceAll(preview, "  ", " ")
	}
	preview = strings.TrimSpace(preview)
	// Truncate
	if len(preview) > maxLen {
		preview = preview[:maxLen-3] + "..."
	}
	return preview
}

// generateRequestPreview creates a meaningful preview for API request bodies
// Instead of showing raw JSON, it extracts key info: model, tools count, messages
func generateRequestPreview(body string, maxLen int) string {
	// For very large bodies, use fast string extraction instead of JSON parsing
	if len(body) > PreviewFastModeThreshold {
		return generateRequestPreviewFast(body, maxLen)
	}

	// Try to parse as JSON
	var req map[string]interface{}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		// Not valid JSON, fall back to sanitizePreview
		return sanitizePreview(body, maxLen)
	}

	var parts []string

	// Extract last user message text (most relevant info)
	if messages, ok := req["messages"].([]interface{}); ok && len(messages) > 0 {
		// Find last user message
		for i := len(messages) - 1; i >= 0; i-- {
			if msg, ok := messages[i].(map[string]interface{}); ok {
				if role, _ := msg["role"].(string); role == "user" {
					content := extractUserMessageText(msg["content"])
					if content != "" {
						// Truncate but leave room for metadata
						maxText := maxLen - PreviewMetadataReserve // Reserve space for tool/msg count
						if len(content) > maxText {
							content = content[:maxText-3] + "..."
						}
						parts = append(parts, content)
						break
					}
				}
			}
		}
	}

	// Add metadata summary
	var meta []string
	if tools, ok := req["tools"].([]interface{}); ok && len(tools) > 0 {
		meta = append(meta, fmt.Sprintf("%d tools", len(tools)))
	}
	if messages, ok := req["messages"].([]interface{}); ok {
		meta = append(meta, fmt.Sprintf("%d msgs", len(messages)))
	}

	if len(meta) > 0 {
		parts = append(parts, "["+strings.Join(meta, ", ")+"]")
	}

	if len(parts) == 0 {
		return sanitizePreview(body, maxLen)
	}

	preview := strings.Join(parts, " ")
	if len(preview) > maxLen {
		preview = preview[:maxLen-3] + "..."
	}
	return preview
}

// generateRequestPreviewFast creates a preview using fast string matching (no JSON parsing)
// Used for large request bodies to avoid slow JSON parsing
func generateRequestPreviewFast(body string, maxLen int) string {
	// Count tools by looking for "name": patterns in tools array section
	toolCount := 0
	if toolsIdx := strings.Index(body, `"tools":`); toolsIdx != -1 {
		// Count occurrences of "name": in the next 50KB (tools section)
		toolsSection := body[toolsIdx:]
		if len(toolsSection) > ToolsSectionLimit {
			toolsSection = toolsSection[:ToolsSectionLimit]
		}
		toolCount = strings.Count(toolsSection, `"name":`)
	}

	// Count messages
	msgCount := strings.Count(body, `"role":`)

	// Try to find last user message text (search from end, limited)
	var userText string
	searchEnd := len(body)
	if searchEnd > PreviewSearchBufferSize {
		// Search last 20KB for user message
		searchStart := searchEnd - PreviewSearchBufferSize
		lastSection := body[searchStart:]
		// Find last "role":"user"
		lastUserIdx := strings.LastIndex(lastSection, `"role":"user"`)
		if lastUserIdx != -1 {
			// Look for "text":" after it
			afterUser := lastSection[lastUserIdx:]
			if textIdx := strings.Index(afterUser, `"text":"`); textIdx != -1 {
				startPos := textIdx + 8
				endPos := startPos
				for endPos < len(afterUser) && endPos < startPos+500 {
					if afterUser[endPos] == '"' && afterUser[endPos-1] != '\\' {
						break
					}
					endPos++
				}
				if endPos > startPos {
					userText = afterUser[startPos:endPos]
					userText = strings.ReplaceAll(userText, `\n`, " ")
					userText = strings.ReplaceAll(userText, `\"`, `"`)
				}
			}
		}
	}

	var parts []string
	if userText != "" {
		maxText := maxLen - PreviewMetadataReserve
		if len(userText) > maxText {
			userText = userText[:maxText-3] + "..."
		}
		parts = append(parts, userText)
	}

	var meta []string
	if toolCount > 0 {
		meta = append(meta, fmt.Sprintf("%d tools", toolCount))
	}
	if msgCount > 0 {
		meta = append(meta, fmt.Sprintf("%d msgs", msgCount/2)) // Divide by 2 since we count both user and assistant
	}
	if len(meta) > 0 {
		parts = append(parts, "["+strings.Join(meta, ", ")+"]")
	}

	if len(parts) == 0 {
		return sanitizePreview(body[:min(len(body), maxLen*2)], maxLen)
	}

	preview := strings.Join(parts, " ")
	if len(preview) > maxLen {
		preview = preview[:maxLen-3] + "..."
	}
	return preview
}

// extractSSETextPreview extracts text content from SSE using fast string matching (no JSON parsing)
func extractSSETextPreview(content string, maxLen int) string {
	// Limit search to first 10KB for performance
	searchContent := content
	if len(searchContent) > SSEPreviewSearchLimit {
		searchContent = searchContent[:SSEPreviewSearchLimit]
	}

	var result strings.Builder
	// Look for "text":" patterns which indicate text deltas in SSE
	searchIdx := 0
	for result.Len() < maxLen*2 { // Gather enough for truncation
		idx := strings.Index(searchContent[searchIdx:], `"text":"`)
		if idx == -1 {
			break
		}
		startPos := searchIdx + idx + 8 // Skip past "text":"
		// Find the closing quote (handle escaped quotes)
		endPos := startPos
		for endPos < len(searchContent) {
			if searchContent[endPos] == '"' && (endPos == startPos || searchContent[endPos-1] != '\\') {
				break
			}
			endPos++
		}
		if endPos > startPos && endPos <= len(searchContent) {
			text := searchContent[startPos:endPos]
			// Unescape common JSON escapes
			text = strings.ReplaceAll(text, `\\n`, " ")
			text = strings.ReplaceAll(text, `\n`, " ")
			text = strings.ReplaceAll(text, `\"`, `"`)
			text = strings.ReplaceAll(text, `\\`, `\`)
			result.WriteString(text)
		}
		searchIdx = endPos + 1
		if searchIdx >= len(searchContent) {
			break
		}
	}

	if result.Len() == 0 {
		return "[no text content]"
	}

	preview := result.String()
	// Clean up whitespace
	preview = strings.ReplaceAll(preview, "\n", " ")
	preview = strings.ReplaceAll(preview, "\r", " ")
	for strings.Contains(preview, "  ") {
		preview = strings.ReplaceAll(preview, "  ", " ")
	}
	preview = strings.TrimSpace(preview)

	if len(preview) > maxLen {
		preview = preview[:maxLen-3] + "..."
	}
	return preview
}

// extractUserMessageText extracts text from a message content field
func extractUserMessageText(content interface{}) string {
	switch c := content.(type) {
	case string:
		// Simple string content - clean up newlines
		text := strings.ReplaceAll(c, "\n", " ")
		text = strings.ReplaceAll(text, "\r", " ")
		for strings.Contains(text, "  ") {
			text = strings.ReplaceAll(text, "  ", " ")
		}
		return strings.TrimSpace(text)
	case []interface{}:
		// Array of content blocks - find first text block
		for _, block := range c {
			if blockMap, ok := block.(map[string]interface{}); ok {
				if blockType, _ := blockMap["type"].(string); blockType == "text" {
					if text, ok := blockMap["text"].(string); ok {
						text = strings.ReplaceAll(text, "\n", " ")
						text = strings.ReplaceAll(text, "\r", " ")
						for strings.Contains(text, "  ") {
							text = strings.ReplaceAll(text, "  ", " ")
						}
						return strings.TrimSpace(text)
					}
				}
			}
		}
	}
	return ""
}

// Base64 patterns to detect in JSON content
var (
	// Matches base64 in JSON fields like "data": "base64...", "bytes": "base64...", "content": "base64..."
	// Captures: field name, quote char, base64 content
	// Requires at least 100 chars and must contain typical base64 variety (not just one repeated char)
	base64FieldPattern = regexp.MustCompile(`("(?:data|bytes|content|image|file|attachment|b64|base64)"\s*:\s*")([A-Za-z0-9+/]{100,}={0,2})(")`)

	// Common media type patterns in JSON (e.g., "type": "image/png", "media_type": "application/pdf")
	mediaTypePattern = regexp.MustCompile(`"(?:type|media_type|content_type|mime_type)"\s*:\s*"([^"]+)"`)
)

// extractAndSaveAttachments extracts base64 attachments from body and saves them to separate files
// Returns the body with base64 replaced by file references
func (l *Logger) extractAndSaveAttachments(body string, timestamp time.Time) (string, error) {
	const minBase64Size = 1024 // 1KB - only extract attachments larger than this

	// If no attachment store is configured, fall back to old behavior
	if l.attachmentStore == nil {
		return l.extractAndSaveAttachmentsLegacy(body, timestamp)
	}

	// Extract media types from the JSON for context
	mediaTypes := make(map[int]string) // position -> media type
	for _, match := range mediaTypePattern.FindAllStringSubmatchIndex(body, -1) {
		if len(match) >= 4 {
			pos := match[0]
			mediaType := body[match[2]:match[3]]
			mediaTypes[pos] = mediaType
		}
	}

	// Find the closest media type before a given position
	findMediaType := func(pos int) string {
		bestPos := -1
		bestType := ""
		for mPos, mType := range mediaTypes {
			// Look for media type within 500 chars before the base64
			if mPos < pos && pos-mPos < 500 && mPos > bestPos {
				bestPos = mPos
				bestType = mType
			}
		}
		return bestType
	}

	// Replace base64 fields with attachment references
	result := base64FieldPattern.ReplaceAllStringFunc(body, func(match string) string {
		// Extract the parts
		parts := base64FieldPattern.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match // Shouldn't happen, but be safe
		}

		fieldPrefix := parts[1] // e.g., "data": "
		base64Content := parts[2]
		fieldSuffix := parts[3] // closing "

		// Only process if larger than minBase64Size
		if len(base64Content) < minBase64Size {
			return match
		}

		// Try to determine media type
		mediaType := "binary"
		matchPos := strings.Index(body, match)
		if matchPos >= 0 {
			if nearbyType := findMediaType(matchPos); nearbyType != "" {
				mediaType = nearbyType
			}
		}

		// Try to decode a sample to verify it's valid base64
		sampleSize := 100
		if len(base64Content) < sampleSize {
			sampleSize = len(base64Content)
		}
		if _, err := base64.StdEncoding.DecodeString(base64Content[:sampleSize]); err != nil {
			// Not valid base64, keep original
			return match
		}

		// Decode the full base64 content
		decodedData, err := base64.StdEncoding.DecodeString(base64Content)
		if err != nil {
			// Failed to decode, keep original
			return match
		}

		// Save to attachment store (with automatic deduplication)
		hash, _, err := l.attachmentStore.Save(decodedData, mediaType)
		if err != nil {
			// Failed to save, keep original
			return match
		}

		// Create attachment reference: @attachment:sha256:{first12}:{size}:{mediaType}
		sizeStr := config.FormatBytes(int64(len(decodedData)))
		hashPrefix := hash[:12] // Use first 12 chars of hash
		reference := fmt.Sprintf("@attachment:sha256:%s:%s:%s", hashPrefix, sizeStr, mediaType)

		return fieldPrefix + reference + fieldSuffix
	})

	return result, nil
}

// extractAndSaveAttachmentsLegacy is the old implementation for backward compatibility
func (l *Logger) extractAndSaveAttachmentsLegacy(body string, timestamp time.Time) (string, error) {
	const minBase64Size = 1024 // 1KB - only extract attachments larger than this

	// Get home directory for attachment storage
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return body, err
	}

	// Create attachments directory: ~/.claude2kiro/attachments/YYYY-MM-DD/
	dateDir := timestamp.Format("2006-01-02")
	attachmentDir := filepath.Join(homeDir, ".claude2kiro", "attachments", dateDir)
	if err := os.MkdirAll(attachmentDir, 0755); err != nil {
		return body, err
	}

	// Extract media types from the JSON for context
	mediaTypes := make(map[int]string) // position -> media type
	for _, match := range mediaTypePattern.FindAllStringSubmatchIndex(body, -1) {
		if len(match) >= 4 {
			pos := match[0]
			mediaType := body[match[2]:match[3]]
			mediaTypes[pos] = mediaType
		}
	}

	// Find the closest media type before a given position
	findMediaType := func(pos int) string {
		bestPos := -1
		bestType := ""
		for mPos, mType := range mediaTypes {
			// Look for media type within 500 chars before the base64
			if mPos < pos && pos-mPos < 500 && mPos > bestPos {
				bestPos = mPos
				bestType = mType
			}
		}
		return bestType
	}

	// Replace base64 fields with file references
	result := base64FieldPattern.ReplaceAllStringFunc(body, func(match string) string {
		// Extract the parts
		parts := base64FieldPattern.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match // Shouldn't happen, but be safe
		}

		fieldPrefix := parts[1] // e.g., "data": "
		base64Content := parts[2]
		fieldSuffix := parts[3] // closing "

		// Only process if larger than minBase64Size
		if len(base64Content) < minBase64Size {
			return match
		}

		// Try to determine media type
		mediaType := "binary"
		matchPos := strings.Index(body, match)
		if matchPos >= 0 {
			if nearbyType := findMediaType(matchPos); nearbyType != "" {
				mediaType = nearbyType
			}
		}

		// Try to decode a sample to verify it's valid base64
		sampleSize := 100
		if len(base64Content) < sampleSize {
			sampleSize = len(base64Content)
		}
		if _, err := base64.StdEncoding.DecodeString(base64Content[:sampleSize]); err != nil {
			// Not valid base64, keep original
			return match
		}

		// Decode the full base64 content
		decodedData, err := base64.StdEncoding.DecodeString(base64Content)
		if err != nil {
			// Failed to decode, keep original
			return match
		}

		// Determine file extension from media type
		ext := getFileExtension(mediaType)

		// Generate unique filename using counter
		l.attachmentCounter.mu.Lock()
		l.attachmentCounter.counter++
		fileNum := l.attachmentCounter.counter
		l.attachmentCounter.mu.Unlock()

		// Create filename: img_001.png, doc_002.pdf, etc.
		prefix := getFilePrefix(mediaType)
		filename := fmt.Sprintf("%s_%03d%s", prefix, fileNum, ext)
		filePath := filepath.Join(attachmentDir, filename)

		// Save the decoded data to file
		if err := os.WriteFile(filePath, decodedData, 0644); err != nil {
			// Failed to save, keep original
			return match
		}

		// Create relative path for reference: attachments/2026-01-02/img_001.png
		relativePath := filepath.Join("attachments", dateDir, filename)

		// Create file reference
		sizeStr := config.FormatBytes(int64(len(decodedData)))
		reference := fmt.Sprintf("[ATTACHMENT: %s %s]", relativePath, sizeStr)

		return fieldPrefix + reference + fieldSuffix
	})

	return result, nil
}

// getFileExtension returns the file extension for a media type
func getFileExtension(mediaType string) string {
	switch {
	case strings.HasPrefix(mediaType, "image/png"):
		return ".png"
	case strings.HasPrefix(mediaType, "image/jpeg"), strings.HasPrefix(mediaType, "image/jpg"):
		return ".jpg"
	case strings.HasPrefix(mediaType, "image/gif"):
		return ".gif"
	case strings.HasPrefix(mediaType, "image/webp"):
		return ".webp"
	case strings.HasPrefix(mediaType, "image/"):
		return ".img"
	case strings.HasPrefix(mediaType, "application/pdf"):
		return ".pdf"
	case strings.HasPrefix(mediaType, "video/mp4"):
		return ".mp4"
	case strings.HasPrefix(mediaType, "video/webm"):
		return ".webm"
	case strings.HasPrefix(mediaType, "video/"):
		return ".vid"
	case strings.HasPrefix(mediaType, "audio/mp3"):
		return ".mp3"
	case strings.HasPrefix(mediaType, "audio/wav"):
		return ".wav"
	case strings.HasPrefix(mediaType, "audio/"):
		return ".aud"
	case strings.HasPrefix(mediaType, "text/"):
		return ".txt"
	default:
		return ".bin"
	}
}

// getFilePrefix returns the filename prefix for a media type
func getFilePrefix(mediaType string) string {
	switch {
	case strings.HasPrefix(mediaType, "image/"):
		return "img"
	case strings.HasPrefix(mediaType, "application/pdf"):
		return "doc"
	case strings.HasPrefix(mediaType, "video/"):
		return "vid"
	case strings.HasPrefix(mediaType, "audio/"):
		return "aud"
	case strings.HasPrefix(mediaType, "text/"):
		return "txt"
	default:
		return "file"
	}
}

// replaceBase64WithPlaceholders scans for base64 content in JSON and replaces ALL base64 blobs with placeholders
// This preserves the conversation structure while removing binary data that causes memory/disk issues
func replaceBase64WithPlaceholders(body string, maxSizeKB int) string {
	if maxSizeKB <= 0 {
		return body // No limit
	}

	const minBase64Size = 1024 // 1KB - replace any base64 larger than this (regex requires 100 chars minimum anyway)

	// Extract media types from the JSON for context
	mediaTypes := make(map[int]string) // position -> media type
	for _, match := range mediaTypePattern.FindAllStringSubmatchIndex(body, -1) {
		if len(match) >= 4 {
			pos := match[0]
			mediaType := body[match[2]:match[3]]
			mediaTypes[pos] = mediaType
		}
	}

	// Find the closest media type before a given position
	findMediaType := func(pos int) string {
		bestPos := -1
		bestType := ""
		for mPos, mType := range mediaTypes {
			// Look for media type within 500 chars before the base64
			if mPos < pos && pos-mPos < 500 && mPos > bestPos {
				bestPos = mPos
				bestType = mType
			}
		}
		return bestType
	}

	// Replace base64 fields with placeholders
	result := base64FieldPattern.ReplaceAllStringFunc(body, func(match string) string {
		// Extract the parts
		parts := base64FieldPattern.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match // Shouldn't happen, but be safe
		}

		fieldPrefix := parts[1] // e.g., "data": "
		base64Content := parts[2]
		fieldSuffix := parts[3] // closing "

		// Only replace if larger than minBase64Size
		if len(base64Content) < minBase64Size {
			return match
		}

		// Try to determine media type and size
		mediaType := "binary"

		// Check for nearby media type declaration
		matchPos := strings.Index(body, match)
		if matchPos >= 0 {
			if nearbyType := findMediaType(matchPos); nearbyType != "" {
				mediaType = nearbyType
			}
		}

		// Try to decode a sample to verify it's valid base64
		// (prevents false positives on long alphanumeric strings)
		sampleSize := 100
		if len(base64Content) < sampleSize {
			sampleSize = len(base64Content)
		}
		if _, err := base64.StdEncoding.DecodeString(base64Content[:sampleSize]); err != nil {
			// Not valid base64, keep original
			return match
		}

		// Calculate approximate decoded size (base64 is ~1.33x the original)
		decodedSize := (len(base64Content) * 3) / 4

		// Determine attachment type based on media type
		attachmentType := "ATTACHMENT"
		if strings.HasPrefix(mediaType, "image/") {
			attachmentType = "IMAGE"
		} else if strings.HasPrefix(mediaType, "application/pdf") {
			attachmentType = "PDF"
		} else if strings.HasPrefix(mediaType, "video/") {
			attachmentType = "VIDEO"
		} else if strings.HasPrefix(mediaType, "audio/") {
			attachmentType = "AUDIO"
		}

		// Create placeholder
		placeholder := fmt.Sprintf("[%s: %s %s]",
			attachmentType,
			mediaType,
			config.FormatBytes(int64(decodedSize)),
		)

		return fieldPrefix + placeholder + fieldSuffix
	})

	// After replacing base64, check if we still need to truncate
	maxBytes := maxSizeKB * 1024
	if len(result) > maxBytes {
		// Still too large, apply traditional truncation
		truncated := result[:maxBytes]
		return truncated + fmt.Sprintf("\n\n[... TRUNCATED - original size: %s ...]", config.FormatBytes(int64(len(result))))
	}

	return result
}

// generateRequestID generates a unique request ID (6 hex chars)
func (l *Logger) generateRequestID() string {
	l.requestCount++
	return fmt.Sprintf("%06x", l.requestCount%0xFFFFFF)
}

// LogRequestResult contains the IDs generated for a logged request
type LogRequestResult struct {
	RequestID string // Hex ID for correlation (6 chars)
	SeqNum    int    // Sequential number for display (#01, #02)
}

// LogRequest is a convenience method for logging requests
// Returns the generated request ID and sequential number for correlation with the response
// status: 0 = OK (request parsed successfully), non-zero = error status code
// parseDuration: time taken to receive/parse the request
func (l *Logger) LogRequest(model, method, path, body, sessionID, fullUUID string, status int, parseDuration time.Duration) LogRequestResult {
	l.mu.Lock()
	requestID := l.generateRequestID()
	// Get or create sequential number for this session
	seqNum := l.seqNumMap[sessionID] + 1
	l.seqNumMap[sessionID] = seqNum
	l.mu.Unlock()

	cfg := config.Get()
	l.Log(LogEntry{
		Timestamp: time.Now(),
		Type:      LogTypeReq,
		Model:     model,
		Method:    method,
		Path:      path,
		Status:    status,
		Duration:  parseDuration,
		Preview:   generateRequestPreview(body, cfg.Logging.PreviewLength),
		Body:      body, // Full body - truncation happens in Log() for memory only
		SessionID: sessionID,
		FullUUID:  fullUUID,
		RequestID: requestID,
		BodySize:  len(body),
		SeqNum:    seqNum,
	})
	return LogRequestResult{RequestID: requestID, SeqNum: seqNum}
}

// LogResponse is a convenience method for logging responses
// sessionID, requestID, and seqNum are used for correlation with the request
func (l *Logger) LogResponse(status int, path string, duration time.Duration, responsePreview, sessionID, fullUUID, requestID string, seqNum int) {
	cfg := config.Get()
	l.Log(LogEntry{
		Timestamp: time.Now(),
		Type:      LogTypeRes,
		Status:    status,
		Path:      path,
		Duration:  duration,
		Preview:   sanitizePreview(responsePreview, cfg.Logging.PreviewLength),
		Body:      responsePreview, // Full body - truncation happens in Log() for memory only
		SessionID: sessionID,
		FullUUID:  fullUUID,
		RequestID: requestID,
		BodySize:  len(responsePreview),
		SeqNum:    seqNum,
	})
}

// LogResponseWithBody is like LogResponse but with separate preview and full body
// This allows proper parsing of responses where the preview (human-readable text) differs from the raw body (SSE/JSON)
func (l *Logger) LogResponseWithBody(status int, path string, duration time.Duration, preview, body, sessionID, fullUUID, requestID string, seqNum int) {
	cfg := config.Get()
	l.Log(LogEntry{
		Timestamp: time.Now(),
		Type:      LogTypeRes,
		Status:    status,
		Path:      path,
		Duration:  duration,
		Preview:   sanitizePreview(preview, cfg.Logging.PreviewLength),
		Body:      body,
		SessionID: sessionID,
		FullUUID:  fullUUID,
		RequestID: requestID,
		BodySize:  len(body),
		SeqNum:    seqNum,
	})
}

// LogError is a convenience method for logging errors
func (l *Logger) LogError(message string) {
	cfg := config.Get()
	l.Log(LogEntry{
		Timestamp: time.Now(),
		Type:      LogTypeErr,
		Preview:   sanitizePreview(message, cfg.Logging.PreviewLength),
		Body:      message,
	})
}

// LogInfo is a convenience method for logging info messages
func (l *Logger) LogInfo(message string) {
	cfg := config.Get()
	l.Log(LogEntry{
		Timestamp: time.Now(),
		Type:      LogTypeInf,
		Preview:   sanitizePreview(message, cfg.Logging.PreviewLength),
		Body:      message,
	})
}

// LogComparison is a convenience method for logging comparison mode messages
// sessionID and requestID are used for correlation with the request/response
func (l *Logger) LogComparison(sessionID, requestID, message string) {
	cfg := config.Get()
	l.Log(LogEntry{
		Timestamp: time.Now(),
		Type:      LogTypeCmp,
		Preview:   sanitizePreview(message, cfg.Logging.PreviewLength),
		Body:      message,
		SessionID: sessionID,
		RequestID: requestID,
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
	l.seqNumMap = make(map[string]int)
}

// Count returns the number of log entries
func (l *Logger) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.entries)
}

// EstimatedMemoryUsage returns the estimated total memory used by all entries in bytes
func (l *Logger) EstimatedMemoryUsage() int {
	l.mu.RLock()
	defer l.mu.RUnlock()

	total := 0
	for _, entry := range l.entries {
		total += entry.EstimatedMemorySize()
	}
	return total
}

// FilePath returns the current log file path, if file logging is enabled
func (l *Logger) FilePath() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.filePath
}

// LoadFromFile loads log entries from a file into the logger
// This is used to restore logs from previous sessions on startup
func (l *Logger) LoadFromFile(filePath string) (int, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	l.mu.Lock()
	defer l.mu.Unlock()

	// Use a reader that can handle very long lines
	reader := bufio.NewReader(file)
	count := 0

	for {
		// ReadString reads until delimiter, handles any line size
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				// Process last line if it doesn't end with newline
				if len(line) > 0 {
					line = strings.TrimSuffix(line, "\n")
					line = strings.TrimSuffix(line, "\r")
					if entry, ok := parsePlainLogLine(line); ok {
						if len(l.entries) >= l.maxEntries {
							l.entries = l.entries[1:]
						}
						l.entries = append(l.entries, entry)
						count++
					}
				}
			}
			break
		}

		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r") // Handle Windows line endings

		if entry, ok := parsePlainLogLine(line); ok {
			// Ring buffer: remove oldest if at capacity
			if len(l.entries) >= l.maxEntries {
				l.entries = l.entries[1:]
			}
			l.entries = append(l.entries, entry)
			count++
		}
	}

	return count, nil
}

// Regex patterns for parsing log lines
var (
	// Main log line pattern: [timestamp] [TYPE] details
	logLinePattern = regexp.MustCompile(`^\[([^\]]+)\] \[([A-Z]{3})\] (.*)$`)
	// Session:RequestID pattern: [session:reqid]
	sessionReqPattern = regexp.MustCompile(`^\[([^:]+):([^\]]+)\] (.*)$`)
	// RequestID only pattern: [reqid]
	reqOnlyPattern = regexp.MustCompile(`^\[([^\]]+)\] (.*)$`)
	// Response details: status path (duration)
	responsePattern = regexp.MustCompile(`^(\d+) ([^ ]+) \(([^)]+)\)$`)
)

// parsePlainLogLine parses a line from the plain text log format
func parsePlainLogLine(line string) (LogEntry, bool) {
	matches := logLinePattern.FindStringSubmatch(line)
	if matches == nil {
		return LogEntry{}, false
	}

	timestampStr := matches[1]
	typeStr := matches[2]
	details := matches[3]

	// Parse timestamp in local timezone (logs are written in local time)
	timestamp, err := time.ParseInLocation("2006-01-02 15:04:05.000", timestampStr, time.Local)
	if err != nil {
		return LogEntry{}, false
	}

	// Parse log type
	var logType LogType
	switch typeStr {
	case "REQ":
		logType = LogTypeReq
	case "RES":
		logType = LogTypeRes
	case "ERR":
		logType = LogTypeErr
	case "INF":
		logType = LogTypeInf
	case "CMP":
		logType = LogTypeCmp
	default:
		return LogEntry{}, false
	}

	entry := LogEntry{
		Timestamp: timestamp,
		Type:      logType,
	}

	// Split details and preview (separated by " | ")
	parts := strings.SplitN(details, " | ", 2)
	mainDetails := parts[0]
	if len(parts) > 1 {
		bodyContent := parts[1]
		// Check for @size:N@ marker at the start of body content
		// This preserves the original body size even when content was truncated
		if strings.HasPrefix(bodyContent, "@size:") {
			endIdx := strings.Index(bodyContent[6:], "@")
			if endIdx > 0 {
				sizeStr := bodyContent[6 : 6+endIdx]
				if size, err := strconv.Atoi(sizeStr); err == nil {
					entry.BodySize = size
				}
				// Remove the size marker from body content
				bodyContent = bodyContent[6+endIdx+1:]
			}
		}
		entry.Body = bodyContent
		// Generate appropriate preview based on log type
		// For very large bodies, use fast truncation to avoid slow parsing
		if len(bodyContent) > PreviewVeryLargeThreshold {
			// Just truncate for huge entries - parsing is too slow
			entry.Preview = sanitizePreview(bodyContent[:min(len(bodyContent), 500)], 200)
		} else if logType == LogTypeRes && strings.HasPrefix(bodyContent, "event:") {
			// RES with SSE format - extract text content for preview
			entry.Preview = extractSSETextPreview(bodyContent, 200)
		} else if logType == LogTypeReq && strings.HasPrefix(bodyContent, "{") {
			// REQ with JSON - generate meaningful preview
			entry.Preview = generateRequestPreview(bodyContent, 200)
		} else {
			entry.Preview = bodyContent
		}
	}

	// Parse based on log type
	switch logType {
	case LogTypeReq:
		parseRequestDetails(&entry, mainDetails)
	case LogTypeRes:
		parseResponseDetails(&entry, mainDetails)
	case LogTypeCmp:
		parseComparisonDetails(&entry, mainDetails)
	case LogTypeErr, LogTypeInf:
		entry.Preview = mainDetails
		entry.Body = mainDetails
	}

	// Set body size from parsed body content ONLY if not already set from @size marker
	// The @size marker contains the ORIGINAL body size before truncation, so it takes precedence
	if entry.BodySize == 0 && entry.Body != "" {
		entry.BodySize = len(entry.Body)
	}

	return entry, true
}

// deriveSeqNumFromRequestID tries to derive a display SeqNum from the hex RequestID
// This is used when loading entries from log files that don't have SeqNum stored
func deriveSeqNumFromRequestID(requestID string) int {
	if requestID == "" {
		return 0
	}
	// Parse hex string to int
	if val, err := strconv.ParseInt(requestID, 16, 64); err == nil {
		return int(val)
	}
	return 0
}

// parseRequestDetails parses request-specific details
func parseRequestDetails(entry *LogEntry, details string) {
	remaining := details

	// Check for [session:reqid] or [reqid] prefix
	if matches := sessionReqPattern.FindStringSubmatch(remaining); matches != nil {
		entry.SessionID = matches[1]
		entry.RequestID = matches[2]
		remaining = matches[3]
	} else if matches := reqOnlyPattern.FindStringSubmatch(remaining); matches != nil {
		entry.RequestID = matches[1]
		remaining = matches[2]
	}

	// Derive SeqNum from RequestID for display (for entries loaded from file)
	entry.SeqNum = deriveSeqNumFromRequestID(entry.RequestID)

	// Parse: model method path
	parts := strings.SplitN(remaining, " ", 3)
	if len(parts) >= 1 {
		entry.Model = parts[0]
	}
	if len(parts) >= 2 {
		entry.Method = parts[1]
	}
	if len(parts) >= 3 {
		entry.Path = parts[2]
	}
}

// parseResponseDetails parses response-specific details
func parseResponseDetails(entry *LogEntry, details string) {
	remaining := details

	// Check for [session:reqid] or [reqid] prefix
	if matches := sessionReqPattern.FindStringSubmatch(remaining); matches != nil {
		entry.SessionID = matches[1]
		entry.RequestID = matches[2]
		// Derive SeqNum from RequestID for display
		entry.SeqNum = deriveSeqNumFromRequestID(entry.RequestID)
		remaining = matches[3]
	} else if matches := reqOnlyPattern.FindStringSubmatch(remaining); matches != nil {
		entry.RequestID = matches[1]
		// Derive SeqNum from RequestID for display
		entry.SeqNum = deriveSeqNumFromRequestID(entry.RequestID)
		remaining = matches[2]
	}

	// Parse: status path (duration)
	if matches := responsePattern.FindStringSubmatch(remaining); matches != nil {
		entry.Status, _ = strconv.Atoi(matches[1])
		entry.Path = matches[2]
		entry.Duration, _ = time.ParseDuration(matches[3])
	}
}

// parseComparisonDetails parses comparison-specific details
func parseComparisonDetails(entry *LogEntry, details string) {
	remaining := details

	// Check for [session:reqid] or [reqid] prefix
	if matches := sessionReqPattern.FindStringSubmatch(remaining); matches != nil {
		entry.SessionID = matches[1]
		entry.RequestID = matches[2]
		// Derive SeqNum from RequestID for display
		entry.SeqNum = deriveSeqNumFromRequestID(entry.RequestID)
		remaining = matches[3]
	} else if matches := reqOnlyPattern.FindStringSubmatch(remaining); matches != nil {
		entry.RequestID = matches[1]
		// Derive SeqNum from RequestID for display
		entry.SeqNum = deriveSeqNumFromRequestID(entry.RequestID)
		remaining = matches[2]
	}

	// The rest is the message
	entry.Preview = remaining
	entry.Body = remaining
}

// GetUniqueSessions returns a list of unique session IDs from the log entries
func (l *Logger) GetUniqueSessions() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	seen := make(map[string]bool)
	var sessions []string

	for _, entry := range l.entries {
		if entry.SessionID != "" && !seen[entry.SessionID] {
			seen[entry.SessionID] = true
			sessions = append(sessions, entry.SessionID)
		}
	}

	return sessions
}

// GetEntriesBySession returns log entries filtered by session ID
// If sessionID is empty, returns all entries
func (l *Logger) GetEntriesBySession(sessionID string) []LogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if sessionID == "" {
		result := make([]LogEntry, len(l.entries))
		copy(result, l.entries)
		return result
	}

	var result []LogEntry
	for _, entry := range l.entries {
		if entry.SessionID == sessionID {
			result = append(result, entry)
		}
	}
	return result
}
