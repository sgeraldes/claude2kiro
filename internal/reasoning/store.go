// Package reasoning keeps the model's reasoning between requests so the proxy
// can give it back to the backend on the next turn. The client never sees the
// reasoning and never sends it back, so the proxy is the only place that has
// it. The store is in memory only: after a restart the model simply thinks
// again.
package reasoning

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

// DefaultMaxBytes bounds the reasoning kept for all sessions together.
const DefaultMaxBytes = 128 << 20

// Entry is one assistant turn's reasoning and the Kiro model id of the request
// that produced it. Either Text and Signature are set, or Redacted is.
type Entry struct {
	Model     string
	Text      string
	Signature string
	Redacted  json.RawMessage
}

// Size is the number of reasoning bytes the entry holds; it is what the store
// bound and the per-request cap count.
func (e Entry) Size() int { return len(e.Text) + len(e.Signature) + len(e.Redacted) }

// ToolKey is the key of the turn that emitted the tool call toolUseID in the
// client session session. A request without a session gets the unscoped key
// (session ""): tool use ids are unique on their own.
func ToolKey(session, toolUseID string) string {
	if session == "" {
		return "tool:" + toolUseID
	}
	return "tool:" + session + "\x00" + toolUseID
}

// TextKey is the key of a turn without tool calls: a hash of the client
// session, the turn's position among the conversation's assistant turns, and
// its text after NormalizeText. Without the session and the position, two
// conversations (or two turns of one) that said the same thing would share
// reasoning.
func TextKey(session string, turn int, text string) string {
	sum := sha256.Sum256([]byte(session + "\x00" + strconv.Itoa(turn) + "\x00" + NormalizeText(text)))
	return "text:" + hex.EncodeToString(sum[:])
}

// NormalizeText is the form of a turn's text that TextKey hashes: every run of
// whitespace becomes one space and the ends are trimmed. The response side
// concatenates the text frames and the client may send the turn back split
// into several text blocks; callers pass the concatenation of those blocks,
// and this makes both hash the same thing.
func NormalizeText(text string) string { return strings.Join(strings.Fields(text), " ") }

type item struct {
	keys  []string
	entry Entry
}

// Store is a byte-bounded store of entries, least recently used evicted first.
// It is safe for concurrent use.
type Store struct {
	mu       sync.Mutex
	maxBytes int
	used     int
	order    *list.List               // front is the most recently used
	byKey    map[string]*list.Element // every key of every item
}

// NewStore returns an empty store that holds at most maxBytes (see Entry.Size).
func NewStore(maxBytes int) *Store {
	return &Store{maxBytes: maxBytes, order: list.New(), byKey: map[string]*list.Element{}}
}

// Put stores e under every key, replacing any entry stored under one of them.
// An entry larger than the whole bound is not stored, but still replaces: the
// entries under its keys are removed.
func (s *Store) Put(e Entry, keys ...string) {
	if len(keys) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if el, ok := s.byKey[k]; ok {
			s.remove(el)
		}
	}
	if e.Size() > s.maxBytes {
		return
	}
	el := s.order.PushFront(&item{keys: append([]string(nil), keys...), entry: e})
	for _, k := range keys {
		s.byKey[k] = el
	}
	s.used += e.Size()
	for s.used > s.maxBytes {
		s.remove(s.order.Back())
	}
}

// Get returns the entry stored under the first of keys that has one, when it
// was produced by model. An entry of another model is not returned.
func (s *Store) Get(model string, keys ...string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		el, ok := s.byKey[k]
		if !ok {
			continue
		}
		s.order.MoveToFront(el)
		e := el.Value.(*item).entry
		if e.Model != model {
			return Entry{}, false
		}
		return e, true
	}
	return Entry{}, false
}

// Evict removes the entries stored under any of keys, under all their keys.
func (s *Store) Evict(keys ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if el, ok := s.byKey[k]; ok {
			s.remove(el)
		}
	}
}

// Bytes is the number of reasoning bytes stored.
func (s *Store) Bytes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used
}

// Len is the number of entries stored.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.order.Len()
}

// remove drops one item and all its keys. The caller holds s.mu.
func (s *Store) remove(el *list.Element) {
	it := el.Value.(*item)
	for _, k := range it.keys {
		if s.byKey[k] == el {
			delete(s.byKey, k)
		}
	}
	s.order.Remove(el)
	s.used -= it.entry.Size()
}
