package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/reasoning"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
)

// bodyLog records the body of every request the fake backend receives.
type bodyLog struct {
	mu     sync.Mutex
	bodies []string
}

func (b *bodyLog) add(r *http.Request) string {
	raw, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bodies = append(b.bodies, string(raw))
	return string(raw)
}

func (b *bodyLog) all() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.bodies...)
}

const signatureInvalidBody = `{"message":"Invalid signature in thinking block","reason":"THINKING_SIGNATURE_INVALID"}`

// reasoningBackend answers the first turn (no history) with reasoning, a
// signature and a tool call, and any later turn with plain text. With
// rejectSignatures it answers 400 THINKING_SIGNATURE_INVALID to every request
// that carries reasoningContent.
func reasoningBackend(log *bodyLog, rejectSignatures bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := log.add(r)
		if rejectSignatures && strings.Contains(body, `"reasoningContent"`) {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, signatureInvalidBody)
			return
		}
		w.WriteHeader(http.StatusOK)
		if !strings.Contains(body, `"toolResults"`) {
			for _, p := range toolAnswer("I must read the file first.", "SIG-E2E", "tu_e2e") {
				w.Write(cwFrame(p))
			}
			w.Write(cwFrame(`{"stopReason":"TOOL_USE","modelId":"claude-sonnet-4.6"}`))
			return
		}
		w.Write(cwFrame(`{"content":"The file says hello."}`))
		w.Write(cwFrame(`{"stopReason":"END_TURN"}`))
	}
}

func reasoningHandlerSetup(t *testing.T, backend http.HandlerFunc) {
	t.Helper()
	withReasoningStore(t)
	streamTestSetup(t, backend)
	cfg := *config.Get()
	cfg.Advanced.ReasoningHistoryMaxBytes = config.DefaultReasoningHistoryMaxBytes
	cfg.Advanced.ToolMode = "full"
	cfg.Advanced.HistoryMode = "full"
	withConfig(t, &cfg)
}

type handlerRun func(t *testing.T, req AnthropicRequest, lg *logger.Logger) string

func runStream(t *testing.T, req AnthropicRequest, lg *logger.Logger) string {
	t.Helper()
	rec := newLiveRecorder()
	handleStreamRequestWithLogger(rec, req, lg, "sess", "req", nil)
	return rec.String()
}

func runNonStream(t *testing.T, req AnthropicRequest, lg *logger.Logger) string {
	t.Helper()
	rec := httptest.NewRecorder()
	handleNonStreamRequest(rec, req, lg, "sess", "req")
	return rec.Body.String()
}

var handlerRuns = map[string]handlerRun{"stream": runStream, "non-stream": runNonStream}

func firstTurnRequest() AnthropicRequest {
	return AnthropicRequest{Model: reasoningTestModel, MaxTokens: 64, Messages: []AnthropicRequestMessage{{Role: "user", Content: "read the file"}}}
}

func TestHandlerGivesTheReasoningBackOnTheNextTurn(t *testing.T) {
	for name, run := range handlerRuns {
		t.Run(name, func(t *testing.T) {
			log := &bodyLog{}
			reasoningHandlerSetup(t, reasoningBackend(log, false))

			lg := logger.NewLogger(100)
			first := run(t, firstTurnRequest(), lg)
			if !strings.Contains(first, "tu_e2e") {
				t.Fatalf("first answer lacks the tool call: %s", first)
			}
			for _, secret := range []string{"I must read", "SIG-E2E", "thinking", "signature"} {
				if strings.Contains(first, secret) {
					t.Fatalf("the client received %q: %s", secret, first)
				}
			}
			if !loggedLine(lg, logger.LogTypeInf, "Parser", "servedModel=claude-sonnet-4.6") {
				t.Fatalf("no Parser line with servedModel; entries: %+v", lg.GetEntries())
			}

			second := run(t, toolTurnRequest(reasoningTestModel, "tu_e2e"), lg)
			if !strings.Contains(second, "The file says hello.") {
				t.Fatalf("second answer = %s", second)
			}
			bodies := log.all()
			if len(bodies) != 2 {
				t.Fatalf("backend saw %d requests, want 2", len(bodies))
			}
			want := `"reasoningContent":{"reasoningText":{"text":"I must read the file first.","signature":"SIG-E2E"}}`
			if strings.Count(bodies[1], `"reasoningContent"`) != 1 || !strings.Contains(bodies[1], want) {
				t.Fatalf("second request lacks the reasoning exactly once:\n%s", bodies[1])
			}
			if strings.Contains(bodies[0], "reasoningContent") {
				t.Fatalf("first request had reasoning to give back:\n%s", bodies[0])
			}
			if !loggedLine(lg, logger.LogTypeInf, "Request convId=", "reasoningAttached=1", "reasoningBytes=34") {
				t.Fatalf("no request metrics line with the reasoning counts; entries: %+v", lg.GetEntries())
			}
			parserLines, withModel := 0, 0
			for _, e := range lg.GetEntries() {
				if strings.HasPrefix(e.Body, "Parser") {
					parserLines++
					if strings.Contains(e.Body, "servedModel=") {
						withModel++
					}
				}
			}
			if parserLines != 2 || withModel != 1 {
				t.Fatalf("Parser lines = %d, with servedModel = %d; want 2 and 1 (the second response names no model)", parserLines, withModel)
			}
		})
	}
}

