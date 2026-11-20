package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

type nonStreamAnswer struct {
	Content []map[string]any `json:"content"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	StopReason string `json:"stop_reason"`
}

// nonStreamAnswerFor answers one non-streaming request from a fake backend that
// writes the given frames, and returns the decoded answer and the size of the
// body the backend received.
func nonStreamAnswerFor(t *testing.T, payloads ...string) (nonStreamAnswer, int) {
	t.Helper()
	var mu sync.Mutex
	bodyLen := 0
	streamTestSetup(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodyLen = len(b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		for _, p := range payloads {
			w.Write(cwFrame(p))
		}
	})

	rec := httptest.NewRecorder()
	if status := handleNonStreamRequestCtx(context.Background(), rec, testRequest(), logger.NewLogger(50), "sess", "req"); status != http.StatusOK {
		t.Fatalf("status = %d, body %s", status, rec.Body.String())
	}
	var got nonStreamAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	mu.Lock()
	defer mu.Unlock()
	return got, bodyLen
}

// Every tool call of a non-streaming answer reaches the client as its own
// tool_use block, in order, with its own id, name and input; the text keeps
// its place before them.
func TestNonStreamKeepsEveryToolCall(t *testing.T) {
	got, _ := nonStreamAnswerFor(t,
		`{"content":"Reading two files."}`,
		`{"name":"Read","toolUseId":"tu_1"}`,
		`{"name":"Read","toolUseId":"tu_1","input":"{\"file_path\":"}`,
		`{"name":"Read","toolUseId":"tu_1","input":"\"a.go\"}"}`,
		`{"name":"Read","toolUseId":"tu_1","stop":true}`,
		`{"name":"Grep","toolUseId":"tu_2"}`,
		`{"name":"Grep","toolUseId":"tu_2","input":"{\"pattern\":\"x\"}"}`,
		`{"name":"Grep","toolUseId":"tu_2","stop":true}`,
	)

	want := []map[string]any{
		{"type": "text", "text": "Reading two files."},
		{"type": "tool_use", "id": "tu_1", "name": "Read", "input": map[string]any{"file_path": "a.go"}},
		{"type": "tool_use", "id": "tu_2", "name": "Grep", "input": map[string]any{"pattern": "x"}},
	}
	if !reflect.DeepEqual(got.Content, want) {
		t.Fatalf("content =\n%v\nwant\n%v", got.Content, want)
	}
	if got.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", got.StopReason)
	}
}

// Text that follows a tool call stays after it.
func TestNonStreamTextAfterAToolKeepsItsPlace(t *testing.T) {
	got, _ := nonStreamAnswerFor(t,
		`{"name":"Read","toolUseId":"tu_1"}`,
		`{"name":"Read","toolUseId":"tu_1","input":"{}"}`,
		`{"name":"Read","toolUseId":"tu_1","stop":true}`,
		`{"content":"after"}`,
	)
	if len(got.Content) != 2 || got.Content[0]["type"] != "tool_use" || got.Content[1]["text"] != "after" {
		t.Fatalf("content = %v, want the tool then the text", got.Content)
	}
}

// Interleaved text and tools keep their order and each tool its own input.
func TestNonStreamInterleavedTextAndToolsKeepTheirOrder(t *testing.T) {
	got, _ := nonStreamAnswerFor(t,
		`{"name":"Read","toolUseId":"tu_1"}`,
		`{"name":"Read","toolUseId":"tu_1","input":"{\"file_path\":\"a\"}"}`,
		`{"name":"Read","toolUseId":"tu_1","stop":true}`,
		`{"content":"between"}`,
		`{"name":"Grep","toolUseId":"tu_2"}`,
		`{"name":"Grep","toolUseId":"tu_2","input":"{\"pattern\":\"b\"}"}`,
		`{"name":"Grep","toolUseId":"tu_2","stop":true}`,
		`{"content":"last"}`,
	)
	want := []map[string]any{
		{"type": "tool_use", "id": "tu_1", "name": "Read", "input": map[string]any{"file_path": "a"}},
		{"type": "text", "text": "between"},
		{"type": "tool_use", "id": "tu_2", "name": "Grep", "input": map[string]any{"pattern": "b"}},
		{"type": "text", "text": "last"},
	}
	if !reflect.DeepEqual(got.Content, want) {
		t.Fatalf("content =\n%v\nwant\n%v", got.Content, want)
	}
}

// Tool input the backend sent as broken JSON is not turned into an empty
// input: the client gets a 502 api_error and the log names the tool.
func TestNonStreamMalformedToolInputIsAnError(t *testing.T) {
	streamTestSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		for _, p := range []string{
			`{"content":"Running it."}`,
			`{"name":"Bash","toolUseId":"tu_bad"}`,
			`{"name":"Bash","toolUseId":"tu_bad","input":"{\"command\":"}`,
			`{"name":"Bash","toolUseId":"tu_bad","stop":true}`,
		} {
			w.Write(cwFrame(p))
		}
	})
	lg := logger.NewLogger(50)
	rec := httptest.NewRecorder()
	status := handleNonStreamRequestCtx(context.Background(), rec, testRequest(), lg, "sess", "req")
	if status != http.StatusBadGateway || rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d (recorder %d), want 502; body %s", status, rec.Code, rec.Body.String())
	}
	var envelope struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if envelope.Type != "error" || envelope.Error.Type != "api_error" || !strings.Contains(envelope.Error.Message, "malformed tool input") {
		t.Fatalf("envelope = %+v, want an api_error about malformed tool input", envelope)
	}
	if !loggedLine(lg, logger.LogTypeErr, "Bash", "tu_bad", "malformed") {
		t.Fatalf("no error line naming the tool; entries: %+v", lg.GetEntries())
	}
}

// Non-streaming usage uses the streaming path's estimates: input is the
// request body's bytes / 4, output the characters of text and tool input / 4.
func TestNonStreamUsageEstimatesTokensLikeStreaming(t *testing.T) {
	text := "Hello there, this is the answer text."
	input := `{"file_path":"some/long/path/name.go"}`
	inputJSON, _ := json.Marshal(input)
	got, bodyLen := nonStreamAnswerFor(t,
		`{"content":"`+text+`"}`,
		`{"name":"Read","toolUseId":"tu_1"}`,
		`{"name":"Read","toolUseId":"tu_1","input":`+string(inputJSON)+`}`,
		`{"name":"Read","toolUseId":"tu_1","stop":true}`,
	)
	if bodyLen == 0 {
		t.Fatal("the backend received no body")
	}
	if want := bodyLen / 4; got.Usage.InputTokens != want {
		t.Fatalf("input_tokens = %d, want %d (request bytes %d / 4)", got.Usage.InputTokens, want, bodyLen)
	}
	if want := (len(text) + len(input)) / 4; got.Usage.OutputTokens != want {
		t.Fatalf("output_tokens = %d, want %d", got.Usage.OutputTokens, want)
	}
}
