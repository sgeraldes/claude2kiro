package logger

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReplaceBase64WithPlaceholders(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		maxSizeKB      int
		expectedSubstr string // substring we expect to find in the result
		shouldContain  bool   // whether base64 should be replaced
	}{
		{
			name:           "small base64 kept intact",
			input:          `{"data": "` + strings.Repeat("A", 100) + `"}`,
			maxSizeKB:      1024,
			expectedSubstr: strings.Repeat("A", 100),
			shouldContain:  true,
		},
		{
			name:           "large base64 image replaced",
			input:          `{"type": "image/png", "data": "` + base64.StdEncoding.EncodeToString(make([]byte, 50000)) + `"}`,
			maxSizeKB:      1024,
			expectedSubstr: "[IMAGE: image/png",
			shouldContain:  true,
		},
		{
			name:           "large base64 pdf replaced",
			input:          `{"media_type": "application/pdf", "bytes": "` + base64.StdEncoding.EncodeToString(make([]byte, 100000)) + `"}`,
			maxSizeKB:      1024,
			expectedSubstr: "[PDF: application/pdf",
			shouldContain:  true,
		},
		{
			name:           "large base64 generic attachment",
			input:          `{"content": "` + base64.StdEncoding.EncodeToString(make([]byte, 30000)) + `"}`,
			maxSizeKB:      1024,
			expectedSubstr: "[ATTACHMENT: binary",
			shouldContain:  true,
		},
		{
			name:           "multiple base64 fields",
			input:          `{"type": "image/jpeg", "data": "` + base64.StdEncoding.EncodeToString(make([]byte, 20000)) + `", "thumbnail": "` + base64.StdEncoding.EncodeToString(make([]byte, 15000)) + `"}`,
			maxSizeKB:      1024,
			expectedSubstr: "[IMAGE: image/jpeg",
			shouldContain:  true,
		},
		{
			name:           "no limit - returns original",
			input:          `{"data": "` + base64.StdEncoding.EncodeToString(make([]byte, 100000)) + `"}`,
			maxSizeKB:      0,
			expectedSubstr: "",
			shouldContain:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := replaceBase64WithPlaceholders(tt.input, tt.maxSizeKB)

			if tt.shouldContain {
				if !strings.Contains(result, tt.expectedSubstr) {
					t.Errorf("Expected result to contain %q, but it didn't.\nResult: %s", tt.expectedSubstr, result)
				}

				// Verify that the large base64 was actually replaced (result should be much smaller)
				if tt.maxSizeKB > 0 && len(result) > len(tt.input)/2 && strings.Contains(tt.input, "EncodeToString") {
					// For large base64, result should be significantly smaller
					// (This is a heuristic - if we encoded 50KB+, result should be < 1KB after replacement)
					if strings.Contains(tt.name, "large") && len(result) > 10000 {
						t.Errorf("Expected result to be much smaller after base64 replacement. Got %d bytes", len(result))
					}
				}
			} else {
				// No limit case - should return original
				if result != tt.input {
					t.Errorf("Expected original input when maxSizeKB=0")
				}
			}
		})
	}
}

func TestReplaceBase64PreservesStructure(t *testing.T) {
	// Test that JSON structure is preserved, only base64 is replaced
	input := `{
		"model": "claude-sonnet-4-20250514",
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "text",
						"text": "What's in this image?"
					},
					{
						"type": "image",
						"source": {
							"type": "base64",
							"media_type": "image/png",
							"data": "` + base64.StdEncoding.EncodeToString(make([]byte, 50000)) + `"
						}
					}
				]
			}
		]
	}`

	result := replaceBase64WithPlaceholders(input, 1024)

	// Verify structure is intact
	if !strings.Contains(result, `"model": "claude-sonnet-4-20250514"`) {
		t.Error("Model field was lost")
	}
	if !strings.Contains(result, `"role": "user"`) {
		t.Error("Role field was lost")
	}
	if !strings.Contains(result, `"text": "What's in this image?"`) {
		t.Error("Text content was lost")
	}
	if !strings.Contains(result, `"type": "image"`) {
		t.Error("Image type was lost")
	}
	if !strings.Contains(result, `"media_type": "image/png"`) {
		t.Error("Media type was lost")
	}

	// Verify base64 was replaced
	if !strings.Contains(result, "[IMAGE: image/png") {
		t.Error("Base64 was not replaced with placeholder")
	}

	// Verify the original huge base64 is NOT in the result
	if strings.Contains(result, base64.StdEncoding.EncodeToString(make([]byte, 50000))) {
		t.Error("Original large base64 is still present")
	}
}

func TestReplaceBase64MediaTypes(t *testing.T) {
	tests := []struct {
		mediaType    string
		expectedType string
	}{
		{"image/png", "IMAGE"},
		{"image/jpeg", "IMAGE"},
		{"application/pdf", "PDF"},
		{"video/mp4", "VIDEO"},
		{"audio/mp3", "AUDIO"},
		{"application/octet-stream", "ATTACHMENT"},
	}

	for _, tt := range tests {
		t.Run(tt.mediaType, func(t *testing.T) {
			input := `{"type": "` + tt.mediaType + `", "data": "` + base64.StdEncoding.EncodeToString(make([]byte, 20000)) + `"}`
			result := replaceBase64WithPlaceholders(input, 1024)

			expected := "[" + tt.expectedType + ": " + tt.mediaType
			if !strings.Contains(result, expected) {
				t.Errorf("Expected %q in result, got: %s", expected, result)
			}
		})
	}
}

