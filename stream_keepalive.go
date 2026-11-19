package main

import (
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

// keepaliveWriter is the ResponseWriter of a streaming response. It serializes
// every write and, from its own goroutine, writes a ping whenever nothing has
// been written for the interval. Callers must write whole SSE frames in one
// Write, and must call stop before the handler returns.
type keepaliveWriter struct {
	mu        sync.Mutex
	w         http.ResponseWriter
	flusher   http.Flusher
	lastWrite time.Time

	done chan struct{}
	wg   sync.WaitGroup
}

func newKeepaliveWriter(w http.ResponseWriter, flusher http.Flusher, interval time.Duration) *keepaliveWriter {
	k := &keepaliveWriter{w: w, flusher: flusher, lastWrite: time.Now(), done: make(chan struct{})}
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
	k.lastWrite = time.Now()
	return k.w.Write(p)
}

func (k *keepaliveWriter) Flush() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.flusher.Flush()
}

// stop ends the ping goroutine and waits for it, so nothing is written to the
// response after the handler returns.
func (k *keepaliveWriter) stop() {
	close(k.done)
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
	if time.Since(k.lastWrite) < interval {
		return
	}
	k.lastWrite = time.Now()
	// A failed write means the client is gone; the handler learns that from
	// the request context, so there is nothing to do with the error here.
	if _, err := k.w.Write([]byte(sseKeepalivePing)); err == nil {
		k.flusher.Flush()
	}
}