func TestRejectedSignatureIsResentOnceWithoutReasoning(t *testing.T) {
	for name, run := range handlerRuns {
		t.Run(name, func(t *testing.T) {
			log := &bodyLog{}
			reasoningHandlerSetup(t, reasoningBackend(log, true))

			lg := logger.NewLogger(100)
			run(t, firstTurnRequest(), lg)
			if reasoningStore.Len() != 1 {
				t.Fatalf("store has %d entries after the first turn, want 1", reasoningStore.Len())
			}

			second := run(t, toolTurnRequest(reasoningTestModel, "tu_e2e"), lg)
			if !strings.Contains(second, "The file says hello.") {
				t.Fatalf("the client must see a normal answer, got: %s", second)
			}
			if strings.Contains(second, "THINKING_SIGNATURE_INVALID") || strings.Contains(second, `"error"`) {
				t.Fatalf("the client saw the rejection: %s", second)
			}
			bodies := log.all()
			if len(bodies) != 3 {
				t.Fatalf("backend saw %d requests, want 3 (first turn, rejected, resent)", len(bodies))
			}
			if !strings.Contains(bodies[1], `"reasoningContent"`) {
				t.Fatalf("the rejected request should have carried reasoning:\n%s", bodies[1])
			}
			if strings.Contains(bodies[2], "reasoningContent") {
				t.Fatalf("the resend still carries reasoning:\n%s", bodies[2])
			}
			if _, ok := reasoningStore.Get(getKiroModelID(reasoningTestModel), reasoning.ToolKey("tu_e2e")); ok {
				t.Fatal("the rejected reasoning must be evicted")
			}
			if !loggedLine(lg, logger.LogTypeInf, "THINKING_SIGNATURE_INVALID", "without") {
				t.Fatalf("no info line for the fallback; entries: %+v", lg.GetEntries())
			}
		})
	}
}

// A rejection of a request that carries no reasoning cannot be fixed by the
// fallback: it is not resent at all, let alone in a loop.
func TestSignatureRejectionWithoutReasoningIsNotRetried(t *testing.T) {
	for name, run := range handlerRuns {
		t.Run(name, func(t *testing.T) {
			log := &bodyLog{}
			reasoningHandlerSetup(t, func(w http.ResponseWriter, r *http.Request) {
				log.add(r)
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, signatureInvalidBody)
			})
			out := run(t, firstTurnRequest(), logger.NewLogger(100))
			if got := len(log.all()); got != 1 {
				t.Fatalf("backend saw %d requests, want 1", got)
			}
			if !strings.Contains(out, "error") {
				t.Fatalf("the client must see an error: %s", out)
			}
		})
	}
}

