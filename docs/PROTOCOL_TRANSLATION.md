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

## Why This Matters

The proxy doesn't modify the semantic content - it translates the protocol. Like a translator converting between English and Japanese, the meaning stays the same but the grammar and structure change completely.

Without this translation:
- Claude Code would receive binary garbage it can't parse
- Request formats would be incompatible
- Streaming wouldn't work
- Tool calls would fail completely
