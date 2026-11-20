package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// stalledWriter is a ResponseWriter whose client stopped reading without
// closing: every write blocks until its write deadline passes, or forever when
// none is set (until the test ends).
type stalledWriter struct {
	mu       sync.Mutex
	header   http.Header
	deadline time.Time
	writes   chan struct{} // one value per write that started
	release  chan struct{}
}

func newStalledWriter(t *testing.T) *stalledWriter {
	s := &stalledWriter{header: http.Header{}, writes: make(chan struct{}, 100), release: make(chan struct{})}
	t.Cleanup(func() { close(s.release) })
	return s
}

func (s *stalledWriter) Header() http.Header { return s.header }
func (s *stalledWriter) WriteHeader(int)     {}
func (s *stalledWriter) Flush()              {}

func (s *stalledWriter) SetWriteDeadline(d time.Time) error {
	s.mu.Lock()
	s.deadline = d
	s.mu.Unlock()
	return nil
}

func (s *stalledWriter) Write(p []byte) (int, error) {
	select {
	case s.writes <- struct{}{}:
	default:
	}
	s.mu.Lock()
	d := s.deadline
	s.mu.Unlock()
	if d.IsZero() {
		<-s.release
		return 0, io.ErrClosedPipe
	}
	select {
	case <-time.After(time.Until(d)):
		return 0, os.ErrDeadlineExceeded
	case <-s.release:
		return 0, io.ErrClosedPipe
	}
}

func withStreamWriteTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := streamWriteTimeout
	streamWriteTimeout = d
	t.Cleanup(func() { streamWriteTimeout = prev })
}

// A ping stuck on a client that stopped reading ends at the write deadline, so
// stop returns; the writer reports the client gone and writes nothing more.
func TestKeepaliveWriterStopReturnsWhenAPingWriteStalls(t *testing.T) {
	withStreamWriteTimeout(t, 50*time.Millisecond)
	sw := newStalledWriter(t)
	gone := make(chan struct{})
	var goneOnce sync.Once
	k := newKeepaliveWriter(sw, 10*time.Millisecond, func() { goneOnce.Do(func() { close(gone) }) })

	select {
	case <-sw.writes:
	case <-time.After(2 * time.Second):
		t.Fatal("the keepalive writer never pinged")
	}

	stopped := make(chan struct{})
	go func() { k.stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not return while a ping write was stalled")
	}
	select {
	case <-gone:
	default:
		t.Fatal("a write that hit its deadline must report the client gone")
	}
	if _, err := k.Write([]byte("x")); err == nil {
		t.Fatal("a write after the client is gone must fail")
	}
}

// A non-streaming answer written to a client that stopped reading ends at the
// same per-write deadline instead of blocking the handler forever, and the
// deadline is cleared again afterwards.
func TestNonStreamWriteEndsWhenTheClientStopsReading(t *testing.T) {
	withStreamWriteTimeout(t, 50*time.Millisecond)
	streamTestSetup(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write(cwFrame(`{"content":"ANSWER"}`))
	})

	sw := newStalledWriter(t)
	lg := logger.NewLogger(50)
	done := make(chan struct{})
	go func() {
		handleNonStreamRequestCtx(context.Background(), sw, testRequest(), lg, "sess", "req")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the non-stream handler blocked on a client that stopped reading")
	}
	sw.mu.Lock()
	deadline := sw.deadline
	sw.mu.Unlock()
	if !deadline.IsZero() {
		t.Fatalf("the write deadline is still armed (%v); a later request on the connection would hit it", deadline)
	}
	if !loggedLine(lg, logger.LogTypeErr, "Non-stream", "write") {
		t.Fatalf("no error line for the failed write; entries: %+v", lg.GetEntries())
	}
}

// A handler whose client stopped reading ends through its client-gone path
// and cancels the backend request, instead of blocking on a write forever.
func TestStreamEndsWhenTheClientStopsReading(t *testing.T) {
	withStreamWriteTimeout(t, 50*time.Millisecond)
	cancelled := make(chan struct{})
	streamTestSetup(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write(cwFrame(`{"content":"FIRST"}`))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(5 * time.Second):
		}
	})
	withKeepaliveInterval(t, 0)

	sw := newStalledWriter(t)
	done := make(chan struct{})
	go func() {
		handleStreamRequestWithLogger(sw, testRequest(), logger.NewLogger(50), "sess", "req", nil)
		close(done)
	}()
	for name, ch := range map[string]chan struct{}{"handler return": done, "backend cancellation": cancelled} {
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for %s", name)
		}
	}
}
