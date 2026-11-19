# Protocol Translation: Anthropic API ↔ CodeWhisperer API

This document explains why Claude2Kiro needs to translate between two different API protocols and how the translation works.

## Overview

Claude Code and Kiro/CodeWhisperer use completely different API formats. The proxy acts as a **protocol translator** - not a modifier of content, but a converter between incompatible formats.

## The Two Protocols

### Anthropic Messages API (Claude Code)

**Endpoint:** `POST /v1/messages`

**Request Format:**
```json
{
  "model": "claude-sonnet-4-20250514",
  "messages": [
    {"role": "user", "content": "Hello"},
    {"role": "assistant", "content": "Hi there"}
  ],
  "tools": [
    {"name": "Bash", "description": "...", "input_schema": {...}}
  ],
  "max_tokens": 4096,
  "stream": true
}
```

**Response Format:** Server-Sent Events (SSE)
```
event: message_start
data: {"type": "message_start", "message": {"id": "msg_...", "role": "assistant"}}

event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text"}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Hello"}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 0}

event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}}

event: message_stop
data: {"type": "message_stop"}
```

### CodeWhisperer API (Kiro)

**Endpoint:** `POST https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse`

**Request Format:**
```json
{
  "conversationState": {
    "currentMessage": {
      "userInputMessage": {
        "content": "Hello",
        "userInputMessageContext": {
          "envState": {
            "operatingSystem": "windows",
            "currentWorkingDirectory": "G:\\Code\\Claude2Kiro"
          },
          "tools": [...]
        }
      }
    },
    "chatTriggerType": "MANUAL",
    "agentTaskType": "vibe",
    "conversationId": "session-derived-uuid",
    "customizationArn": "arn:aws:codewhisperer:..."
  },
  "profileArn": "arn:aws:codewhisperer:us-east-1:...",
  "additionalModelRequestFields": {
    "thinking": {"type": "disabled"},
    "output_config": {"effort": "medium"},
    "max_tokens": 64000
  }
}
```

Each key of `additionalModelRequestFields` is sent only when the model's catalog
schema declares it. When none applies the whole object is left out.

**Response Format:** Binary Event Stream
```
┌─────────────┬──────────────┬─────────────┬─────────────┬──────────┐
│ Total len   │ Header len   │ Headers     │ Payload     │ CRC32    │
│ (4 bytes)   │ (4 bytes)    │ (variable)  │ (variable)  │ (4 bytes)│
│ Big Endian  │ Big Endian   │             │ JSON        │          │
└─────────────┴──────────────┴─────────────┴─────────────┴──────────┘
```

**Event Types in Binary Stream:**
- `assistantResponseEvent` - Text content: `{"content": "Hello", "stop": false}`
- `toolUseEvent` - Tool calls: `{"name": "Bash", "toolUseId": "tooluse_...", "input": "{...}"}`
- `reasoningContentEvent` - The model's reasoning: `{"text": "..."}`, then `{"signature": "..."}`, or `{"redactedContent": "..."}`
- A stop frame - Why the model stopped: `{"stopReason": "TOOL_USE"}` (also `END_TURN`, `MAX_TOKENS`, `STOP_SEQUENCE`)
- `meteringEvent` - Usage tracking: `{"unit": "credit", "usage": 0.79}`
- `contextUsageEvent` - Context info: `{"contextUsagePercentage": 23.24}`

The parser tells frames apart by their payload keys; it does not read the frame
headers.

## Key Differences

| Aspect | Anthropic API | CodeWhisperer API |
|--------|---------------|-------------------|
| Transport | SSE (text) | Binary event stream |
| Content indexing | Explicit `index` field required | No indices |
| Tool format | Separate `content_block_start` + deltas | Combined events |
| Stop signaling | Single `message_delta` at end | Per-tool stop flags |
| Message structure | Flat messages array | Nested conversation state |

## Translation Process

### Request Translation (main.go: `buildCodeWhispererRequest`)

