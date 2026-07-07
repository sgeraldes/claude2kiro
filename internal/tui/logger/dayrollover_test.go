package logger

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDayRollover verifies the active file switches to today's date-named log
// when the calendar day changes, so a proxy left running across midnight starts
// a fresh day-file instead of appending to yesterday's forever (which broke
// per-day retention and let "today's log" grow without bound).
func TestDayRollover(t *testing.T) {
	dir := t.TempDir()
	lg := NewLogger(10)
	if err := lg.EnableFileLogging(dir); err != nil {
		t.Fatal(err)
	}
	defer lg.DisableFileLogging()

	today := time.Now().Format("2006-01-02")
	if got := filepath.Base(lg.filePath); got != today+".log" {
		t.Fatalf("active file = %s, want %s.log", got, today)
	}

	// Simulate the proxy having started on a previous day: point the active
	// writer at a stale day-file, then a write-time roll should switch to today.
	lg.mu.Lock()
	stalePath := filepath.Join(dir, "2000-01-01.log")
	f, err := os.OpenFile(stalePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		lg.mu.Unlock()
		t.Fatal(err)
	}
	lg.fileWriter.Close()
	lg.fileWriter = f
	lg.filePath = stalePath
	lg.fileDate = "2000-01-01"
	lg.fileBytes = 123

	lg.maybeRollDateLocked()
	rolledPath, rolledDate, rolledBytes := lg.filePath, lg.fileDate, lg.fileBytes
	lg.mu.Unlock()

	if rolledDate != today {
		t.Fatalf("fileDate = %s after rollover, want %s", rolledDate, today)
	}
	if got := filepath.Base(rolledPath); got != today+".log" {
		t.Fatalf("active file = %s after rollover, want %s.log", got, today)
	}
	if rolledBytes != 0 {
		t.Fatalf("fileBytes = %d after rollover, want 0", rolledBytes)
	}
	if _, err := os.Stat(rolledPath); err != nil {
		t.Fatalf("today's log file must exist after rollover: %v", err)
	}
}
