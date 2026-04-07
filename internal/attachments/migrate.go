package attachments

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MigrationResult contains statistics from migrating a log file
type MigrationResult struct {
	FilePath          string
	OriginalSize      int64
	NewSize           int64
	AttachmentsFound  int
	UniqueAttachments int
	DuplicatesRemoved int
	SpaceSaved        int64
}

// Base64 patterns to detect in JSON content (same as logger.go)
var (
	// Matches base64 in JSON fields like "data": "base64...", "bytes": "base64...", "content": "base64..."
	base64FieldPattern = regexp.MustCompile(`("(?:data|bytes|content|image|file|attachment|b64|base64)"\s*:\s*")([A-Za-z0-9+/]{100,}={0,2})(")`)

	// Common media type patterns in JSON (e.g., "type": "image/png", "media_type": "application/pdf")
	mediaTypePattern = regexp.MustCompile(`"(?:type|media_type|content_type|mime_type)"\s*:\s*"([^"]+)"`)
)

// MigrateLogFile processes a single log file, extracting base64 content to the store
// and replacing with references. The original file is replaced atomically.
func MigrateLogFile(logPath string, store *Store) (*MigrationResult, error) {
	// Get original file size
	fileInfo, err := os.Stat(logPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}
	originalSize := fileInfo.Size()

	// Open log file for reading
	inFile, err := os.Open(logPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer inFile.Close()

	// Create temp output file in same directory
	tempPath := logPath + ".tmp"
	outFile, err := os.Create(tempPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer func() {
		outFile.Close()
		// Clean up temp file if we error out
		if err != nil {
			os.Remove(tempPath)
		}
	}()

	// Track statistics
	result := &MigrationResult{
		FilePath:     logPath,
		OriginalSize: originalSize,
	}
	seenHashes := make(map[string]bool)

	// Process file line by line with large buffer
	reader := bufio.NewReaderSize(inFile, 1024*1024) // 1MB buffer
	writer := bufio.NewWriter(outFile)
	defer writer.Flush()

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				// Process last line if it doesn't end with newline
				if len(line) > 0 {
					processedLine := processLine(line, store, result, seenHashes)
					writer.WriteString(processedLine)
				}
				break
			}
			return nil, fmt.Errorf("failed to read line: %w", err)
		}

		processedLine := processLine(line, store, result, seenHashes)
		if _, err := writer.WriteString(processedLine); err != nil {
			return nil, fmt.Errorf("failed to write line: %w", err)
		}
	}

	// Flush writer before getting file size
	if err := writer.Flush(); err != nil {
		return nil, fmt.Errorf("failed to flush output: %w", err)
	}

	// Get new file size
	tempInfo, err := os.Stat(tempPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat temp file: %w", err)
	}
	result.NewSize = tempInfo.Size()
	result.SpaceSaved = originalSize - result.NewSize

	// Close files before rename
	outFile.Close()
	inFile.Close()

	// Atomically replace original file with temp file
	if err := os.Rename(tempPath, logPath); err != nil {
		return nil, fmt.Errorf("failed to replace original file: %w", err)
	}

	return result, nil
}

// processLine processes a single log line, extracting base64 attachments
func processLine(line string, store *Store, result *MigrationResult, seenHashes map[string]bool) string {
	// Quick check: does this line even look like it might have base64?
	hasBase64Field := strings.Contains(line, `"data"`) ||
		strings.Contains(line, `"bytes"`) ||
		strings.Contains(line, `"content"`) ||
		strings.Contains(line, `"image"`) ||
		strings.Contains(line, `"file"`) ||
		strings.Contains(line, `"attachment"`) ||
		strings.Contains(line, `"b64"`) ||
		strings.Contains(line, `"base64"`)

	if !hasBase64Field {
		return line
	}

	// Extract media types from the line for context
	mediaTypes := make(map[int]string) // position -> media type
	for _, match := range mediaTypePattern.FindAllStringSubmatchIndex(line, -1) {
		if len(match) >= 4 {
			pos := match[0]
			mediaType := line[match[2]:match[3]]
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
	processedLine := base64FieldPattern.ReplaceAllStringFunc(line, func(match string) string {
		// Extract the parts
		parts := base64FieldPattern.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}

		fieldPrefix := parts[1] // e.g., "data": "
		base64Content := parts[2]
		fieldSuffix := parts[3] // closing "

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

		// Determine media type before saving
		mediaType := "binary"
		matchPos := strings.Index(line, match)
		if matchPos >= 0 {
			if nearbyType := findMediaType(matchPos); nearbyType != "" {
				mediaType = nearbyType
			}
		}

		// Save to store and get hash
		hash, isNew, err := store.Save(decodedData, mediaType)
		if err != nil {
			// Failed to save, keep original
			return match
		}

		// Track statistics
		result.AttachmentsFound++
		if isNew {
			result.UniqueAttachments++
			seenHashes[hash] = true
		} else {
			if !seenHashes[hash] {
				result.UniqueAttachments++
				seenHashes[hash] = true
			} else {
				result.DuplicatesRemoved++
			}
		}

		// Create attachment reference
		sizeStr := FormatBytes(int64(len(decodedData)))
		reference := fmt.Sprintf("@attachment:sha256:%s:%s:%s", hash, sizeStr, mediaType)

		return fieldPrefix + reference + fieldSuffix
	})

	return processedLine
}

// MigrateAllLogs processes all log files in a directory
func MigrateAllLogs(logDir string, store *Store) ([]*MigrationResult, error) {
	// Find all .log files in the directory
	files, err := filepath.Glob(filepath.Join(logDir, "*.log"))
	if err != nil {
		return nil, fmt.Errorf("failed to list log files: %w", err)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no log files found in %s", logDir)
	}

	var results []*MigrationResult
	for _, logPath := range files {
		result, err := MigrateLogFile(logPath, store)
		if err != nil {
			// Log error but continue with other files
			fmt.Fprintf(os.Stderr, "Error migrating %s: %v\n", logPath, err)
			continue
		}
		results = append(results, result)
	}

	return results, nil
}

// FormatMigrationSummary formats a migration result for display
func (r *MigrationResult) FormatSummary() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("File: %s\n", filepath.Base(r.FilePath)))
	sb.WriteString(fmt.Sprintf("  Original size:      %s\n", FormatBytes(r.OriginalSize)))
	sb.WriteString(fmt.Sprintf("  New size:           %s\n", FormatBytes(r.NewSize)))
	sb.WriteString(fmt.Sprintf("  Space saved:        %s (%.1f%%)\n",
		FormatBytes(r.SpaceSaved),
		float64(r.SpaceSaved)/float64(r.OriginalSize)*100))
	sb.WriteString(fmt.Sprintf("  Attachments found:  %d\n", r.AttachmentsFound))
	sb.WriteString(fmt.Sprintf("  Unique:             %d\n", r.UniqueAttachments))
	sb.WriteString(fmt.Sprintf("  Duplicates:         %d\n", r.DuplicatesRemoved))
	return sb.String()
}

// FormatBytes formats a byte count as a human-readable string
func FormatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%dB", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
