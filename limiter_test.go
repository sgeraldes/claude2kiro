package main

import (
	"context"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
)

// TestKiroLimiterEnforcesLimit verifies the live limiter blocks at the
// configured concurrency and that a release admits a waiter — the mechanism
// that lets max_concurrent_requests throttle the backend without a restart.
func TestKiroLimiterEnforcesLimit(t *testing.T) {
	prev := config.Get()
	cfg := *prev
	cfg.Network.MaxConcurrentReqs = 2
	config.Set(&cfg)
	t.Cleanup(func() { config.Set(prev) })

	l := newKiroLimiter()
	l.acquire() // 1
	l.acquire() // 2 — at the limit

	// A third acquire must block.
	admitted := make(chan struct{})
	go func() { l.acquire(); close(admitted) }()

	select {
	case <-admitted:
		t.Fatal("third acquire should block at limit=2")
	case <-time.After(75 * time.Millisecond):
		// expected: still blocked
	}

	// Releasing one slot must admit the waiter.
	l.release()
	select {
	case <-admitted:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("release did not admit the blocked acquire")
	}

	l.release()
	l.release()
}

// A request whose client leaves while it waits for a slot stops waiting at
// once and never takes a slot; the next request still gets the slot.
func TestKiroLimiterCancelWhileQueued(t *testing.T) {
	prev := config.Get()
	cfg := *prev
	cfg.Network.MaxConcurrentReqs = 1
	config.Set(&cfg)
	t.Cleanup(func() { config.Set(prev) })

	l := newKiroLimiter()
	if !l.acquireCtx(context.Background()) {
		t.Fatal("the first acquire must get the free slot")
	}

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan bool)
	go func() { got <- l.acquireCtx(ctx) }()
	select {
	case <-got:
		t.Fatal("the second acquire should wait at limit=1")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case ok := <-got:
		if ok {
			t.Fatal("a cancelled acquire must not take a slot")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled acquire kept waiting")
	}

	l.mu.Lock()
	active := l.active
	l.mu.Unlock()
	if active != 1 {
		t.Fatalf("active = %d after the cancelled wait, want 1", active)
	}

	// The slot is still the first request's; once it is released a later
	// request gets it.
	later := make(chan bool)
	go func() { later <- l.acquireCtx(context.Background()) }()
	l.release()
	select {
	case ok := <-later:
		if !ok {
			t.Fatal("a later acquire must get the released slot")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a later acquire was not admitted after the release")
	}
	l.release()
}

// An acquire whose context is already done never takes a slot, even a free one.
func TestKiroLimiterDoneContextTakesNoSlot(t *testing.T) {
	l := newKiroLimiter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if l.acquireCtx(ctx) {
		t.Fatal("an acquire with a done context must fail")
	}
	if l.active != 0 {
		t.Fatalf("active = %d, want 0", l.active)
	}
}

// TestKiroLimiterLowerLimitLive verifies that lowering the limit while requests
// are in flight makes new acquires wait until the in-flight count drains below
// the new (smaller) limit — the throttle-under-load path.
func TestKiroLimiterLowerLimitLive(t *testing.T) {
	prev := config.Get()
	cfg := *prev
	cfg.Network.MaxConcurrentReqs = 3
	config.Set(&cfg)
	t.Cleanup(func() { config.Set(prev) })

	l := newKiroLimiter()
	l.acquire()
	l.acquire()
	l.acquire() // 3 in flight at limit 3

	// Lower the limit live to 1.
	lowered := *prev
	lowered.Network.MaxConcurrentReqs = 1
	config.Set(&lowered)

	// A new acquire must block: 3 active >= new limit 1.
	admitted := make(chan struct{})
	go func() { l.acquire(); close(admitted) }()
	select {
	case <-admitted:
		t.Fatal("acquire should block while active(3) exceeds new limit(1)")
	case <-time.After(75 * time.Millisecond):
	}

	// Drain below the new limit: release until active < 1 (i.e. to 0).
	l.release() // 2
	l.release() // 1
	select {
	case <-admitted:
		t.Fatal("still over new limit at active=1; should not admit yet")
	case <-time.After(50 * time.Millisecond):
	}
	l.release() // 0 -> waiter admitted
	select {
	case <-admitted:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not admitted after draining below the lowered limit")
	}
	l.release()
}