1. Extract messages from Anthropic format
2. Convert to CodeWhisperer conversation history
3. Map model names (e.g., `claude-sonnet-4-20250514` → `CLAUDE_SONNET_4_20250514_V1_0`)
4. Transform tool definitions to CodeWhisperer format
5. Add current Kiro protocol fields (`agentTaskType`, `additionalModelRequestFields`
   with `thinking`, `output_config.effort` and `max_tokens`, and `envState` when
   Claude Code sends an `<env>` system block)
6. Add required AWS metadata (profileArn, customizationArn)

#### Thinking (`advanced.thinking_mode`)

Without a `thinking` field the backend applies its default, which is adaptive
thinking on. Claude Code launched by `claude2kiro run` has thinking disabled
(`CLAUDE_CODE_DISABLE_THINKING=1`) and sends no `thinking` field, which in the
Anthropic API means no thinking. On one real agent step of 91k input tokens,
Opus 5.5 with adaptive thinking took 329 s and 8.26 credits, while Opus 4.8 with
thinking disabled took 13.2 s and 0.86 credits. Lowering the effort did not
shorten the reasoning.

The setting `advanced.thinking_mode` decides what the proxy sends:

- `client` (default) translates the client's request. A request with no
  `thinking` field, or with `"type": "disabled"`, becomes
  `"thinking": {"type": "disabled"}` when the model's schema lists `disabled`.
  A request with `"type": "enabled"` or `"adaptive"` becomes
  `"thinking": {"type": "adaptive"}` when the schema lists `adaptive`. The
  budget tokens are not forwarded. When the schema does not list the value
  needed, nothing is sent: Opus 5.5 lists only `adaptive` and Sonnet 5.5
  lists `adaptive` and `between_tools`, so thinking cannot be turned off for
  them.
- `backend` never sends a `thinking` field, as the proxy did before this
  setting existed.

An unknown value counts as `client`. Models with no schema (`auto`, the 4.5
models, DeepSeek, MiniMax, GLM, Qwen), models whose schema has no `thinking`
property (GPT, Grok) and models the catalog does not list get no thinking
field in either mode.

#### max_tokens

In both modes, when the model's schema declares `max_tokens` and the client sent
a positive `max_tokens`, the proxy sends it clamped to the schema's `minimum` and
`maximum`. Claude Code sends 128000; Opus 5.5 accepts at most 64000, so it gets
64000.

### Response Translation (parser/sse_parser.go)

1. Parse binary frames (length headers, CRC)
2. Extract JSON payloads from each frame
3. Convert to Anthropic SSE events:
   - `assistantResponseEvent` with `content` → `content_block_delta` with `text_delta`
   - `toolUseEvent` start → `content_block_start` with `tool_use`
   - `toolUseEvent` with `input` → `content_block_delta` with `input_json_delta`
   - `toolUseEvent` with `stop: true` → `content_block_stop`
4. Give each content block its index in arrival order, as the stream translator
   does: a run of text takes the next index when it starts, each tool call the
   next index when first seen, and text after a tool starts a new block
5. Add single `message_delta` at the end

Reasoning frames (`text`, `signature`, `redactedContent`), `contextUsagePercentage`
and metering frames produce no client events and do not take a block index.
Reasoning is not forwarded to the client. The proxy counts its characters and
keeps it to give it back to the backend on later turns (see below).

#### Reasoning given back (`advanced.reasoning_history_max_bytes`)

The backend streams the model's reasoning: `{"text": "..."}` frames, then one
`{"signature": "..."}`, or a `{"redactedContent": "..."}` blob. For `auto` the
signature is an opaque envelope starting with `.KTR~~` of 140 to 180 KB; for
Claude models it is a base64 string of up to about 70 KB. The official Kiro
client sends the reasoning of earlier turns back inside the history, and the
proxy does the same:

