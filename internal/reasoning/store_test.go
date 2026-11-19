package reasoning

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func entry(model, text string) Entry {
	return Entry{Model: model, Text: text, Signature: "sig-" + text}
}

func TestPutAndGetByToolIDAndByTextHash(t *testing.T) {
	s := NewStore(1 << 20)
	s.Put(entry("auto", "plan-a"), ToolKey("tool-1"), ToolKey("tool-2"))
	s.Put(Entry{Model: "auto", Redacted: json.RawMessage(`"blob"`)}, TextKey("The answer is 42."))

	for _, key := range []string{ToolKey("tool-1"), ToolKey("tool-2")} {
		got, ok := s.Get("auto", key)
		if !ok || got.Text != "plan-a" || got.Signature != "sig-plan-a" {
			t.Fatalf("Get(%s) = %+v, %v; want plan-a", key, got, ok)
		}
	}
	got, ok := s.Get("auto", TextKey("The answer is 42."))
	if !ok || string(got.Redacted) != `"blob"` {
		t.Fatalf("Get by text = %+v, %v; want the redacted entry", got, ok)
	}
	if _, ok := s.Get("auto", ToolKey("unknown"), TextKey("other text")); ok {
		t.Fatal("unknown keys must find nothing")
	}
	// The first key that has an entry wins.
	got, ok = s.Get("auto", ToolKey("unknown"), ToolKey("tool-2"), TextKey("The answer is 42."))
	if !ok || got.Text != "plan-a" {
		t.Fatalf("Get with several keys = %+v, %v; want the first key with an entry", got, ok)
	}
	if ToolKey("x") == TextKey("x") {
		t.Fatal("a tool id and a text must never share a key")
	}
	if TextKey("a") == TextKey("b") || TextKey("same") != TextKey("same") {
		t.Fatal("the text key must be a stable hash of the text")
	}
}

func TestModelMismatchReturnsNothing(t *testing.T) {
	s := NewStore(1 << 20)
	s.Put(entry("auto", "plan"), ToolKey("t1"))
	if _, ok := s.Get("claude-opus-4.8", ToolKey("t1")); ok {
		t.Fatal("an entry produced by another model must not be returned")
	}
	if _, ok := s.Get("auto", ToolKey("t1")); !ok {
		t.Fatal("the producing model must still find it")
	}
}

func TestByteBoundEvictsLeastRecentlyUsed(t *testing.T) {
	one := entry("m", strings.Repeat("a", 100))
	size := one.Size()
	s := NewStore(3 * size)
	s.Put(entry("m", strings.Repeat("a", 100)), ToolKey("a"))
	s.Put(entry("m", strings.Repeat("b", 100)), ToolKey("b"))
	s.Put(entry("m", strings.Repeat("c", 100)), ToolKey("c"))
	if _, ok := s.Get("m", ToolKey("a")); !ok { // a is now the most recent
		t.Fatal("a must still be stored")
	}
	s.Put(entry("m", strings.Repeat("d", 100)), ToolKey("d"))

	if _, ok := s.Get("m", ToolKey("b")); ok {
		t.Fatal("b was least recently used and must be evicted")
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := s.Get("m", ToolKey(k)); !ok {
			t.Fatalf("%s must survive", k)
		}
	}
	if s.Bytes() > 3*size {
		t.Fatalf("stored bytes %d exceed the bound %d", s.Bytes(), 3*size)
	}

	// An entry larger than the whole bound is not stored and evicts nothing.
	s.Put(entry("m", strings.Repeat("z", 10*size)), ToolKey("z"))
	if _, ok := s.Get("m", ToolKey("z")); ok {
		t.Fatal("an entry larger than the bound must not be stored")
	}
	if s.Len() != 3 {
		t.Fatalf("len = %d, want 3", s.Len())
	}
}

func TestPutReplacesAnEntryUnderTheSameKey(t *testing.T) {
	s := NewStore(1 << 20)
	s.Put(entry("m", "old"), ToolKey("t1"), ToolKey("t2"))
	s.Put(entry("m", "new"), ToolKey("t2"))
	if got, ok := s.Get("m", ToolKey("t2")); !ok || got.Text != "new" {
		t.Fatalf("t2 = %+v, %v; want new", got, ok)
	}
	if _, ok := s.Get("m", ToolKey("t1")); ok {
		t.Fatal("the replaced entry must be gone under all its keys")
	}
	if s.Len() != 1 || s.Bytes() != entry("m", "new").Size() {
		t.Fatalf("len=%d bytes=%d, want one entry of %d bytes", s.Len(), s.Bytes(), entry("m", "new").Size())
	}
}

func TestEvictRemovesTheEntryUnderAllItsKeys(t *testing.T) {
	s := NewStore(1 << 20)
	s.Put(entry("m", "plan"), ToolKey("t1"), ToolKey("t2"))
	s.Put(entry("m", "other"), ToolKey("t3"))
	s.Evict(ToolKey("t1"), ToolKey("missing"))
	if _, ok := s.Get("m", ToolKey("t2")); ok {
		t.Fatal("evicting one key must remove the entry under its other keys")
	}
	if _, ok := s.Get("m", ToolKey("t3")); !ok {
		t.Fatal("other entries must survive")
	}
	if s.Bytes() != entry("m", "other").Size() {
		t.Fatalf("bytes = %d after evict", s.Bytes())
	}
}

func TestStoreIsSafeForConcurrentUse(t *testing.T) {
	s := NewStore(50 * entry("m", "plan-000").Size())
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				key := ToolKey(fmt.Sprintf("t%d-%d", g, i%80))
				s.Put(entry("m", fmt.Sprintf("plan-%03d", i%1000)), key)
				s.Get("m", key)
				if i%7 == 0 {
					s.Evict(key)
				}
				_ = s.Bytes()
			}
		}(g)
	}
	wg.Wait()
	if s.Bytes() > 50*entry("m", "plan-000").Size() {
		t.Fatalf("bytes %d exceed the bound", s.Bytes())
	}
}
