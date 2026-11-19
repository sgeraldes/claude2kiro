package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// liveRecorder is a ResponseWriter whose body can be read while the handler is
// still writing, which httptest.ResponseRecorder does not allow.
type liveRecorder struct {
	mu     sync.Mutex
	header http.Header
	body   strings.Builder
}

func newLiveRecorder() *liveRecorder { return &liveRecorder{header: http.Header{}} }

func (l *liveRecorder) Header() http.Header { return l.header }
func (l *liveRecorder) WriteHeader(int)     {}
func (l *liveRecorder) Flush()              {}
func (l *liveRecorder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.body.Write(p)
}
func (l *liveRecorder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.body.String()
}

func waitForBody(t *testing.T, rec *liveRecorder, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rec.String(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the client never received %q; body so far:\n%s", want, rec.String())
}

// withKeepaliveInterval sets network.stream_keepalive_interval on top of the
// current config, so call it after streamTestSetup.
func withKeepaliveInterval(t *testing.T, d time.Duration) {
	t.Helper()
	cfg := *config.Get()
	cfg.Network.StreamKeepaliveInterval = d
	withConfig(t, &cfg)
}

func streamTestSetup(t *testing.T, backend http.HandlerFunc) {
	t.Helper()
	fake := httptest.NewServer(backend)
	t.Cleanup(fake.Close)
	withIdentities(t, map[string]TokenData{"": primaryToken()})
	failoverConfig(t, fake.URL)
}

// The client must see the first text while the backend is still generating.
func TestStreamForwardsEventsBeforeTheBackendFinishes(t *testing.T) {
	release := make(chan struct{})
	streamTestSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(cwFrame(`{"content":"FIRST"}`))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		w.Write(cwFrame(`{"content":"SECOND"}`))
	})

	rec := newLiveRecorder()
	done := make(chan string)
	go func() {
		done <- handleStreamRequestWithLogger(rec, testRequest(), logger.NewLogger(50), "sess", "req", nil)
	}()

	waitForBody(t, rec, "FIRST")
	if strings.Contains(rec.String(), "SECOND") || strings.Contains(rec.String(), "message_stop") {
		t.Fatalf("the stream ended before the backend was released:\n%s", rec.String())
	}
	close(release)

	select {
	case preview := <-done:
		if preview != "FIRSTSECOND" {
			t.Fatalf("response preview = %q, want FIRSTSECOND", preview)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not finish after the backend was released")
	}

	body := rec.String()
	for _, want := range []string{"event: message_start", "SECOND", `"stop_reason":"end_turn"`, "event: message_stop"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Index(body, "event: message_start") > strings.Index(body, "FIRST") {
		t.Fatalf("message_start came after the first delta:\n%s", body)
	}
}

// While the backend says nothing, the client keeps receiving pings.
func TestStreamSendsPingsWhileTheBackendIsSilent(t *testing.T) {
	streamTestSetup(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write(cwFrame(`{"content":"LATE"}`))
	})
	withKeepaliveInterval(t, 20*time.Millisecond)

	rec := newLiveRecorder()
	handleStreamRequestWithLogger(rec, testRequest(), logger.NewLogger(50), "sess", "req", nil)

	body := rec.String()
	firstPing := strings.Index(body, "event: ping\n")
	content := strings.Index(body, "LATE")
	if firstPing < 0 || content < 0 || firstPing > content {
		t.Fatalf("want a ping before the late content; body:\n%s", body)
	}
	// Every frame is whole: an event line is always followed by its data line.
	for _, frame := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		lines := strings.Split(frame, "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "event: ") || !strings.HasPrefix(lines[1], "data: ") {
			t.Fatalf("malformed SSE frame %q in body:\n%s", frame, body)
		}
	}
}

// stream_keepalive_interval 0 turns the silence pings off: the only ping is
// the one that follows message_start.
func TestStreamKeepaliveIntervalZeroSendsNoSilencePings(t *testing.T) {
	streamTestSetup(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write(cwFrame(`{"content":"LATE"}`))
	})
	withKeepaliveInterval(t, 0)

	rec := newLiveRecorder()
	handleStreamRequestWithLogger(rec, testRequest(), logger.NewLogger(50), "sess", "req", nil)

	body := rec.String()
	if !strings.HasPrefix(body, "event: message_start\n") {
		t.Fatalf("with pings off the body must open with message_start:\n%s", body)
	}
	if n := strings.Count(body, "event: ping\n"); n != 1 {
		t.Fatalf("pings = %d, want only the one after message_start:\n%s", n, body)
	}
}

