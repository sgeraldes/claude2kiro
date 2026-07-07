package main

import (
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
