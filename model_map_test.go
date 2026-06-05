package main

import "testing"

func TestGetKiroModelID_Claude48(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"claude-sonnet-4-8", "claude-sonnet-4.8"},
		{"claude-sonnet-4.8", "claude-sonnet-4.8"},
		{"claude-opus-4-8", "claude-opus-4.8"},
		{"claude-opus-4.8", "claude-opus-4.8"},
		{"claude-haiku-4-8", "claude-haiku-4.8"},
		{"claude-haiku-4.8", "claude-haiku-4.8"},
		{"claude-sonnet-4-7", "claude-sonnet-4.7"},
		{"claude-opus-4-7", "claude-opus-4.7"},
		{"claude-haiku-4-7", "claude-haiku-4.7"},
	}

	for _, tt := range tests {
		got := getKiroModelID(tt.input)
		if got != tt.expected {
			t.Errorf("getKiroModelID(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
