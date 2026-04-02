---
name: kiro-bridge-quality-gate
enabled: true
event: stop
action: block
---

# Kiro Bridge Quality Gate

Before completing any phase of the Kiro Bridge test-fix cycle, you MUST verify ALL of the following.
If any check fails, DO NOT stop - fix the issue and re-run.

## Mandatory Checks

### 1. Go Unit Tests Pass
Run: `"C:/Program Files/Go/bin/go.exe" test ./... 2>&1`
- ALL tests must pass
- Zero failures allowed

### 2. Kiro Bridge Test Suite
Run the test suite for the current phase:
- `./claude2kiro.exe test "Say HELLO"` must return text
- Non-streaming: `curl -s POST /v1/messages stream:false` must return content
- Streaming: `curl -s POST /v1/messages stream:true` must return valid SSE events
- Event order must be: message_start, ping, content_block_start, content_block_delta*, content_block_stop, message_delta, message_stop

### 3. Code Review / Simplify
Evidence of ONE of these in the conversation:
- `/code-review` skill was invoked
- `/simplify` skill was invoked
- Agent with subagent_type "code-reviewer" was used
- Manual review comments were provided

### 4. Regression Check
ALL previously passing tests must still pass:
- Model mapping: all 11 models respond (claude2kiro test "hi" <model>)
- Streaming SSE order is correct
- Non-streaming returns content (not empty [])
- `claude2kiro test` command works

## How to Verify

Before claiming a phase is complete, run this checklist:

```bash
# 1. Unit tests
"C:/Program Files/Go/bin/go.exe" test ./...

# 2. Quick smoke test
./claude2kiro.exe test "Say PASS"

# 3. SSE order check (streaming)
./claude2kiro.exe server 19999 &
curl -s -N POST http://localhost:19999/v1/messages \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-sonnet-4-6","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"Say OK"}]}' | grep "^event:" 
kill %1

# 4. Non-streaming check
curl -s POST http://localhost:19999/v1/messages \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-sonnet-4-6","max_tokens":50,"stream":false,"messages":[{"role":"user","content":"Say OK"}]}' | python -c "import sys,json; d=json.load(sys.stdin); assert len(d['content'])>0, 'EMPTY CONTENT'"
```

If ANY of these fail, you MUST fix the issue before stopping.
