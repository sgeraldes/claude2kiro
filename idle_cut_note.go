package main

import (
	"sync"
	"time"
)

// When Kiro's idle cut breaks a tool call (see backend_idle_cut.go), the proxy
// closes the call with the input that arrived and remembers its id here. The
// client runs the call, which fails or runs on a cut input, and sends its tool
// result back. On its way to Kiro that tool result gets a note saying why, so
// the model splits the work instead of repeating the call. The note goes to
// Kiro only: the client never receives it and its transcript keeps the tool
// result it wrote. Every later request of the conversation carries the same
// tool result in its history, so the note is added there too, which keeps the
// history the backend sees the same from one request to the next.

// idleCutNoteError is the note for a tool result that reports an error: the
// call did not run.
const idleCutNoteError = "Note from the proxy: the backend closed this tool call because its input took more than 240 seconds to produce, " +
	"so the input arrived incomplete and nothing was executed. Do not repeat it in one call. " +
	"Split the work into smaller pieces: for a large file, write a first part and add the rest with further edits."

// idleCutNoteSuccess is the note for a tool result that reports success: the
// call ran, but on an input that was cut short.
const idleCutNoteSuccess = "Note from the proxy: the backend closed this tool call because its input took more than 240 seconds to produce, " +
	"so the call ran on an input that was cut short and is incomplete. Check what it did, and do not produce that much in one call again. " +
	"Split the work into smaller pieces: for a large file, write a first part and add the rest with further edits."

// idleCutNoteFor returns the note for a tool result with the given status.
func idleCutNoteFor(status string) string {
	if status == "error" {
		return idleCutNoteError
	}
	return idleCutNoteSuccess
}

// idleCutCalls remembers the tool calls an idle cut broke, per conversation.
var idleCutCalls = newCutCallStore(256, 6*time.Hour)

// cutCallStore is a bounded set of tool use ids with an expiry, keyed by the
// same scope the reasoning store uses (session and conversation).
type cutCallStore struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	entries map[string]time.Time // key -> when it was added
	now     func() time.Time
}

func newCutCallStore(max int, ttl time.Duration) *cutCallStore {
	return &cutCallStore{max: max, ttl: ttl, entries: map[string]time.Time{}, now: time.Now}
}

func cutCallKey(scope, toolUseID string) string { return scope + "\x00" + toolUseID }

// Add remembers ids in scope. Expired entries go first; past max, the oldest.
func (s *cutCallStore) Add(scope string, ids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, added := range s.entries {
		if now.Sub(added) > s.ttl {
			delete(s.entries, k)
		}
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		s.entries[cutCallKey(scope, id)] = now
	}
	for len(s.entries) > s.max {
		oldestKey, oldest := "", now
		for k, added := range s.entries {
			if oldestKey == "" || added.Before(oldest) {
				oldestKey, oldest = k, added
			}
		}
		delete(s.entries, oldestKey)
	}
}

// Has reports whether id in scope was broken by an idle cut and has not
// expired.
func (s *cutCallStore) Has(scope, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	added, ok := s.entries[cutCallKey(scope, id)]
	return ok && s.now().Sub(added) <= s.ttl
}

// Len is the number of entries, expired ones included.
func (s *cutCallStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// addIdleCutNotes appends the note to every tool result of the request, in
// the current message and in the history, whose call an idle cut broke in
// this conversation. It returns how many tool results got it.
func addIdleCutNotes(cwReq *CodeWhispererRequest) int {
	scope := cwReq.scope.storeScope()
	added := 0
	note := func(results []ToolResult) {
		for i := range results {
			if idleCutCalls.Has(scope, results[i].ToolUseId) {
				results[i].Content = append(results[i].Content, ToolResultContent{Text: idleCutNoteFor(results[i].Status)})
				added++
			}
		}
	}
	note(cwReq.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.ToolResults)
	for _, h := range cwReq.ConversationState.History {
		if u, ok := h.(HistoryUserMessage); ok && u.UserInputMessage.UserInputMessageContext != nil {
			note(u.UserInputMessage.UserInputMessageContext.ToolResults)
		}
	}
	return added
}
