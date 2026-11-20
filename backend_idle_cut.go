package main

import (
	"bytes"
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
// minutes earlier stayed silent and was cut 240.0 s after its last frame. In
// the three runs that hit it, the stream was cut, Claude Code resent it as a
// non-streaming request every four minutes, and every resend was cut too.
// The proxy cannot keep the backend's stream alive, so it lets the client
// resend once (the streaming attempt fails as retryable, and Claude Code falls
// back to a non-streaming request) and answers a cut non-streaming request as
// final, with what happened and what to ask for instead.

// backendIdleCutAfter is the silence after which a broken stream is taken for
// that cut. It sits a little under the backend's 240 s so the measured cuts
// (240.0 s) fall above it; tests shorten it.
var backendIdleCutAfter = 230 * time.Second

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
// silent for at least backendIdleCutAfter. A clean end and a malformed frame
// are not cuts.
func isBackendIdleCut(err error, silence time.Duration) bool {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, parser.ErrInvalidFrame) {
		return false
	}
	return silence >= backendIdleCutAfter
}

// backendIdleCutMessage is what the client is told about such a cut. tool is
// the tool call the backend had started, or "" when it had started none.
func backendIdleCutMessage(silence time.Duration, tool string) string {
	secs := int(silence.Round(time.Second) / time.Second)
	if tool != "" {
		return fmt.Sprintf("Kiro closed the response after %d s without sending anything (it closes a response that stays silent for 240 s). "+
			"The model had started the %q tool call, and Kiro sends a tool call's input only once it is complete, "+
			"so an input that takes longer than 240 s to write never arrives. Resending the same request usually ends the same way. "+
			"Ask for the content in smaller pieces (for example, several smaller Write or Edit calls), then retry.", secs, tool)
	}
	return fmt.Sprintf("Kiro closed the response after %d s without sending anything (it closes a response that stays silent for 240 s). "+
		"Resending the same request usually ends the same way. Ask for a smaller step, then retry.", secs)
}

// lastToolCallName is the name of the last tool call whose start is in the
// complete frames of a partial response, or "".
func lastToolCallName(partial []byte) string {
	name := ""
	r := bytes.NewReader(partial)
	tr := parser.NewStreamTranslator()
	for {
		payload, err := parser.ReadFramePayload(r)
		if err != nil {
			return name
		}
		for _, e := range tr.Translate(payload) {
			if data, ok := e.Data.(map[string]any); ok {
				if cb, ok := data["content_block"].(map[string]any); ok && cb["type"] == "tool_use" {
					name, _ = cb["name"].(string)
				}
			}
		}
	}
}