func TestReplaceBase64InvalidBase64Ignored(t *testing.T) {
	// Test that strings that look like base64 but aren't are not replaced
	// Use characters that can appear in base64 but don't form valid base64
	input := `{"data": "` + strings.Repeat("!!!!", 4000) + `"}`
	result := replaceBase64WithPlaceholders(input, 1024)

	// Should not contain placeholder (not valid base64 - has invalid chars)
	if strings.Contains(result, "[ATTACHMENT:") || strings.Contains(result, "[IMAGE:") {
		t.Error("Invalid base64 was incorrectly replaced")
	}

	// The original string should be preserved or truncated normally
	if !strings.Contains(result, "!!!!") && !strings.Contains(result, "TRUNCATED") {
		t.Error("Expected invalid base64 to be kept or truncated normally")
	}
}

func TestCommonPrefixLen(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 3},
		{"abcXYZ", "abcDEF", 3},
		{"abc", "abcdef", 3},
		{"x", "y", 0},
	}
	for _, c := range cases {
		if got := commonPrefixLen(c.a, c.b); got != c.want {
			t.Errorf("commonPrefixLen(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestRequestDelta(t *testing.T) {
	// Build a long shared history so collapsing is worthwhile.
	hist := strings.Repeat("conversation-history-message ", 50) // ~1450 bytes
	prev := `{"messages":[` + hist + `]}`
	// Next request appends a new turn (shares the long prefix).
	body := `{"messages":[` + hist + `new-user-turn]}`

	d := requestDelta(prev, "abc123", body)
	if d == "" {
		t.Fatalf("expected a delta for a long shared prefix, got full-body sentinel")
	}
	if !strings.HasPrefix(d, "@delta prev=abc123 shared=") {
		t.Errorf("delta missing marker: %.60s", d)
	}
	// The delta must be much smaller than the full body.
	if len(d) >= len(body) {
		t.Errorf("delta (%d) not smaller than body (%d)", len(d), len(body))
	}
	// Reconstruct: prev[:shared] + tail == body. The tail follows the second
	// '@' in the marker.
	at := strings.IndexByte(d[1:], '@') // first '@' after the leading one
	tail := d[at+2:]
	sh := commonPrefixLen(prev, body)
	if prev[:sh]+tail != body {
		t.Errorf("reconstruction failed: prev[:%d]+tail != body", sh)
	}
}

func TestRequestDeltaShortPrefixReturnsFull(t *testing.T) {
	// Tiny / low-overlap bodies should NOT be collapsed (return "").
	if d := requestDelta(`{"a":1}`, "id", `{"b":2}`); d != "" {
		t.Errorf("expected full-body sentinel for short prefix, got %q", d)
	}
}

func TestParseRetentionDays(t *testing.T) {
	cases := map[string]int{
		"7d": 7, "30d": 30, "90d": 90, "14": 14,
		"unlimited": 0, "": 0, "0": 0, "garbage": 0, "-5d": 0,
	}
	for in, want := range cases {
		if got := parseRetentionDays(in); got != want {
			t.Errorf("parseRetentionDays(%q)=%d want %d", in, got, want)
		}
	}
}

func TestGzipRoundtripLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "2026-01-01.log")
	// Write a couple of real log lines via FormatPlain so the parser accepts them.
	e1 := LogEntry{Timestamp: time.Now(), Type: LogTypeInf, Preview: "hello one"}
	e2 := LogEntry{Timestamp: time.Now(), Type: LogTypeInf, Preview: "hello two"}
	content := e1.FormatPlain() + "\n" + e2.FormatPlain() + "\n"
	if err := os.WriteFile(plain, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Compress it (simulating startup rotation) and confirm the plain file is gone.
	gzipFile(plain)
	if _, err := os.Stat(plain); !os.IsNotExist(err) {
		t.Fatalf("plain file should be removed after gzip, stat err=%v", err)
	}
	if _, err := os.Stat(plain + ".gz"); err != nil {
		t.Fatalf("gz file should exist: %v", err)
	}

	// LoadFromFile given the PLAIN path must transparently read the .gz.
	lg := NewLogger(100)
	n, err := lg.LoadFromFile(plain)
	if err != nil {
		t.Fatalf("LoadFromFile(plain) should fall back to .gz: %v", err)
	}
	if n != 2 {
		t.Errorf("loaded %d entries from gz, want 2", n)
	}
}

func TestEnforceSizeCapKeepsActive(t *testing.T) {
	dir := t.TempDir()
	// Three files; make the "active" one newest and oversized to prove it's kept.
	write := func(name string, size int, age time.Duration) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, make([]byte, size), 0644)
		mt := time.Now().Add(-age)
		os.Chtimes(p, mt, mt)
		return p
	}
	write("2026-01-01.log", 1000, 72*time.Hour) // oldest
	write("2026-01-02.log", 1000, 48*time.Hour)
	active := write("2026-01-03.log", 5000, 0) // newest + largest = active

	// Cap below total; only non-active oldest files may be deleted.
	enforceSizeCap(dir, filepath.Base(active), 5000)

	if _, err := os.Stat(active); err != nil {
		t.Errorf("active file must never be deleted: %v", err)
	}
}
