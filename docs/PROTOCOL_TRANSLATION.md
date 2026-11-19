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
- `reasoningContentEvent` - The model's reasoning: `{"text": "..."}`, then `{"signature": "..."}`
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
4. Generate proper indices for each content block
5. Add single `message_delta` at the end

Reasoning frames (`text`, `signature`), `contextUsagePercentage` and metering
frames produce no client events and do not take a block index. Reasoning is not
forwarded to the client; the proxy only counts its characters.

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

- Blocks get their index in arrival order. A text block is closed before a tool block starts, and text that arrives after a tool opens a new block.
- `message_start` goes out with the first content frame. `message_delta` and `message_stop` close the response when the backend stream ends.
- While the stream is silent the proxy sends a `ping` event every 15 seconds, including the time before the backend's first byte. Without it, a client that hears nothing for minutes gives up and resends the whole request while the first one is still running.
- When the client disconnects, the backend request is cancelled (streaming and non-streaming). It stops consuming credits and frees its slot in `max_concurrent_requests`.
- The log line `Parser: N events, ... queueWait=… firstEvent=… stopReason=… reasoningChars=…` shows how long the request waited for a concurrency slot, how long the backend took to produce its first content, the stop reason as the backend sent it (`none` when it sent none) and how many characters of reasoning it produced.

Non-streaming requests (`"stream": false`) still read the whole backend response and translate it with `parser.ParseEventsWithSummary`, which also returns the stop reason and the reasoning size. Their log line is `Parser (non-stream): N events, hasText=…, hasToolUse=…, stopReason=… reasoningChars=…`.

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
- **Native effort passthrough.** Claude Code `output_config.effort` or enabled
  `thinking` is translated to Kiro's
  `additionalModelRequestFields.output_config.effort` for models that advertise an
  effort enum. Unsupported or absent effort is omitted. The enum is read from the
  live catalog (`additionalModelRequestFieldsSchema` in `ListAvailableModels`), so a
  model released after this build gets the requested effort too. A static table of
  the levels known at build time covers the case where the catalog is unreachable.
  `auto` accepts no request fields, so no effort is sent for it and Kiro decides.
  Effort is sent next to `thinking` and `max_tokens` when those apply (see
  Request Translation above).
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