```
assistantResponseMessage: { content, toolUses: [...], reasoningContent: X }
X is one of:
  { "reasoningText": { "text": "<reasoning text>", "signature": "<signature>" } }
  { "redactedContent": <the blob, in the JSON encoding it arrived in> }
```

Without it the model plans every turn from scratch. In a controlled second turn
of a real 91k-token agent step with model `auto`, the request without the
reasoning took 290 s, produced 36,815 reasoning characters, cost 4.22 credits
and made one trivial tool call. The same request with the reasoning took 44.5 s,
produced 55 reasoning characters, cost 1.36 credits and did the real work.

Claude Code runs with thinking disabled and never receives or sends thinking
blocks, so the client is not involved. Both translators keep the last
reasoning block of each response: text appended across frames, the latest
signature and the latest redacted blob. Reasoning that arrives after assistant
text or a tool call starts a new block that replaces the earlier one. A block
goes back only when it has text with a signature, or redacted content; unsigned
reasoning is dropped. When a block has both, the text and signature are sent.

After a successful response the proxy stores the block in memory. Every key
is scoped to the conversation: the full session id Claude Code sends in
`metadata.user_id` (the one the stable `conversationId` is derived from) plus
a fingerprint of the conversation's first message. Claude Code sends one
session id for a conversation and for every Task subagent it spawns, so the
session alone would let a parent and its subagent share keys.

