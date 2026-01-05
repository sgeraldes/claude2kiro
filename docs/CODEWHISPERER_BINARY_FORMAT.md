# CodeWhisperer Binary Event Stream Format

This document describes the binary event stream format used by AWS CodeWhisperer (Kiro's backend).

## Frame Structure

Each event is wrapped in a binary frame:

```
┌─────────────────┬─────────────────┬─────────────────┬─────────────────┬─────────────────┐
│   Total Length  │  Header Length  │     Headers     │     Payload     │      CRC32      │
│    (4 bytes)    │    (4 bytes)    │   (variable)    │   (variable)    │    (4 bytes)    │
│   Big Endian    │   Big Endian    │                 │      JSON       │                 │
└─────────────────┴─────────────────┴─────────────────┴─────────────────┴─────────────────┘
```

### Length Calculations

- `Total Length` = Header Length + Payload Length + 12 (for the two length fields and CRC)
- `Payload Length` = Total Length - Header Length - 12

## Header Format

Headers are key-value pairs with type information:

```
┌─────────────┬───────────┬────────────┬─────────────────┐
│ Key Length  │    Key    │ Value Type │     Value       │
│  (1 byte)   │ (n bytes) │  (1 byte)  │   (variable)    │
└─────────────┴───────────┴────────────┴─────────────────┘
```

### Common Headers

| Key | Type | Example Value |
|-----|------|---------------|
| `:event-type` | string (7) | `toolUseEvent`, `assistantResponseEvent` |
| `:content-type` | string (7) | `application/json` |
| `:message-type` | string (7) | `event` |

### Value Types

| Type Code | Meaning |
|-----------|---------|
| 7 | String (followed by 2-byte length, then string bytes) |

## Event Types

### assistantResponseEvent

Text content from the assistant.

```json
{
  "content": "Hello, how can I help?",
  "stop": false
}
```

Final text chunk:
```json
{
  "content": "",
  "stop": true
}
```

### toolUseEvent

Tool/function call.

**Start of tool (no input yet):**
```json
{
  "name": "Bash",
  "toolUseId": "tooluse_XAKIoZHyQuav1JhfbmjHkQ"
}
```

**Input chunks (streamed):**
```json
{
  "input": "{\"command\":",
  "name": "Bash",
  "toolUseId": "tooluse_XAKIoZHyQuav1JhfbmjHkQ"
}
```

**End of tool:**
```json
{
  "name": "Bash",
  "stop": true,
  "toolUseId": "tooluse_XAKIoZHyQuav1JhfbmjHkQ"
}
```

### meteringEvent

Usage/billing information.

```json
{
  "unit": "credit",
  "unitPlural": "credits",
  "usage": 0.798734461227195
}
```

### contextUsageEvent

Context window usage.

```json
{
  "contextUsagePercentage": 23.24250030517578
}
```

## Parsing Algorithm

```go
func ParseEvents(resp []byte) []SSEEvent {
    r := bytes.NewReader(resp)

    for r.Len() >= 12 {
        // 1. Read total length (4 bytes, big endian)
        var totalLen uint32
        binary.Read(r, binary.BigEndian, &totalLen)

        // 2. Read header length (4 bytes, big endian)
        var headerLen uint32
        binary.Read(r, binary.BigEndian, &headerLen)

        // 3. Validate frame
        if int(totalLen) > r.Len()+8 {
            break  // Invalid frame
        }

        // 4. Skip headers (we only care about payload)
        header := make([]byte, headerLen)
        io.ReadFull(r, header)

        // 5. Read payload
        payloadLen := int(totalLen) - int(headerLen) - 12
        payload := make([]byte, payloadLen)
        io.ReadFull(r, payload)

        // 6. Skip CRC32 (4 bytes)
        r.Seek(4, io.SeekCurrent)

        // 7. Parse JSON payload
        // Note: Some payloads have "vent" prefix to strip
        payloadStr := strings.TrimPrefix(string(payload), "vent")
        json.Unmarshal([]byte(payloadStr), &evt)
    }
}
```

## Hex Dump Example

```
00000000: 0000 009e 0000 0052 8e71 76c0 0b3a 6576  .......R.qv..:ev
          ^^^^^^^^ ^^^^^^^^
          totalLen headerLen
          = 158    = 82

00000010: 656e 742d 7479 7065 0700 0c74 6f6f 6c55  ent-type...toolU
          ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
          header: event-type = "toolUseEvent"

00000050: 652d 7479 7065 0700 0565 7665 6e74 7b22  e-type...event{"
                                          ^^^^^^
                                          payload starts

00000060: 6e61 6d65 223a 2254 6173 6b22 2c22 746f  name":"Task","to
00000070: 6f6c 5573 6549 6422 3a22 746f 6f6c 7573  olUseId":"toolus
00000080: 655f 5841 4b49 6f5a 4879 5175 6176 314a  e_XAKIoZHyQuav1J
00000090: 6866 626d 6a48 6b51 227d 7c25 e72e       hfbmjHkQ"}|%..
                              ^^^^^^^^^^^
                              CRC32
```

Payload: `{"name":"Task","toolUseId":"tooluse_XAKIoZHyQuav1JhfbmjHkQ"}`

## Multi-Tool Response Structure

When Claude spawns multiple agents/tools, the response contains interleaved events:

```
Frame 1: toolUseEvent - Tool 1 start
Frame 2: toolUseEvent - Tool 1 input chunk
Frame 3: toolUseEvent - Tool 1 input chunk
...
Frame N: toolUseEvent - Tool 1 stop
Frame N+1: toolUseEvent - Tool 2 start
Frame N+2: toolUseEvent - Tool 2 input chunk
...
Frame M: toolUseEvent - Tool 2 stop
Frame M+1: meteringEvent - Usage info
Frame M+2: contextUsageEvent - Context info
Frame M+3: toolUseEvent - Tool 3 start
...
```

Note: Events may not arrive in strict order. The parser must track each tool by its `toolUseId`.

## Debugging Tips

1. **Save raw responses:**
   ```go
   os.WriteFile("response.bin", respBody, 0644)
   ```

2. **Hex dump for analysis:**
   ```bash
   xxd response.bin | head -100
   ```

3. **Extract strings:**
   ```bash
   strings response.bin | grep -E "(name|toolUseId|content)"
   ```

4. **Find stop events:**
   ```bash
   xxd response.bin | grep -i stop
   ```
