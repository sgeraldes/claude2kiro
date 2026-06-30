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
    "output_config": {"effort": "medium"}
  }
}
```

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
- `meteringEvent` - Usage tracking: `{"unit": "credit", "usage": 0.79}`
- `contextUsageEvent` - Context info: `{"contextUsagePercentage": 23.24}`

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
5. Add current Kiro protocol fields (`agentTaskType`, native `output_config.effort`,
   and `envState` when Claude Code sends an `<env>` system block)
6. Add required AWS metadata (profileArn, customizationArn)

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
- **Native effort passthrough.** Claude Code `output_config.effort` or enabled
  `thinking` is translated to Kiro's
  `additionalModelRequestFields.output_config.effort` for models that advertise an
  effort enum. Unsupported or absent effort is omitted.
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