// An identity switch rebuilds only the profileArn: the reasoning attached to
// the history goes to the fallback identity too.
func TestReasoningSurvivesAnIdentitySwitch(t *testing.T) {
	for name, run := range handlerRuns {
		t.Run(name, func(t *testing.T) {
			log := &bodyLog{}
			var bearers []string
			var mu sync.Mutex
			reasoningHandlerSetup(t, func(w http.ResponseWriter, r *http.Request) {
				log.add(r)
				bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				mu.Lock()
				bearers = append(bearers, bearer)
				mu.Unlock()
				if bearer == primaryToken().AccessToken {
					w.WriteHeader(http.StatusPaymentRequired)
					io.WriteString(w, quotaBody)
					return
				}
				w.WriteHeader(http.StatusOK)
				w.Write(cwFrame(`{"content":"` + fallbackContent + `"}`))
			})
			withIdentities(t, map[string]TokenData{"": primaryToken(), "kiro2": fallbackToken()})
			cfg := *config.Get()
			cfg.Auth.FallbackProfiles = []string{"kiro2"}
			withConfig(t, &cfg)
			rememberResponse(t, getKiroModelID(reasoningTestModel), toolAnswer("plan for failover", "SIG-FO", "tu_fo")...)

			out := run(t, toolTurnRequest(reasoningTestModel, "tu_fo"), logger.NewLogger(100))
			if !strings.Contains(out, fallbackContent) {
				t.Fatalf("want the fallback's answer: %s", out)
			}
			bodies := log.all()
			if len(bodies) != 2 || bearers[0] == bearers[1] {
				t.Fatalf("want one request per identity, got %d with bearers %v", len(bodies), bearers)
			}
			for i, b := range bodies {
				if strings.Count(b, `"signature":"SIG-FO"`) != 1 {
					t.Fatalf("request %d lacks the reasoning:\n%s", i, b)
				}
			}
			if !strings.Contains(bodies[1], `"profileArn":"arn:kiro2"`) {
				t.Fatalf("the resend must carry the fallback's profileArn:\n%s", bodies[1])
			}
		})
	}
}

// Parallel requests of one session each get the reasoning of their own turn.
func TestParallelRequestsOfOneSessionGetTheirOwnReasoning(t *testing.T) {
	log := &bodyLog{}
	reasoningHandlerSetup(t, func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		w.WriteHeader(http.StatusOK)
		w.Write(cwFrame(`{"content":"ok"}`))
	})
	kiroModel := getKiroModelID(reasoningTestModel)
	const n = 8
	for i := 0; i < n; i++ {
		id := "tu_par" + string(rune('a'+i))
		rememberResponse(t, kiroModel, toolAnswer("plan "+id, "SIG-"+id, id)...)
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := toolTurnRequest(reasoningTestModel, "tu_par"+string(rune('a'+i)))
			req.Metadata = map[string]any{"user_id": `{"session_id":"ce40736e-1347-467a-9cce-181e245edd92"}`}
			if i%2 == 0 {
				runStream(t, req, logger.NewLogger(50))
			} else {
				runNonStream(t, req, logger.NewLogger(50))
			}
		}(i)
	}
	wg.Wait()

	bodies := log.all()
	if len(bodies) != n {
		t.Fatalf("backend saw %d requests, want %d", len(bodies), n)
	}
	for _, b := range bodies {
		if strings.Count(b, `"reasoningContent"`) != 1 {
			t.Fatalf("each request must carry exactly its own reasoning:\n%s", b)
		}
		var own string
		for i := 0; i < n; i++ {
			id := "tu_par" + string(rune('a'+i))
			if strings.Contains(b, `"toolUseId":"`+id+`"`) {
				own = id
			}
		}
		if own == "" || !strings.Contains(b, `"signature":"SIG-`+own+`"`) {
			t.Fatalf("request for %q carries another turn's reasoning:\n%s", own, b)
		}
	}
}

func TestIsThinkingSignatureInvalid(t *testing.T) {
	cases := map[string]bool{
		signatureInvalidBody:                                          true,
		`{"reason":"thinking_signature_invalid"}`:                     true,
		`not json THINKING_SIGNATURE_INVALID`:                         true,
		`{"reason":"INVALID_MODEL_ID"}`:                               false,
		`{"message":"THINKING_SIGNATURE_INVALID","reason":"OTHER"}`:   false,
		`{"message":"Improperly formed request.","reason":null}`:      false,
		`{"reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD","message":"x"}`: false,
	}
	for body, want := range cases {
		if got := isThinkingSignatureInvalid([]byte(body)); got != want {
			t.Errorf("isThinkingSignatureInvalid(%s) = %v, want %v", body, got, want)
		}
	}
}
