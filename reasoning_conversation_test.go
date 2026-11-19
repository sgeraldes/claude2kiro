package main

import (
	"strings"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// claudeCodeFirstMessage is the first message of a Claude Code conversation as
// a logged request carried it: injected <system-reminder> blocks (the CLAUDE.md
// contents, a git status snapshot, the attribution note) and then the prompt,
// which for a Task subagent is the task the parent wrote.
func claudeCodeFirstMessage(prompt, gitStatus string) []any {
	return []any{
		map[string]any{"type": "text", "text": "<system-reminder>\nCodebase and user instructions are shown below.\n# CLAUDE.md\nBuild with go build.\n</system-reminder>"},
		map[string]any{"type": "text", "text": "<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# gitStatus\n" + gitStatus + "\n</system-reminder>"},
		map[string]any{"type": "text", "text": "<system-reminder>\nAttribution for git commits and pull requests you create from here on.\n</system-reminder>\n"},
		map[string]any{"type": "text", "text": prompt},
	}
}

const (
	parentPrompt   = "Refactor the parser so frames are read once."
	subagentPrompt = "Find every caller of ParseEvents and report them. Do not edit files."
)

// A parent conversation and its Task subagent share the session id. A
// text-only turn with the same text at the same position gets, in each, the
// reasoning its own response produced.
func TestTwoConversationsOfOneSessionNeverShareTextReasoning(t *testing.T) {
	for name, run := range handlerRuns {
		t.Run(name, func(t *testing.T) {
			log := &bodyLog{}
			reasoningHandlerSetup(t, scriptedBackend(log, nil, textAnswer("Done.", "SIG-PARENT"), textAnswer("Done.", "SIG-SUB")))
			lg := logger.NewLogger(100)
			parent := claudeCodeFirstMessage(parentPrompt, "Current branch: main")
			sub := claudeCodeFirstMessage(subagentPrompt, "Current branch: main")
			run(t, textConversationFrom(scopeSessionA, parent), lg)
			run(t, textConversationFrom(scopeSessionA, sub), lg)

			run(t, textConversationFrom(scopeSessionA, parent, "Done."), lg)
			run(t, textConversationFrom(scopeSessionA, sub, "Done."), lg)
			bodies := log.all()
			if len(bodies) != 4 {
				t.Fatalf("backend saw %d requests, want 4", len(bodies))
			}
			if got := historySignatures(t, bodies[2]); !equalStrings(got, []string{"SIG-PARENT"}) {
				t.Fatalf("the parent's turn carries %q, want [SIG-PARENT]:\n%s", got, bodies[2])
			}
			if got := historySignatures(t, bodies[3]); !equalStrings(got, []string{"SIG-SUB"}) {
				t.Fatalf("the subagent's turn carries %q, want [SIG-SUB]:\n%s", got, bodies[3])
			}
		})
	}
}

// One conversation over three requests: the history grows, the first message
// stays, and both the text turn and the tool turn find their reasoning.
func TestGrowingConversationFindsItsReasoning(t *testing.T) {
	for name, run := range handlerRuns {
		t.Run(name, func(t *testing.T) {
			log := &bodyLog{}
			reasoningHandlerSetup(t, scriptedBackend(log, nil,
				textAnswer("Done.", "SIG-1"),
				toolAnswer("I must read it.", "SIG-2", "tu_grow")))
			lg := logger.NewLogger(100)
			first := claudeCodeFirstMessage(parentPrompt, "Current branch: main")
			run(t, textConversationFrom(scopeSessionA, first), lg)
			run(t, textConversationFrom(scopeSessionA, first, "Done."), lg)

			req := textConversationFrom(scopeSessionA, first, "Done.")
			req.Messages = append(req.Messages,
				AnthropicRequestMessage{Role: "assistant", Content: []any{map[string]any{"type": "text", "text": "Reading it."}, toolUseBlock("tu_grow")}},
				AnthropicRequestMessage{Role: "user", Content: []any{toolResultBlock("tu_grow", "file body")}})
			run(t, req, lg)
			bodies := log.all()
			if got := historySignatures(t, bodies[2]); !equalStrings(got, []string{"SIG-1", "SIG-2"}) {
				t.Fatalf("turns carry %q, want [SIG-1 SIG-2]:\n%s", got, bodies[2])
			}
		})
	}
}

// Claude Code re-sends the first message with its reminders; a reminder whose
// text changes between requests does not move the conversation.
func TestChangedReminderInTheFirstMessageKeepsTheConversation(t *testing.T) {
	for name, run := range handlerRuns {
		t.Run(name, func(t *testing.T) {
			log := &bodyLog{}
			reasoningHandlerSetup(t, scriptedBackend(log, nil, textAnswer("Done.", "SIG-R")))
			lg := logger.NewLogger(100)
			run(t, textConversationFrom(scopeSessionA, claudeCodeFirstMessage(parentPrompt, "Status:\n M main.go")), lg)
			run(t, textConversationFrom(scopeSessionA, claudeCodeFirstMessage(parentPrompt, "Status:\n M main.go\n M store.go"), "Done."), lg)
			bodies := log.all()
			if got := historySignatures(t, bodies[1]); !equalStrings(got, []string{"SIG-R"}) {
				t.Fatalf("the turn carries %q, want [SIG-R]:\n%s", got, bodies[1])
			}
		})
	}
}

// After the client compacts a conversation its first message is a summary: the
// earlier entries are a miss, never another conversation's reasoning.
func TestCompactedFirstMessageMissesInsteadOfAttaching(t *testing.T) {
	for name, run := range handlerRuns {
		t.Run(name, func(t *testing.T) {
			log := &bodyLog{}
			reasoningHandlerSetup(t, scriptedBackend(log, nil,
				textAnswer("Done.", "SIG-OLD"),
				toolAnswer("I must read it.", "SIG-OLD-TOOL", "tu_compact")))
			lg := logger.NewLogger(100)
			first := claudeCodeFirstMessage(parentPrompt, "Current branch: main")
			run(t, textConversationFrom(scopeSessionA, first), lg)
			run(t, textConversationFrom(scopeSessionA, first, "Done."), lg)

			summary := claudeCodeFirstMessage("This session is being continued from a previous conversation. Summary: the parser was refactored.", "Current branch: main")
			req := textConversationFrom(scopeSessionA, summary, "Done.")
			req.Messages = append(req.Messages,
				AnthropicRequestMessage{Role: "assistant", Content: []any{map[string]any{"type": "text", "text": "Reading it."}, toolUseBlock("tu_compact")}},
				AnthropicRequestMessage{Role: "user", Content: []any{toolResultBlock("tu_compact", "file body")}})
			run(t, req, lg)
			bodies := log.all()
			if len(bodies) != 3 {
				t.Fatalf("backend saw %d requests, want 3", len(bodies))
			}
			if strings.Contains(bodies[2], "reasoningContent") {
				t.Fatalf("a compacted conversation got reasoning stored before compaction:\n%s", bodies[2])
			}
		})
	}
}

func TestConversationFingerprint(t *testing.T) {
	user := func(content any) []AnthropicRequestMessage {
		return []AnthropicRequestMessage{{Role: "user", Content: content}, {Role: "assistant", Content: "later turns do not count"}}
	}
	image := func(data string, cached bool) []any {
		block := map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": data}}
		if cached {
			block["cache_control"] = map[string]any{"type": "ephemeral"}
		}
		return []any{block}
	}

	parentA := conversationFingerprint(user(claudeCodeFirstMessage(parentPrompt, "Status: clean")))
	parentB := conversationFingerprint(user(claudeCodeFirstMessage(parentPrompt, "Status:\n M main.go")))
	sub := conversationFingerprint(user(claudeCodeFirstMessage(subagentPrompt, "Status: clean")))
	if parentA == "" || parentA != parentB {
		t.Fatalf("a changed reminder moved the fingerprint: %q vs %q", parentA, parentB)
	}
	if sub == parentA {
		t.Fatal("a subagent's first message has its parent's fingerprint")
	}

	split := conversationFingerprint(user([]any{
		map[string]any{"type": "text", "text": "Refactor the parser"},
		map[string]any{"type": "text", "text": "  so frames are read once."},
	}))
	if plain := conversationFingerprint(user("Refactor the parser so frames are read once.")); split != plain {
		t.Fatalf("whitespace and block splits moved the fingerprint: %q vs %q", split, plain)
	}
	embedded := conversationFingerprint(user("<system-reminder>\nchanging context 1\n</system-reminder>\n" + parentPrompt))
	if other := conversationFingerprint(user("<system-reminder>\nchanging context 2\n</system-reminder>\n" + parentPrompt)); embedded != other {
		t.Fatalf("a reminder inside string content moved the fingerprint: %q vs %q", embedded, other)
	}

	onlyReminders1 := conversationFingerprint(user("<system-reminder>one</system-reminder>"))
	onlyReminders2 := conversationFingerprint(user("<system-reminder>two</system-reminder>"))
	if onlyReminders1 == "" || onlyReminders1 == onlyReminders2 {
		t.Fatalf("a first message of reminders only must hash all its text: %q, %q", onlyReminders1, onlyReminders2)
	}

	img := conversationFingerprint(user(image("AAAA", false)))
	if img == "" || img != conversationFingerprint(user(image("AAAA", true))) {
		t.Fatal("a first message without text must hash its JSON without cache_control")
	}
	if img == conversationFingerprint(user(image("BBBB", false))) {
		t.Fatal("two different first messages without text share a fingerprint")
	}

	asSystem := conversationFingerprint([]AnthropicRequestMessage{{Role: "system", Content: parentPrompt}})
	if asSystem == conversationFingerprint(user(parentPrompt)) {
		t.Fatal("the first message's role is not part of the fingerprint")
	}
}
