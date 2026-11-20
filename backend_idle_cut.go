package main

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/sgeraldes/claude2kiro/parser"
)

// Kiro resets a response stream (HTTP/2 RST_STREAM, INTERNAL_ERROR) once it
// has sent nothing on it for 240 seconds. It also sends a tool call's input
// value only once the model has finished writing it: the name and the first
// key (a Write's file_path) arrive at once, then nothing, then the whole
// content in one burst. So a tool input that takes the model longer than 240 s
// to write (a large file in one Write call) never arrives. Measured on
// 2026-11-20, replaying one request straight against the backend: one send
// burst its content after 97 s of silence and finished; the same body sent ten
// minutes earlier stayed silent and was cut 240.0 s after its last frame. Cut
// requests were not charged.
//
// What the proxy does about it is in recovery.go: the cut, a broken stream or
// a MAX_TOKENS stop inside a tool call is recovered the way Kiro's own client
// does it. network.backend_idle_cut_after only decides whether the log and the
// error call a broken stream Kiro's idle cut.

// lastReadReader records when its reader last returned data.
type lastReadReader struct {
	r    io.Reader
	last time.Time
}

func newLastReadReader(r io.Reader, start time.Time) *lastReadReader {
	return &lastReadReader{r: r, last: start}
}

func (l *lastReadReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	if n > 0 {
		l.last = time.Now()
	}
	return n, err
}

// silence is how long the reader has gone without data.
func (l *lastReadReader) silence() time.Duration { return time.Since(l.last) }

// isBackendIdleCut reports a response that broke after the backend had been
// silent for at least threshold (network.backend_idle_cut_after). A clean end
// and a malformed frame are not cuts.
func isBackendIdleCut(err error, silence, threshold time.Duration) bool {
	if err == nil || err == io.EOF || errors.Is(err, parser.ErrInvalidFrame) {
		return false
	}
	return silence >= threshold
}

// backendIdleCutMessage is what the client is told about Kiro's idle cut
// when the proxy does not recover it.
func backendIdleCutMessage(silence time.Duration) string {
	secs := int(silence.Round(time.Second) / time.Second)
	return fmt.Sprintf("Kiro closed the response after it had sent nothing for %d s (Kiro ends a response that stays silent for about 240 s). "+
		"A tool call whose input takes the model longer than that to write, such as a large file in one Write call, never arrives. "+
		"Ask for a smaller step (for example, write a large file in several smaller Write and Edit calls), then retry.", secs)
}

// blockIndex reads the index of a content block event.
func blockIndex(data map[string]any) (int, bool) {
	switch v := data["index"].(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	}
	return 0, false
}