// A client that goes away stops the backend request instead of leaving it
// running, paying credits and holding a concurrency slot.
func TestStreamCancelsTheBackendWhenTheClientDisconnects(t *testing.T) {
	arrived := make(chan struct{})
	cancelled := make(chan struct{})
	streamTestSetup(t, func(w http.ResponseWriter, r *http.Request) {
		// net/http only watches for a closed connection once the body is read.
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(arrived)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(5 * time.Second):
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	rec := newLiveRecorder()
	done := make(chan struct{})
	go func() {
		handleStreamRequestCtx(ctx, rec, testRequest(), logger.NewLogger(50), "sess", "req", nil)
		close(done)
	}()

	<-arrived
	cancel()

	for name, ch := range map[string]chan struct{}{"handler return": done, "backend cancellation": cancelled} {
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for %s", name)
		}
	}
	if strings.Contains(rec.String(), `"type":"error"`) {
		t.Fatalf("a disconnected client must not be answered an error:\n%s", rec.String())
	}
}

// A client that leaves while its request waits for a concurrency slot ends the
// wait: the handler returns without the slot and the backend never hears of it.
func TestStreamQueuedRequestStopsWaitingWhenTheClientLeaves(t *testing.T) {
	var contacted sync.Once
	backendHit := make(chan struct{})
	streamTestSetup(t, func(w http.ResponseWriter, r *http.Request) {
		contacted.Do(func() { close(backendHit) })
		w.WriteHeader(http.StatusOK)
	})
	cfg := *config.Get()
	cfg.Network.MaxConcurrentReqs = 1
	withConfig(t, &cfg)

	// Another request holds the only slot.
	kiroSema.acquire()
	held := true
	t.Cleanup(func() {
		if held {
			kiroSema.release()
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		handleStreamRequestCtx(ctx, newLiveRecorder(), testRequest(), logger.NewLogger(50), "sess", "req", nil)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond) // let it queue
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler kept waiting for a slot after the client left")
	}
	select {
	case <-backendHit:
		t.Fatal("a request abandoned in the queue reached the backend")
	default:
	}
	kiroSema.mu.Lock()
	active := kiroSema.active
	kiroSema.mu.Unlock()
	if active != 1 {
		t.Fatalf("active = %d, want 1 (only the holder)", active)
	}
	kiroSema.release()
	held = false
}

// assertStreamFailed checks that a response the backend did not finish reaches
// the client as a retryable error and never as a finished answer.
func assertStreamFailed(t *testing.T, body string) {
	t.Helper()
	if !strings.Contains(body, "event: error\n") || !strings.Contains(body, `"overloaded_error"`) {
		t.Fatalf("want an overloaded_error event; body:\n%s", body)
	}
	for _, unwanted := range []string{"event: message_stop", "event: message_delta", "event: content_block_stop"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("a failed stream must not send %q; body:\n%s", unwanted, body)
		}
	}
}

// A backend stream that ends inside a frame is a failed response, not a
// finished one: the client must not run a tool with half its input.
func TestStreamEndingInsideAFrameIsAnError(t *testing.T) {
	streamTestSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(cwFrame(`{"content":"FIRST"}`))
		second := cwFrame(`{"content":"SECOND HALF NEVER ARRIVES"}`)
		w.Write(second[:len(second)/2])
	})

	rec := newLiveRecorder()
	handleStreamRequestWithLogger(rec, testRequest(), logger.NewLogger(50), "sess", "req", nil)

	body := rec.String()
	if !strings.Contains(body, "FIRST") {
		t.Fatalf("the whole first frame should have been forwarded; body:\n%s", body)
	}
	assertStreamFailed(t, body)
}

// A frame whose length fields cannot be right is a failed response too.
func TestStreamWithAnImpossibleFrameIsAnError(t *testing.T) {
	streamTestSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(cwFrame(`{"content":"FIRST"}`))
		// total length 4 is shorter than the 12-byte minimum frame.
		w.Write([]byte{0, 0, 0, 4, 0, 0, 0, 0, 1, 2, 3, 4})
	})

	rec := newLiveRecorder()
	handleStreamRequestWithLogger(rec, testRequest(), logger.NewLogger(50), "sess", "req", nil)

	assertStreamFailed(t, rec.String())
}

func TestNonStreamCancelsTheBackendWhenTheClientDisconnects(t *testing.T) {
	arrived := make(chan struct{})
	cancelled := make(chan struct{})
	streamTestSetup(t, func(w http.ResponseWriter, r *http.Request) {
		// net/http only watches for a closed connection once the body is read.
		io.Copy(io.Discard, r.Body)
		close(arrived)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(5 * time.Second):
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		handleNonStreamRequestCtx(ctx, httptest.NewRecorder(), testRequest(), logger.NewLogger(50), "sess", "req")
		close(done)
	}()

	<-arrived
	cancel()

	for name, ch := range map[string]chan struct{}{"handler return": done, "backend cancellation": cancelled} {
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for %s", name)
		}
	}
}
