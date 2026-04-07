# Anthropic SSE Event Format

This document describes the Server-Sent Events (SSE) format that Claude Code expects from the Anthropic Messages API.

## Event Types

### 1. message_start

Sent at the beginning of every response.

```json
{
  "type": "message_start",
  "message": {
    "id": "msg_01234567890",
    "type": "message",
    "role": "assistant",
    "content": [],
    "model": "claude-sonnet-4-20250514",
    "stop_reason": null,
    "stop_sequence": null,
    "usage": {
      "input_tokens": 100,
      "output_tokens": 1
    }
  }
}
```

### 2. content_block_start

Signals the start of a content block. Each block has a unique `index`.

**For text content:**
```json
{
  "type": "content_block_start",
  "index": 0,
  "content_block": {
    "type": "text",
    "text": ""
  }
}
```

**For tool use:**
```json
{
  "type": "content_block_start",
  "index": 1,
  "content_block": {
    "type": "tool_use",
    "id": "tooluse_abc123",
    "name": "Bash",
    "input": {}
  }
}
```

### 3. content_block_delta

Streams content for a block. Must reference the correct `index`.

**For text:**
```json
{
  "type": "content_block_delta",
  "index": 0,
  "delta": {
    "type": "text_delta",
    "text": "Hello, "
  }
}
```

**For tool input JSON:**
```json
{
  "type": "content_block_delta",
  "index": 1,
  "delta": {
    "type": "input_json_delta",
    "partial_json": "{\"command\":"
  }
}
```

### 4. content_block_stop

Signals the end of a content block.

```json
{
  "type": "content_block_stop",
  "index": 0
}
```

### 5. message_delta

Sent once at the end with the stop reason.

**For text completion:**
```json
{
  "type": "message_delta",
  "delta": {
    "stop_reason": "end_turn",
    "stop_sequence": null
  },
  "usage": {
    "output_tokens": 150
  }
}
```

**For tool use:**
```json
{
  "type": "message_delta",
  "delta": {
    "stop_reason": "tool_use",
    "stop_sequence": null
  },
  "usage": {
    "output_tokens": 200
  }
}
```

### 6. message_stop

Final event signaling the message is complete.

```json
{
  "type": "message_stop"
}
```

### 7. ping

Keep-alive event.

```json
{
  "type": "ping"
}
```

## Index Rules

**Critical:** Each content block must have a unique index.

| Content Type | Index |
|-------------|-------|
| Text | 0 |
| First tool | 1 (or 0 if no text) |
| Second tool | 2 (or 1 if no text) |
| Third tool | 3 (or 2 if no text) |

### Example: Text-only Response
```
index 0: text content
```

### Example: Tool-only Response (single tool)
```
index 0: tool_use
```

### Example: Tool-only Response (multiple tools)
```
index 0: tool_use (Bash)
index 1: tool_use (Read)
index 2: tool_use (Write)
```

### Example: Mixed Response (text + tools)
```
index 0: text
index 1: tool_use (Task)
index 2: tool_use (Task)
```

## Complete Streaming Sequence

### Text Response
```
event: message_start
event: content_block_start (index=0, type=text)
event: content_block_delta (index=0, text chunk 1)
event: content_block_delta (index=0, text chunk 2)
event: content_block_delta (index=0, text chunk 3)
event: content_block_stop (index=0)
event: message_delta (stop_reason=end_turn)
event: message_stop
```

### Single Tool Response
```
event: message_start
event: content_block_start (index=0, type=tool_use, name=Bash)
event: content_block_delta (index=0, partial_json chunk 1)
event: content_block_delta (index=0, partial_json chunk 2)
event: content_block_stop (index=0)
event: message_delta (stop_reason=tool_use)
event: message_stop
```

### Multi-Tool Response (4 parallel agents)
```
event: message_start
event: content_block_start (index=0, type=tool_use, name=Task, id=tool_1)
event: content_block_delta (index=0, partial_json chunks...)
event: content_block_stop (index=0)
event: content_block_start (index=1, type=tool_use, name=Task, id=tool_2)
event: content_block_delta (index=1, partial_json chunks...)
event: content_block_stop (index=1)
event: content_block_start (index=2, type=tool_use, name=Task, id=tool_3)
event: content_block_delta (index=2, partial_json chunks...)
event: content_block_stop (index=2)
event: content_block_start (index=3, type=tool_use, name=Task, id=tool_4)
event: content_block_delta (index=3, partial_json chunks...)
event: content_block_stop (index=3)
event: message_delta (stop_reason=tool_use)  ← ONLY ONE at the end
event: message_stop
```

## Common Mistakes

### Wrong: Duplicate indices
```
content_block_start index=1 (tool 1)
content_block_start index=1 (tool 2)  ← WRONG: same index
```

### Wrong: Multiple message_delta
```
content_block_stop (tool 1)
message_delta  ← WRONG: too early
content_block_stop (tool 2)
message_delta  ← WRONG: duplicate
```

### Wrong: Index conflict with text
```
content_block_start index=0 (text)
content_block_start index=0 (tool)  ← WRONG: conflicts with text
```

## Wire Format

Each SSE event is formatted as:
```
event: {event_type}
data: {json_payload}

```

Note the blank line after each event. Multiple events are separated by blank lines.
