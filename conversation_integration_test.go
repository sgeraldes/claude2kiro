package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// TestConcurrentSubagents_DistinctConversationIds is the end-to-end regression
// for the parallel-subagent stall: it drives the real streaming handler
// concurrently with the SAME Claude Code session id (as parent + Task subagents
// do) and asserts each request reaches the backend on its own conversationId.
// A barrier in the fake backend holds every request open until all have arrived,
// guaranteeing their claims overlap.
func TestConcurrentSubagents_DistinctConversationIds(t *testing.T) {
	const n = 3 // <= default MaxConcurrentReqs (4) so all arrive concurrently

	var (
		mu      sync.Mutex
		gotIDs  []string
		arrived int32
		barrier = make(chan struct{})
	)

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed CodeWhispererRequest
		_ = json.Unmarshal(body, &parsed)
		mu.Lock()
		gotIDs = append(gotIDs, parsed.ConversationState.ConversationId)
		mu.Unlock()
		// Hold all requests open until every one has arrived, so their
		// conversationId claims are simultaneously in flight.
		if atomic.AddInt32(&arrived, 1) == int32(n) {
			close(barrier)
		}
		select {
		case <-barrier:
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusOK) // empty body -> parser yields no events
	}))
	defer fake.Close()

	cfg := *config.Get()
	cfg.Advanced.StableConversationID = true
	cfg.Advanced.CodeWhispererEndpoint = fake.URL
	cfg.Network.MaxConcurrentReqs = n
	withConfig(t, &cfg)

	lg := logger.NewLogger(100)

	// Same session id for all n requests, exactly like parent + subagents.
	metadata := map[string]any{"user_id": `{"session_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}`}
	req := AnthropicRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 64,
		Stream:    true,
		Metadata:  metadata,
		Messages:  []AnthropicRequestMessage{{Role: "user", Content: "hi"}},
	}

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			handleStreamRequestWithLogger(rec, req, TokenData{}, lg, "sess", "req", nil)
		}(i)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(gotIDs) != n {
		t.Fatalf("backend saw %d requests, want %d", len(gotIDs), n)
	}
	seen := map[string]int{}
	for _, id := range gotIDs {
		if id == "" {
			t.Fatal("a request reached the backend with an empty conversationId")
		}
		seen[id]++
	}
	if len(seen) != n {
		t.Fatalf("concurrent requests collided on conversationId(s): %v", gotIDs)
	}
}
