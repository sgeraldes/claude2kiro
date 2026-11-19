package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

const (
	scopeSessionA = "aaaaaaaa-1111-4111-8111-111111111111"
	scopeSessionB = "bbbbbbbb-2222-4222-8222-222222222222"
	scopeSessionC = "cccccccc-3333-4333-8333-333333333333"
)

// scriptedBackend answers the requests it receives with answers, in order.
// A request reject reports true for is answered 400 THINKING_SIGNATURE_INVALID
// and does not use up an answer.
func scriptedBackend(log *bodyLog, reject func(body string) bool, answers ...[]string) http.HandlerFunc {
	next := 0
	return func(w http.ResponseWriter, r *http.Request) {
		body := log.add(r)
		if reject != nil && reject(body) {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, signatureInvalidBody)
			return
		}
		w.WriteHeader(http.StatusOK)
		answer := []string{`{"content":"ok"}`}
		if next < len(answers) {
			answer = answers[next]
			next++
		}
		for _, p := range answer {
			w.Write(cwFrame(p))
		}
	}
}

// textAnswer is a text-only answer with signed reasoning.
func textAnswer(text, sig string) []string {
	return []string{`{"text":"thinking ` + sig + `"}`, `{"signature":"` + sig + `"}`, `{"content":"` + text + `"}`}
}

// inSession gives req the metadata Claude Code sends for session ("" sends none).
func inSession(req AnthropicRequest, session string) AnthropicRequest {
	if session != "" {
		req.Metadata = map[string]any{"user_id": `{"session_id":"` + session + `"}`}
	}
	return req
}

// textConversation is a conversation whose earlier assistant turns are the
// given texts, each after a user question, ending on a user question.
func textConversation(session string, turns ...string) AnthropicRequest {
	msgs := []AnthropicRequestMessage{{Role: "user", Content: "q0"}}
	for i, text := range turns {
		msgs = append(msgs,
			AnthropicRequestMessage{Role: "assistant", Content: text},
			AnthropicRequestMessage{Role: "user", Content: "q" + string(rune('1'+i))})
	}
	return inSession(AnthropicRequest{Model: reasoningTestModel, MaxTokens: 64, Messages: msgs}, session)
}

