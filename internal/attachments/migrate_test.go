package attachments

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateLogFile_WithBase64(t *testing.T) {
	tmpDir := t.TempDir()
	storeDir := filepath.Join(tmpDir, "store")
	logDir := filepath.Join(tmpDir, "logs")
	os.MkdirAll(logDir, 0755)

	store, err := NewStore(storeDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	testData := strings.Repeat("test image data", 100)
	testBase64 := base64.StdEncoding.EncodeToString([]byte(testData))

	logContent := fmt.Sprintf("[2026-01-02 10:30:00.000] [REQ] test | {\"type\": \"image/png\", \"data\": \"%s\"}\n", testBase64)
	logContent += "[2026-01-02 10:30:01.000] [RES] test | {\"status\": \"ok\"}\n"

	logFile := filepath.Join(logDir, "test.log")
	if err := os.WriteFile(logFile, []byte(logContent), 0644); err != nil {
		t.Fatalf("Failed to write log file: %v", err)
	}

	result, err := MigrateLogFile(logFile, store)
	if err != nil {
		t.Fatalf("Migration failed: %v", err)
	}

	if result.AttachmentsFound == 0 {
		t.Error("Expected at least 1 attachment found")
	}

	migratedContent, _ := os.ReadFile(logFile)
	migratedStr := string(migratedContent)
	if strings.Contains(migratedStr, testBase64) {
		t.Error("Base64 data should be replaced")
	}
	if !strings.Contains(migratedStr, "sha256:") {
		t.Error("Should contain sha256 hash reference")
	}
}

func TestMigrateLogFile_WithDuplicates(t *testing.T) {
	tmpDir := t.TempDir()
	storeDir := filepath.Join(tmpDir, "store")
	logDir := filepath.Join(tmpDir, "logs")
	os.MkdirAll(logDir, 0755)

	store, err := NewStore(storeDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	sameImg := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("duplicate", 100)))

	logContent := fmt.Sprintf("[2026-01-02 10:30:00.000] [REQ] s1 | {\"type\": \"image/png\", \"data\": \"%s\"}\n", sameImg)
	logContent += fmt.Sprintf("[2026-01-02 10:30:01.000] [REQ] s2 | {\"type\": \"image/png\", \"data\": \"%s\"}\n", sameImg)
	logContent += fmt.Sprintf("[2026-01-02 10:30:02.000] [REQ] s3 | {\"type\": \"image/png\", \"data\": \"%s\"}\n", sameImg)

	logFile := filepath.Join(logDir, "test.log")
	os.WriteFile(logFile, []byte(logContent), 0644)

	result, err := MigrateLogFile(logFile, store)
	if err != nil {
		t.Fatalf("Migration failed: %v", err)
	}

	if result.UniqueAttachments != 1 {
		t.Errorf("Expected 1 unique attachment, got %d", result.UniqueAttachments)
	}

	if result.DuplicatesRemoved < 2 {
		t.Errorf("Expected at least 2 duplicates removed, got %d", result.DuplicatesRemoved)
	}

	if store.manifest.Stats.TotalFiles != 1 {
		t.Errorf("Expected 1 unique file due to deduplication, got %d", store.manifest.Stats.TotalFiles)
	}
}

func TestMigrateAllLogs(t *testing.T) {
	tmpDir := t.TempDir()
	storeDir := filepath.Join(tmpDir, "store")
	logDir := filepath.Join(tmpDir, "logs")
	os.MkdirAll(logDir, 0755)

	store, err := NewStore(storeDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	for i := 1; i <= 3; i++ {
		img := base64.StdEncoding.EncodeToString([]byte(strings.Repeat(fmt.Sprintf("img%d", i), 100)))
		content := fmt.Sprintf("[2026-01-02 10:30:00.000] [REQ] s%d | {\"type\": \"image/png\", \"data\": \"%s\"}\n", i, img)

		logFile := filepath.Join(logDir, fmt.Sprintf("test%d.log", i))
		os.WriteFile(logFile, []byte(content), 0644)
	}

	results, err := MigrateAllLogs(logDir, store)
	if err != nil {
		t.Fatalf("Migration failed: %v", err)
	}

	if len(results) != 3 {
		t.Errorf("Expected 3 results, got %d", len(results))
	}
}

func TestMigrateLogFile_CalculatesStats(t *testing.T) {
	tmpDir := t.TempDir()
	storeDir := filepath.Join(tmpDir, "store")
	logDir := filepath.Join(tmpDir, "logs")
	os.MkdirAll(logDir, 0755)

	store, err := NewStore(storeDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	largeData := make([]byte, 100000)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}
	largeBase64 := base64.StdEncoding.EncodeToString(largeData)

	logContent := fmt.Sprintf("[2026-01-02 10:30:00.000] [REQ] s1 | {\"type\": \"image/png\", \"data\": \"%s\"}\n", largeBase64)

	logFile := filepath.Join(logDir, "test.log")
	os.WriteFile(logFile, []byte(logContent), 0644)

	result, err := MigrateLogFile(logFile, store)
	if err != nil {
		t.Fatalf("Migration failed: %v", err)
	}

	if result.SpaceSaved == 0 {
		t.Error("SpaceSaved should not be 0")
	}

	summary := result.FormatSummary()
	if !strings.Contains(summary, "100KB") && !strings.Contains(summary, "KB") {
		t.Errorf("Summary should contain space saved info, got: %s", summary)
	}
}
