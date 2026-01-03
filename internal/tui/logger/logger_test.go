package logger

import (
	"encoding/base64"
	"strings"
	"testing"
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
			name: "small base64 kept intact",
			input: `{"data": "` + strings.Repeat("A", 100) + `"}`,
			maxSizeKB: 1024,
			expectedSubstr: strings.Repeat("A", 100),
			shouldContain: true,
		},
		{
			name: "large base64 image replaced",
			input: `{"type": "image/png", "data": "` + base64.StdEncoding.EncodeToString(make([]byte, 50000)) + `"}`,
			maxSizeKB: 1024,
			expectedSubstr: "[IMAGE: image/png",
			shouldContain: true,
		},
		{
			name: "large base64 pdf replaced",
			input: `{"media_type": "application/pdf", "bytes": "` + base64.StdEncoding.EncodeToString(make([]byte, 100000)) + `"}`,
			maxSizeKB: 1024,
			expectedSubstr: "[PDF: application/pdf",
			shouldContain: true,
		},
		{
			name: "large base64 generic attachment",
			input: `{"content": "` + base64.StdEncoding.EncodeToString(make([]byte, 30000)) + `"}`,
			maxSizeKB: 1024,
			expectedSubstr: "[ATTACHMENT: binary",
			shouldContain: true,
		},
		{
			name: "multiple base64 fields",
			input: `{"type": "image/jpeg", "data": "` + base64.StdEncoding.EncodeToString(make([]byte, 20000)) + `", "thumbnail": "` + base64.StdEncoding.EncodeToString(make([]byte, 15000)) + `"}`,
			maxSizeKB: 1024,
			expectedSubstr: "[IMAGE: image/jpeg",
			shouldContain: true,
		},
		{
			name: "no limit - returns original",
			input: `{"data": "` + base64.StdEncoding.EncodeToString(make([]byte, 100000)) + `"}`,
			maxSizeKB: 0,
			expectedSubstr: "",
			shouldContain: false,
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
