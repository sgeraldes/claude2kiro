package logger

import (
	"encoding/base64"
	"fmt"
	"testing"
)

// TestRealWorldScenario demonstrates the function with a realistic multi-image request
func TestRealWorldScenario(t *testing.T) {
	// Simulate a request with multiple images and text
	img1 := base64.StdEncoding.EncodeToString(make([]byte, 100000)) // 100KB
	img2 := base64.StdEncoding.EncodeToString(make([]byte, 200000)) // 200KB
	pdf := base64.StdEncoding.EncodeToString(make([]byte, 500000))  // 500KB

	request := fmt.Sprintf(`{
  "model": "claude-sonnet-4-20250514",
  "messages": [
    {
      "role": "user",
      "content": [
        {"type": "text", "text": "Analyze these documents"},
        {"type": "image", "source": {"type": "base64", "media_type": "image/jpeg", "data": "%s"}},
        {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "%s"}},
        {"type": "document", "source": {"type": "base64", "media_type": "application/pdf", "data": "%s"}}
      ]
    }
  ]
}`, img1, img2, pdf)

	originalSize := len(request)
	result := replaceBase64WithPlaceholders(request, 2048) // 2MB limit
	resultSize := len(result)

	t.Logf("Original size: %d bytes (%.2f MB)", originalSize, float64(originalSize)/1024/1024)
	t.Logf("Result size: %d bytes (%.2f KB)", resultSize, float64(resultSize)/1024)
	t.Logf("Reduction: %.1f%%", (1-float64(resultSize)/float64(originalSize))*100)

	// Verify all placeholders are present
	if !containsString(result, "[IMAGE: image/jpeg") {
		t.Error("Missing JPEG placeholder")
	}
	if !containsString(result, "[IMAGE: image/png") {
		t.Error("Missing PNG placeholder")
	}
	if !containsString(result, "[PDF: application/pdf") {
		t.Error("Missing PDF placeholder")
	}

	// Verify structure is intact
	if !containsString(result, `"type": "text"`) {
		t.Error("Text type missing")
	}
	if !containsString(result, `"text": "Analyze these documents"`) {
		t.Error("Text content missing")
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && findString(s, substr)
}

func findString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
