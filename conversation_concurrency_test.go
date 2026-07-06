package main

import (
	"sync"
	"testing"
)

func TestConversationTracker(t *testing.T) {
	tr := &conversationTracker{active: make(map[string]struct{})}
	if !tr.tryClaim("a") {
		t.Fatal("first claim of 'a' should succeed")
	}
	if tr.tryClaim("a") {
		t.Fatal("second concurrent claim of 'a' should fail")
	}
	if !tr.tryClaim("b") {
		t.Fatal("claim of a different id 'b' should succeed")
	}
	tr.release("a")
	if !tr.tryClaim("a") {
		t.Fatal("claim of 'a' after release should succeed")
	}
	tr.release("a")
	tr.release("b")
}

// The core regression: two concurrent requests from the same Claude Code session
// (parent + Task subagent) must NOT hit the backend on the same conversationId.
func TestClaimStableConversation_ConcurrentGetsFreshId(t *testing.T) {
	withStableConversationID(t, true)
	sessionKey := "session-concurrency-test-1"
	stable := stableConversationID(sessionKey)

	req1 := &CodeWhispererRequest{}
	req1.ConversationState.ConversationId = stable
	rel1 := claimStableConversation(req1, sessionKey)
	if req1.ConversationState.ConversationId != stable {
		t.Fatalf("first request should keep the stable convId, got %q", req1.ConversationState.ConversationId)
	}

	// Concurrent second request on the same session must get its own conversation.
	req2 := &CodeWhispererRequest{}
	req2.ConversationState.ConversationId = stable
	rel2 := claimStableConversation(req2, sessionKey)
	if req2.ConversationState.ConversationId == stable {
		t.Fatal("concurrent request must get a fresh conversationId, not the stable one")
	}
	if req2.ConversationState.ConversationId == req1.ConversationState.ConversationId {
		t.Fatal("concurrent requests must not share a conversationId")
	}
	rel2()

	// Once the owner releases, a later (sequential) turn reclaims the stable id so
	// prefix-cache reuse is preserved for the common single-agent case.
	rel1()
	req3 := &CodeWhispererRequest{}
	req3.ConversationState.ConversationId = stable
	rel3 := claimStableConversation(req3, sessionKey)
	defer rel3()
	if req3.ConversationState.ConversationId != stable {
		t.Fatalf("sequential request after release should reclaim the stable convId, got %q", req3.ConversationState.ConversationId)
	}
}

// With the feature disabled, buildCodeWhispererRequest already used a fresh UUID
// per request, so claim must be an inert no-op and never rewrite the id.
func TestClaimStableConversation_Disabled(t *testing.T) {
	withStableConversationID(t, false)
	sessionKey := "session-concurrency-test-2"

	req := &CodeWhispererRequest{}
	req.ConversationState.ConversationId = "fresh-uuid-a"
	rel := claimStableConversation(req, sessionKey)
	defer rel()
	if req.ConversationState.ConversationId != "fresh-uuid-a" {
		t.Fatalf("with stable off, claim must not modify the convId, got %q", req.ConversationState.ConversationId)
	}

	req2 := &CodeWhispererRequest{}
	req2.ConversationState.ConversationId = "fresh-uuid-b"
	rel2 := claimStableConversation(req2, sessionKey)
	defer rel2()
	if req2.ConversationState.ConversationId != "fresh-uuid-b" {
		t.Fatalf("with stable off, concurrent claim must not override, got %q", req2.ConversationState.ConversationId)
	}
}

// Under a burst of concurrent claims (run with -race), exactly one request may
// keep the stable id and every resulting conversationId must be unique.
func TestClaimStableConversation_RaceOneWinner(t *testing.T) {
	withStableConversationID(t, true)
	sessionKey := "session-race-test"
	stable := stableConversationID(sessionKey)

	const n = 20
	var wg sync.WaitGroup
	ids := make([]string, n)
	releases := make([]func(), n)
	start := make(chan struct{})

	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := &CodeWhispererRequest{}
			req.ConversationState.ConversationId = stable
			<-start
			releases[i] = claimStableConversation(req, sessionKey)
			ids[i] = req.ConversationState.ConversationId
		}(i)
	}
	close(start)
	wg.Wait()

	stableCount := 0
	seen := map[string]int{}
	for _, id := range ids {
		seen[id]++
		if id == stable {
			stableCount++
		}
	}
	if stableCount != 1 {
		t.Fatalf("expected exactly 1 request to keep the stable convId, got %d", stableCount)
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("conversationId %q used %d times; all must be unique across concurrent requests", id, c)
		}
	}
	for _, r := range releases {
		if r != nil {
			r()
		}
	}
}