The fingerprint is a SHA-256 hash of the role of `messages[0]` and its text,
truncated to its first 128 bits. Claude Code opens that message with
`<system-reminder>` blocks (instructions, a git status snapshot, an
attribution note) that can change between requests of one conversation, and
ends it with the prompt, which for a subagent is the task its parent wrote.
The hash therefore covers the text blocks joined with a space, with every
`<system-reminder>...</system-reminder>` span removed and whitespace collapsed
as for turn text. The spans are removed from the joined text, so a reminder
that opens in one block and closes in a later one is removed too. The result
is stable across a conversation's turns and differs between a parent and its
subagents. A first message that is all reminders hashes all its text; one
with no text at all hashes its JSON with `cache_control` removed, since the
client moves cache markers between requests. The proxy computes the
fingerprint once per request, when it builds the backend request: lookup (the
history's turns) and capture (storing the response) both use that value, and
a resend of the same request, without reasoning or to another identity, keeps
it. A later request computes it from its own `messages[0]`. When the client
compacts or rewrites the first message the fingerprint changes, and every
earlier entry of that conversation, text or tool keyed, becomes a miss: the
turns carry no reasoning and the model thinks again. It never gets another
conversation's reasoning.

- A response that emitted tool calls is stored under the conversation scope
  plus each `toolUseId`, and a history turn with tool calls is looked up by
  those keys only, never by its text.
- A response with no tool call is stored under a SHA-256 hash of the
  conversation scope, the turn's position and its assistant text. The text is the concatenation of
  the response's text frames, or of the turn's text blocks in a later
  request, with every run of whitespace collapsed to one space and the ends
  trimmed, so a turn the client sends back split into several blocks is still
  found. The position is the number of
  assistant messages in the request that produced the response; in a later
  request it is the turn's index among that request's assistant messages. Both
  count the client's `messages`, not the history sent to the backend, so a
  history mode that drops entries does not shift them. Two turns of one
  conversation that said the same thing therefore each get their own
  reasoning, and so do a parent and a subagent that said the same thing at
  the same position.
- A request with no session id (a client that sends no `metadata`) uses
  unscoped tool keys only (tool use ids are unique) and no fingerprint. Its
  text-only turns are neither stored nor looked up, so two clients without a
  session never share reasoning.

A response whose reasoning is not stored (unsigned, too large, or the feature
off) removes whatever an earlier response left under the same keys, so a turn
can only get reasoning that its own response produced. After the client
compacts a conversation the positions restart and the summarised turns are
gone: their entries are simply not found (a miss), and a new turn at a reused
position replaces the old entry when its response arrives. Two conversations
of one session whose first messages have the same text once the reminders are
removed (for example two subagents given the identical task) share a scope, as
two turns of one conversation would.

The entry records the Kiro model id of the request (`auto` for `auto`). The
store holds all sessions together, bounded to 128 MiB in total, and evicts the
least recently used entries first. It is not written to disk: after a restart
the next turn simply carries no reasoning and the model thinks again.

When the proxy builds a request, it looks up each assistant turn of the history
by the keys above and attaches `reasoningContent` only when the entry's model
id equals the current request's Kiro model id. It attaches newest turns first and skips any turn
whose reasoning would take the request past
`advanced.reasoning_history_max_bytes` (default 4194304, 4 MiB; text plus
signature or redacted bytes), then keeps trying older turns, so one oversized
block does not leave every older turn without its reasoning. `0` turns the feature off: nothing is kept and
nothing is attached. Turns a history mode drops carry nothing, and the cap only
counts turns that are sent. In `tool_mode: none_text` nothing is attached,
because that history holds tool calls as text, which is not what the reasoning
was signed with. A turn with no entry has no `reasoningContent` key at all.

If the backend answers 400 with reason `THINKING_SIGNATURE_INVALID` to a request
that carries reasoning, the proxy resends it once with every `reasoningContent`
removed, forgets the turns it had attached (only that request's entries:
the keys are scoped to its conversation) and logs one info line starting with
`THINKING_SIGNATURE_INVALID`. The resend does not count as a retry. A request
that carries no reasoning and still gets that reason is not resent; the client
gets a non-retryable error.

The request log line ends with `reasoningAttached=<turns> reasoningBytes=<bytes>`.
When a response frame carries a `modelId`, the `Parser:` line ends with
`servedModel=<id>`, the last one seen; this is how to learn which model `auto`
picked.

#### Stop reason

The `message_delta` carries the stop reason the backend sent in its
`stopReason` frame: `END_TURN` becomes `end_turn`, `TOOL_USE` becomes
`tool_use`, `MAX_TOKENS` becomes `max_tokens` and `STOP_SEQUENCE` becomes
`stop_sequence`. When the frame is missing or holds another value the proxy
derives it as before: `tool_use` if the response used a tool, else `end_turn`.
Both the streaming and the non-streaming paths do this.

A model can spend its whole output budget on reasoning and stop with
`MAX_TOKENS` before writing anything. The proxy then answers a normal message
with one text block, `[Kiro stopped at the output limit before producing an
answer]`, and `stop_reason: "max_tokens"`, and logs an error line with the
reasoning size. A response with no content and any other stop reason is
answered as before (`[Error: No response received from backend]` when
streaming).

### Streaming (parser/stream.go, stream_keepalive.go)

A streaming request (`"stream": true`) is forwarded frame by frame. `parser.ReadFramePayload` reads one binary frame from the backend response and `parser.StreamTranslator` turns it into Anthropic events, which the proxy sends before it reads the next frame. The client sees text and tool input while the backend is still generating.

- Blocks get their index in arrival order. A text block is closed before a tool block starts, and text that arrives after a tool opens a new block. A tool block stays open until its own stop frame or the end of the stream: text that arrives while it is open goes to a new text block, so two blocks can be open at once and no tool input is lost. That order is handled defensively: in the live responses captured so far text always came before the tool frames. Clients follow blocks by index.
- `message_start` goes out with the first content frame. `message_delta` and `message_stop` close the response only when the backend stream ends on a frame boundary. A stream that ends inside a frame, a frame with impossible lengths or a transport error is a failed response: the client gets an `error` event (`overloaded_error`, so it retries) and no `message_stop`.
- While the stream is silent the proxy sends a `ping` event every `network.stream_keepalive_interval` (default 15s, `0` turns the pings off), including the time before the backend's first byte, so a `ping` can arrive before `message_start`. The value is read when a request starts, so a change in the settings screen applies to the next request. Without the pings, a client that hears nothing for minutes gives up and resends the whole request while the first one is still running.
- When the client disconnects, the backend request is cancelled (streaming and non-streaming) and its slot in `max_concurrent_requests` is freed. A streaming request that is still waiting for a slot stops waiting and never reaches the backend, and a retry backoff ends at once. Whether Kiro stops charging for a generation cancelled midway has not been measured.
- Each streaming write (event frames and pings) has a 60-second write deadline; the servers have no global write timeout, which would cut long streams. A client that stops reading without closing makes a write miss its deadline, and the proxy then treats it as disconnected: nothing more is written and the backend request is cancelled as above. A non-streaming answer is written and flushed under the same 60-second deadline, which is cleared afterwards, so a client that stops reading cannot hold that handler either.
- The log line `Parser: N events, ... queueWait=… firstEvent=… stopReason=… reasoningChars=…` shows how long the request waited for a concurrency slot, how long the backend took to produce its first content, the stop reason as the backend sent it (`none` when it sent none) and how many characters of reasoning it produced. A trailing `servedModel=…` names the model the backend said served the response, when it said.

Non-streaming requests (`"stream": false`) still read the whole backend response and translate it with `parser.ParseEventsWithSummary`, which also returns the stop reason and the reasoning size. Their log line is `Parser (non-stream): N events, hasText=…, hasToolUse=…, stopReason=… reasoningChars=…`, with `servedModel=…` when the backend named a model. The answer's `content` keeps the arrival order: one `text` block per run of text and one `tool_use` block per tool call, each with its own `id`, `name` and `input`. A tool call with no input gets `input: {}`. A tool call whose input the backend sent as JSON that does not parse is not turned into an empty input: the proxy logs an error line naming the tool, its id and the parse error, and answers `502` with an `api_error` envelope saying the backend sent malformed tool input. The streaming path forwards the backend's input fragments unchanged as `input_json_delta` events, and the client parses the assembled JSON itself.

Kiro reports no token counts, so `usage` is an estimate, the same on both paths: `input_tokens` is the size of the request sent to the backend in bytes / 4, `output_tokens` the characters of text and tool input / 4.

## Credit-Efficiency Optimizations

Credits scale with how many tokens the backend ingests, so the proxy mirrors the
current Kiro IDE session identifier behavior and keeps protocol-level cache
markers where Claude Code supplies them. Default behavior remains conservative:
stable conversation IDs are on, while history/tool shrinking stays off until
benchmarks prove the trade-off is safe.

- **Stable conversationId per session (default ON).** Current Kiro IDE uses its
  `chatSessionId` as the `conversationId` and reuses it across turns while still sending
  previous turns in `history`. Claude2Kiro mirrors that by hashing the per-session UUID
  Claude Code sends in `metadata.user_id` (current builds use a JSON string with
  `session_id`; older builds used `..._session_<uuid>`) into a deterministic,
  well-formed `conversationId` (`stableConversationID`), so all turns of one Claude Code
  session map to the same backend conversation id. Disable
  `advanced.stable_conversation_id` to restore the older proxy behavior of a fresh
  random UUID per request, or when a client sends no session UUID metadata.
- **Tool-level `cache_control` → Kiro `cachePoint`.** When Claude Code marks a tool
  with Anthropic `cache_control`, the proxy emits a sibling `{"cachePoint":{"type":"default"}}`
  entry in the CodeWhisperer `tools` array immediately after that tool. The current Kiro
  protocol SDK supports `cachePoint`, but installed Kiro IDE request-builder code does
  not appear to add it in the q-developer-converse path; treat this as a compatibility
  optimization for Claude-provided cache boundaries. The total emitted entries (tool
  entries + cachePoint entries) are capped at the tool-count limit
  (`network.max_tools_per_request`, default 85) so cachePoints cannot push the array past
  the backend's ~90-tool / ~260KB body limit.
- **Each tool result is sent once.** A `tool_result` block travels only in
  `userInputMessageContext.toolResults`. The user message `content` holds that
  message's own text blocks, and is the empty string when the message has only tool
  results, which Kiro accepts. This applies to the current message and to every history
  entry. Before, the result text was also copied into `content`, so a large file read
  was ingested twice; in one captured request 99,159 of about 340,000 bytes were such
  copies. `tool_mode: none_text` is unchanged: there the results are plain text in
  `content` and no structured results are sent. The request log reports
  `contentChars` (the `content` field) and `toolResultChars` (current tool results)
  separately.
- **Images inside a tool result.** A `tool_result` whose `content` array holds an
  `image` block (a screenshot tool, for example) has its text items sent in
  `toolResults` and its base64 images sent in the current message's `images`,
  after the message's own top-level images, in order. Only the current message
  carries images: history user messages have no `images` field in the request
  format, so images of earlier turns, top-level or inside a tool result, are
  not sent.
- **Native effort passthrough.** Claude Code `output_config.effort`, or `thinking`
  of type `enabled` or `adaptive` (which get the default effort), is translated to Kiro's
  `additionalModelRequestFields.output_config.effort` for models that advertise an
  effort enum. Unsupported or absent effort is omitted. The enum is read from the
  live catalog (`additionalModelRequestFieldsSchema` in `ListAvailableModels`), so a
  model released after this build gets the requested effort too. A static table of
  the levels known at build time covers the case where the catalog is unreachable.
  `auto` accepts no request fields, so no effort is sent for it and Kiro decides.
  Effort is sent next to `thinking` and `max_tokens` when those apply (see
  Request Translation above). When the proxy sends `thinking: disabled`, it
  does what the official Kiro CLI does: the effort is still sent, but `xhigh`
  and `max` are lowered to the highest other level the model lists (`high` for
  the current Claude models); `low`, `medium` and `high` pass unchanged. The
  backend accepted `claude-opus-5` with `thinking: disabled` and effort
  `medium` in a live check.
- **Environment state passthrough.** Claude Code's `<env>` system block is parsed for
  working directory and platform, then attached to the current message as
  `envState`. This is protocol parity with current Kiro clients, not a direct token
  saving feature.
- **Metering event logging.** Kiro binary `meteringEvent` frames with `unit: credit`
  are parsed separately from Claude-facing SSE conversion and logged as
  `Kiro metering: ...`, so benchmark runs can compare exact per-response credit usage
  when the backend sends it.
- **Experimental request diet settings (default OFF).** The TUI exposes:
  `advanced.history_mode` (`full`, `recent`, `current_only`),
  `advanced.history_recent_turns`, `advanced.tool_mode` (`full`, `compact`,
  `none_text`), `advanced.tool_compact_max_chars`, and
  `advanced.aggressive_cache_points`. These can reduce request size for controlled
  tests, but they can remove context or tools Claude Code expects, so defaults preserve
  full history and full tools. History trimming remains protocol-aware: if a kept
  current or historical `tool_result` references a prior assistant `tool_use`, the
  proxy keeps the minimal matching user/assistant tool-use pair to avoid backend
  `TOOL_USE_RESULT_MISMATCH` errors.
- **Runtime endpoint status.** The helper `kiroRuntimeEndpoint(region)` documents the
  current Kiro runtime host shape (`https://runtime.<region>.kiro.dev/`). The proxy
  still defaults to the existing CodeWhisperer endpoint until local captures confirm
  the exact current Kiro CLI header/signing requirements for the runtime endpoint.

## Why This Matters

The proxy doesn't modify the semantic content - it translates the protocol. Like a translator converting between English and Japanese, the meaning stays the same but the grammar and structure change completely.

Without this translation:
- Claude Code would receive binary garbage it can't parse
- Request formats would be incompatible
- Streaming wouldn't work
- Tool calls would fail completely
