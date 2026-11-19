package main

import (
	"errors"
	"net/http"
	"sync"
	"time"
)

// How long a streaming response may stay silent before the proxy pings the
// client is network.stream_keepalive_interval (default 15s, 0 = no pings). The
// backend can spend minutes before its first byte; a client that hears nothing
// for that long gives up and resends the whole request while the first one is
// still running.

const sseKeepalivePing = "event: ping\ndata: {\"type\":\"ping\"}\n\n"

// defaultStreamWriteTimeout bounds each write (and flush) of a streaming
// response. The servers have no global write timeout, which would cut long
// streams; without a per-write bound a client that stops reading without
// closing would block a write, and with it the handler, forever.
const defaultStreamWriteTimeout = 60 * time.Second

// streamWriteTimeout is defaultStreamWriteTimeout; tests shorten it.
var streamWriteTimeout = defaultStreamWriteTimeout

// errStreamClientGone is returned by writes after one write failed.
var errStreamClientGone = errors.New("stream client gone: an earlier write failed")

// keepaliveWriter is the ResponseWriter of a streaming response. It serializes
// every write and, from its own goroutine, writes a ping whenever nothing has
// been written for the interval. Every write and flush runs under a write
// deadline of streamWriteTimeout. The first write or flush that fails (the
// deadline passed, or the connection broke) marks the client gone: onGone is
// called once and nothing more is written. Callers must write whole SSE frames
// in one Write, and must call stop before the handler returns.
type keepaliveWriter struct {
	mu           sync.Mutex
	w            http.ResponseWriter
	rc           *http.ResponseController
	writeTimeout time.Duration
	onGone       func()
	gone         bool
	lastWrite    time.Time

	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func newKeepaliveWriter(w http.ResponseWriter, interval time.Duration, onGone func()) *keepaliveWriter {
	k := &keepaliveWriter{
		w:            w,
		rc:           http.NewResponseController(w),
		writeTimeout: streamWriteTimeout,
		onGone:       onGone,
		lastWrite:    time.Now(),
		done:         make(chan struct{}),
	}
	if interval > 0 {
		k.wg.Add(1)
		go k.pingLoop(interval)
	}
	return k
}

func (k *keepaliveWriter) Header() http.Header { return k.w.Header() }

func (k *keepaliveWriter) WriteHeader(status int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.w.WriteHeader(status)
}

func (k *keepaliveWriter) Write(p []byte) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.gone {
		return 0, errStreamClientGone
	}
	k.lastWrite = time.Now()
	return k.writeLocked(p)
}

func (k *keepaliveWriter) Flush() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.flushLocked()
}

// writeLocked writes p under the write deadline. Callers hold mu.
func (k *keepaliveWriter) writeLocked(p []byte) (int, error) {
	if !k.setDeadline(time.Now().Add(k.writeTimeout)) {
		return 0, errStreamClientGone
	}
	n, err := k.w.Write(p)
	if err != nil {
		k.markGone()
		return n, err
	}
	k.setDeadline(time.Time{})
	return n, nil
}

// flushLocked flushes under the write deadline. Callers hold mu.
func (k *keepaliveWriter) flushLocked() {
	if k.gone || !k.setDeadline(time.Now().Add(k.writeTimeout)) {
		return
	}
	if err := k.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		k.markGone()
		return
	}
	k.setDeadline(time.Time{})
}

// setDeadline sets (or, with the zero time, clears) the write deadline and
// reports whether writing may go on. A writer without deadlines (a test
// recorder) answers http.ErrNotSupported, which is ignored; any other error
// means the connection is unusable, so the client is gone. Callers hold mu.
func (k *keepaliveWriter) setDeadline(t time.Time) bool {
	if err := k.rc.SetWriteDeadline(t); err != nil && !errors.Is(err, http.ErrNotSupported) {
		k.markGone()
		return false
	}
	return true
}

// markGone records that the client is unreachable and tells the handler once.
// Callers hold mu.
func (k *keepaliveWriter) markGone() {
	if k.gone {
		return
	}
	k.gone = true
	if k.onGone != nil {
		k.onGone()
	}
}

// stop ends the ping goroutine and waits for it, so nothing is written to the
// response after the handler returns. Calling it again does nothing.
func (k *keepaliveWriter) stop() {
	k.stopOnce.Do(func() { close(k.done) })
	k.wg.Wait()
}

func (k *keepaliveWriter) pingLoop(interval time.Duration) {
	defer k.wg.Done()
	// Checking twice per interval keeps the longest silence under 1.5 intervals.
	ticker := time.NewTicker(interval / 2)
	defer ticker.Stop()
	for {
		select {
		case <-k.done:
			return
		case <-ticker.C:
			k.pingIfSilent(interval)
		}
	}
}

func (k *keepaliveWriter) pingIfSilent(interval time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.gone || time.Since(k.lastWrite) < interval {
		return
	}
	k.lastWrite = time.Now()
	// A failed write marks the client gone (writeLocked); the handler learns
	// that through onGone, so there is nothing to do with the error here.
	if _, err := k.writeLocked([]byte(sseKeepalivePing)); err == nil {
		k.flushLocked()
	}
}
