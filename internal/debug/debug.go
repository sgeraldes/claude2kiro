package debug

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GetDebugDir returns the secure debug directory path.
// Creates directory with restrictive permissions if it doesn't exist.
// Uses ~/.claude2kiro/debug/ instead of system temp directory.
func GetDebugDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	debugDir := filepath.Join(homeDir, ".claude2kiro", "debug")

	// Create directory with 0700 (user-only access)
	if err := os.MkdirAll(debugDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create debug directory: %w", err)
	}

	return debugDir, nil
}

// RandomFilename generates a filename with random component for unpredictability
func RandomFilename(prefix string, ext string) string {
	timestamp := time.Now().Format("20060102-150405")
	randomBytes := make([]byte, 8)
	rand.Read(randomBytes)
	randomHex := hex.EncodeToString(randomBytes)

	return fmt.Sprintf("%s-%s-%s.%s", prefix, timestamp, randomHex, ext)
}

// WriteDebugFile writes data to debug directory with secure permissions
func WriteDebugFile(prefix string, data []byte) (string, error) {
	debugDir, err := GetDebugDir()
	if err != nil {
		return "", err
	}

	filename := RandomFilename(prefix, "json")
	filePath := filepath.Join(debugDir, filename)

	// Write with 0600 (user read/write only)
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		return "", fmt.Errorf("failed to write debug file: %w", err)
	}

	return filePath, nil
}

// CleanOldDebugFiles removes debug files older than maxAge
func CleanOldDebugFiles(maxAge time.Duration) error {
	debugDir, err := GetDebugDir()
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(debugDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No directory, nothing to clean
		}
		return fmt.Errorf("failed to read debug directory: %w", err)
	}

	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			path := filepath.Join(debugDir, entry.Name())
			os.Remove(path) // Ignore errors
		}
	}

	return nil
}

// ScrubSensitiveData removes sensitive fields from a map before writing
func ScrubSensitiveData(data map[string]interface{}) {
	// Sensitive keys to redact (case-insensitive check)
	sensitiveKeys := []string{
		"authorization",
		"x-api-key",
		"api-key",
		"apikey",
		"token",
		"access_token",
		"refresh_token",
		"password",
		"secret",
		"credential",
		"bearer",
	}

	for key, value := range data {
		keyLower := strings.ToLower(key)

		// Check if this key should be redacted
		for _, sensitive := range sensitiveKeys {
			if strings.Contains(keyLower, sensitive) {
				data[key] = "[REDACTED]"
				break
			}
		}

		// Recursively scrub nested objects
		if nested, ok := value.(map[string]interface{}); ok {
			ScrubSensitiveData(nested)
		}

		// Handle arrays of objects
		if nestedArray, ok := value.([]interface{}); ok {
			for _, item := range nestedArray {
				if nestedMap, ok := item.(map[string]interface{}); ok {
					ScrubSensitiveData(nestedMap)
				}
			}
		}
	}
}

// WriteDebugFileWithScrub writes data to debug directory after scrubbing sensitive fields
func WriteDebugFileWithScrub(prefix string, rawData []byte) (string, error) {
	var data map[string]interface{}
	if err := json.Unmarshal(rawData, &data); err != nil {
		// If not valid JSON, write as-is
		return WriteDebugFile(prefix, rawData)
	}

	ScrubSensitiveData(data)

	scrubbedData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return WriteDebugFile(prefix, rawData)
	}

	return WriteDebugFile(prefix, scrubbedData)
}
