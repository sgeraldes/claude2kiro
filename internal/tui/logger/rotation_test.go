package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/config"
)

// TestIntraDayRotation verifies the active log file rolls to a sibling once it
// exceeds half the configured cap, so a single busy day can't grow one file
// without bound (the startup-only enforceSizeCap never touches the active file).
func TestIntraDayRotation(t *testing.T) {
	dir := t.TempDir()

	prev := config.Get()
	cfg := *prev
	cfg.Logging.MaxLogSizeMB = 1        // 1 MB total → rotate the active file at 512 KB
	cfg.Logging.CompressRotated = false // keep rotated file as .log for a deterministic assert
	cfg.Logging.FileRetention = "unlimited"
	config.Set(&cfg)
	t.Cleanup(func() { config.Set(prev) })

	lg := NewLogger(10)
	if err := lg.EnableFileLogging(dir); err != nil {
		t.Fatal(err)
	}
	defer lg.DisableFileLogging()
	active := lg.filePath

	// Inflate the active file past the 512 KB rotation threshold, then trigger.
	lg.mu.Lock()
	blob := strings.Repeat("x", 1024)
	for i := 0; i < 600; i++ { // ~600 KB > 512 KB
		n, _ := lg.fileWriter.WriteString(blob)
		lg.fileBytes += int64(n)
	}
	lg.maybeRotateLocked()
	sizeAfter := lg.fileBytes
	lg.mu.Unlock()

	if sizeAfter != 0 {
		t.Fatalf("fileBytes should reset to 0 after rotation, got %d", sizeAfter)
	}

	// A rotated sibling must exist and the active file must be fresh/small.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	rotated := 0
	for _, e := range entries {
		if e.Name() == filepath.Base(active) || !isLogFile(e.Name()) {
			continue
		}
		rotated++
	}
	if rotated == 0 {
		t.Fatalf("expected a rotated log file in %s, found none", dir)
	}
	if info, err := os.Stat(active); err != nil {
		t.Fatalf("active file must be reopened after rotation: %v", err)
	} else if info.Size() > 4096 {
		t.Fatalf("active file should be fresh after rotation, size=%d", info.Size())
	}

	// Total-size cap (1 MB) must still hold for THIS dir: rotated ~600 KB +
	// fresh active < 1 MB. (config.GetLogDirSize scans the configured log dir,
	// not this temp dir, so sum the temp dir directly.)
	var total int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	if total > 1024*1024 {
		t.Fatalf("dir total %d exceeds the 1 MB cap after rotation", total)
	}
}
