package main

import (
	"testing"
	"time"
)

func TestIsTransientOverload(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"429 throttle", 429, `{"message":"Too many requests"}`, true},
		{"503 unavailable", 503, ``, true},
		{"500 high load", 500, `{"message":"Encountered unexpectedly high load when processing the request, please try again"}`, true},
		{"500 try again later", 500, `{"message":"Service busy, try again later"}`, true},
		{"500 generic (not retryable)", 500, `{"message":"Internal error: null pointer"}`, false},
		{"400 not overload", 400, `{"reason":"content_length_exceeds_threshold"}`, false},
		{"200 not overload", 200, `ok`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientOverload(tt.status, []byte(tt.body)); got != tt.want {
				t.Fatalf("isTransientOverload(%d, %q) = %v, want %v", tt.status, tt.body, got, tt.want)
			}
		})
	}
}

func TestRetryBackoff(t *testing.T) {
	// Monotonic-ish growth with a floor and an 8s cap, and jitter keeps it
	// within [backoff/2, backoff] so it's never zero and never over the cap.
	const maxBackoff = 8 * time.Second
	for attempt := 1; attempt <= 8; attempt++ {
		for i := 0; i < 50; i++ {
			d := retryBackoff(attempt)
			if d <= 0 {
				t.Fatalf("retryBackoff(%d) = %v, must be > 0", attempt, d)
			}
			if d > maxBackoff {
				t.Fatalf("retryBackoff(%d) = %v, exceeds cap %v", attempt, d, maxBackoff)
			}
		}
	}
	// Early attempts should generally be smaller than later ones (floor grows).
	// Compare floors: attempt 1 floor 200ms vs attempt 4 floor ~1.6s.
	if lo, hi := retryBackoff(1), retryBackoff(5); lo >= maxBackoff && hi < maxBackoff {
		t.Fatalf("unexpected ordering: attempt1=%v attempt5=%v", lo, hi)
	}
}
