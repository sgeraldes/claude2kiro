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
        "userInputMessageContext": {...}
      }
    },
    "chatTriggerType": "MANUAL",
    "customizationArn": "arn:aws:codewhisperer:..."
  },
  "profileArn": "arn:aws:codewhisperer:us-east-1:..."
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
5. Add required AWS metadata (profileArn, customizationArn)

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

Credits scale with how many tokens the backend ingests, so the proxy mirrors two
behaviors of the real Kiro/Amazon Q IDE to avoid re-billing context every turn:

- **Stable conversationId per session (OPT-IN, default OFF).** The real IDE reuses a
  single server-assigned `conversationId` for the lifetime of a chat session, letting
  the backend retain context server-side. When enabled via
  `advanced.stable_conversation_id`, `buildCodeWhispererRequest` hashes the per-session
  UUID Claude Code sends in `metadata.user_id` (`..._session_<uuid>`) into a
  deterministic, well-formed `conversationId` (`stableConversationID`), so all turns of
  one session map to the same id. **It is off by default** because the proxy still sends
  the full history array on every request and the backend's server-side retention is
  **unverified**: if the backend DOES retain context per `conversationId`, pairing that
  with full-history sending would *double* the ingested context (more credits, not
  fewer), and Claude Code's `/clear` reuses the same session UUID, so two logical
  conversations could collapse onto one `conversationId`. With the flag off — or with no
  session key (non-Claude-Code clients / missing metadata) even when on — it sends a
  fresh random UUID per request, preserving the original behavior.
- **Tool-level `cache_control` → Kiro `cachePoint`.** When Claude Code marks a tool
  with Anthropic `cache_control`, the proxy emits a sibling `{"cachePoint":{"type":"default"}}`
  entry in the CodeWhisperer `tools` array immediately after that tool, marking a
  caching boundary so the backend can reuse the preceding tool definitions rather
  than re-ingesting them. The total emitted entries (tool entries + cachePoint entries)
  are capped at the tool-count limit (`network.max_tools_per_request`, default 85) so
  cachePoints can't push the array past the backend's ~90-tool / ~260KB body limit.

## Why This Matters

The proxy doesn't modify the semantic content - it translates the protocol. Like a translator converting between English and Japanese, the meaning stays the same but the grammar and structure change completely.

Without this translation:
- Claude Code would receive binary garbage it can't parse
- Request formats would be incompatible
- Streaming wouldn't work
- Tool calls would fail completely