// historySignatures returns, for each assistant turn of a sent body's history,
// the signature of the reasoning it carries ("" when none), oldest first.
func historySignatures(t *testing.T, body string) []string {
	t.Helper()
	var decoded struct {
		ConversationState struct {
			History []struct {
				Assistant *struct {
					ReasoningContent *struct {
						ReasoningText *struct {
							Signature string `json:"signature"`
						} `json:"reasoningText"`
					} `json:"reasoningContent"`
				} `json:"assistantResponseMessage"`
			} `json:"history"`
		} `json:"conversationState"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	var out []string
	for _, h := range decoded.ConversationState.History {
		if h.Assistant == nil {
			continue
		}
		sig := ""
		if rc := h.Assistant.ReasoningContent; rc != nil && rc.ReasoningText != nil {
			sig = rc.ReasoningText.Signature
		}
		out = append(out, sig)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestIdenticalTextInTwoSessionsNeverSharesReasoning(t *testing.T) {
	for name, run := range handlerRuns {
		t.Run(name, func(t *testing.T) {
			log := &bodyLog{}
			reasoningHandlerSetup(t, scriptedBackend(log, nil, textAnswer("Yes.", "SIG-A"), textAnswer("Yes.", "SIG-B")))
			lg := logger.NewLogger(100)
			run(t, textConversation(scopeSessionA), lg)
			run(t, textConversation(scopeSessionB), lg)

			run(t, textConversation(scopeSessionA, "Yes."), lg)
			run(t, textConversation(scopeSessionB, "Yes."), lg)
			run(t, textConversation(scopeSessionC, "Yes."), lg)
			bodies := log.all()
			if len(bodies) != 5 {
				t.Fatalf("backend saw %d requests, want 5", len(bodies))
			}
			for i, want := range [][]string{{"SIG-A"}, {"SIG-B"}, {""}} {
				if got := historySignatures(t, bodies[2+i]); !equalStrings(got, want) {
					t.Fatalf("request %d carries %q, want %q:\n%s", 2+i, got, want, bodies[2+i])
				}
			}
		})
	}
}

func TestTwoIdenticalTextTurnsOfOneSessionEachGetTheirOwnReasoning(t *testing.T) {
	for name, run := range handlerRuns {
		t.Run(name, func(t *testing.T) {
			log := &bodyLog{}
			reasoningHandlerSetup(t, scriptedBackend(log, nil, textAnswer("Done.", "SIG-1"), textAnswer("Done.", "SIG-2")))
			lg := logger.NewLogger(100)
			run(t, textConversation(scopeSessionA), lg)
			run(t, textConversation(scopeSessionA, "Done."), lg)
			run(t, textConversation(scopeSessionA, "Done.", "Done."), lg)

			bodies := log.all()
			if got := historySignatures(t, bodies[2]); !equalStrings(got, []string{"SIG-1", "SIG-2"}) {
				t.Fatalf("turns carry %q, want each its own [SIG-1 SIG-2]:\n%s", got, bodies[2])
			}
		})
	}
}

// The response side concatenates the text frames; the client sends the turn
// back as several text blocks. Both hash the same normalized text.
func TestTextArrivingAsTwoBlocksIsFoundAgain(t *testing.T) {
	for name, run := range handlerRuns {
		t.Run(name, func(t *testing.T) {
			log := &bodyLog{}
			reasoningHandlerSetup(t, scriptedBackend(log, nil, []string{
				`{"text":"thinking"}`, `{"signature":"SIG-SPLIT"}`,
				`{"content":"Part one. "}`, `{"content":"Part   two.\n"}`,
			}))
			lg := logger.NewLogger(100)
			run(t, textConversation(scopeSessionA), lg)

			req := textConversation(scopeSessionA, "")
			req.Messages[1].Content = []any{
				map[string]any{"type": "text", "text": "Part one. "},
				map[string]any{"type": "text", "text": "Part two."},
			}
			run(t, req, lg)
			bodies := log.all()
			if got := historySignatures(t, bodies[1]); !equalStrings(got, []string{"SIG-SPLIT"}) {
				t.Fatalf("the turn sent back as two blocks carries %q, want [SIG-SPLIT]:\n%s", got, bodies[1])
			}
		})
	}
}

// A turn with tool calls is found by its tool use ids only, never by its text.
func TestToolTurnWithoutItsEntryGetsNoReasoning(t *testing.T) {
	log := &bodyLog{}
	reasoningHandlerSetup(t, scriptedBackend(log, nil, textAnswer("Reading it.", "SIG-TEXT")))
	lg := logger.NewLogger(100)
	runNonStream(t, textConversation(scopeSessionA), lg)

	req := inSession(toolTurnRequest(reasoningTestModel, "tu_gone"), scopeSessionA)
	runNonStream(t, req, lg)
	bodies := log.all()
	if strings.Contains(bodies[1], "reasoningContent") {
		t.Fatalf("a tool turn without a stored entry got reasoning:\n%s", bodies[1])
	}
}

// A request without a session keeps and finds reasoning by tool use id only.
func TestRequestsWithoutASessionUseToolKeysOnly(t *testing.T) {
	log := &bodyLog{}
	reasoningHandlerSetup(t, scriptedBackend(log, nil,
		textAnswer("Yes.", "SIG-NOSESSION"),
		toolAnswer("I must read it.", "SIG-TOOL", "tu_ns")))
	lg := logger.NewLogger(100)
	runNonStream(t, textConversation(""), lg)
	if reasoningStore.Len() != 0 {
		t.Fatalf("a text-only turn without a session was kept: %d entries", reasoningStore.Len())
	}
	runNonStream(t, textConversation("", "Yes."), lg)
	runNonStream(t, toolTurnRequest(reasoningTestModel, "tu_ns"), lg)

	bodies := log.all()
	if strings.Contains(bodies[1], "reasoningContent") {
		t.Fatalf("a text turn without a session got reasoning:\n%s", bodies[1])
	}
	if got := historySignatures(t, bodies[2]); !equalStrings(got, []string{"SIG-TOOL"}) {
		t.Fatalf("the tool turn carries %q, want [SIG-TOOL]:\n%s", got, bodies[2])
	}
}

// The fallback for a rejected signature forgets the reasoning of the request
// that was rejected and nothing of another session.
func TestSignatureRejectionEvictsOnlyTheRejectedRequestsReasoning(t *testing.T) {
	for name, run := range handlerRuns {
		t.Run(name, func(t *testing.T) {
			log := &bodyLog{}
			reject := func(body string) bool { return strings.Contains(body, `"signature":"SIG-A"`) }
			reasoningHandlerSetup(t, scriptedBackend(log, reject, textAnswer("Yes.", "SIG-A"), textAnswer("Yes.", "SIG-B")))
			lg := logger.NewLogger(100)
			run(t, textConversation(scopeSessionA), lg)
			run(t, textConversation(scopeSessionB), lg)

			run(t, textConversation(scopeSessionA, "Yes."), lg)
			run(t, textConversation(scopeSessionB, "Yes."), lg)
			bodies := log.all()
			if len(bodies) != 5 {
				t.Fatalf("backend saw %d requests, want 5 (two first turns, rejected, resent, B)", len(bodies))
			}
			if got := historySignatures(t, bodies[2]); !equalStrings(got, []string{"SIG-A"}) {
				t.Fatalf("the rejected request carried %q, want [SIG-A]:\n%s", got, bodies[2])
			}
			if got := historySignatures(t, bodies[4]); !equalStrings(got, []string{"SIG-B"}) {
				t.Fatalf("session B's reasoning was evicted: its request carries %q:\n%s", got, bodies[4])
			}
		})
	}
}
