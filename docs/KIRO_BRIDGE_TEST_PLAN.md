# Kiro Bridge - Test Plan & Protocol Compliance Suite

**Codename:** `kiro-bridge` (el puente entre Claude Code y Kiro)

## Overview

This is a comprehensive test plan for the claude2kiro proxy that translates
Anthropic API requests to Kiro/CodeWhisperer format and back. Tests are
organized in phases from basic connectivity to full Claude Code integration.

## Test Architecture

```
claude2kiro test-suite [phase]     # Run all tests or specific phase
claude2kiro test [msg] [model]     # Quick single-message test (existing)
```

Tests use the proxy's headless server + curl to send requests and validate
responses, with no external dependencies. Each test is repeatable and
regression-safe.

## Phase 1: Basic Connectivity & Auth

| # | Test | Input | Expected | Status |
|---|------|-------|----------|--------|
| 1.1 | Token exists | `getToken()` | Token with accessToken, expiresAt | |
| 1.2 | Token refresh | `TryRefreshToken()` | New token, valid expiry | |
| 1.3 | Health endpoint | `GET /health` | `{"status":"ok"}` 200 | |
| 1.4 | Credits endpoint | `GET /credits` | JSON with used/limit/plan | |
| 1.5 | Invalid method | `GET /v1/messages` | 405 Method Not Allowed | |
| 1.6 | Invalid body | `POST /v1/messages {}` | 400 Missing model | |
| 1.7 | ListAvailableModels | Direct API call | All 11 models returned | |

## Phase 2: Model Mapping

| # | Test | Input model | Expected Kiro ID | Status |
|---|------|-------------|------------------|--------|
| 2.1 | Sonnet 4.6 (CC default) | `claude-sonnet-4-6` | `claude-sonnet-4.6` | |
| 2.2 | Sonnet 4.6 with dot | `claude-sonnet-4.6` | `claude-sonnet-4.6` | |
| 2.3 | Opus 4.6 | `claude-opus-4-6` | `claude-opus-4.6` | |
| 2.4 | Haiku 4.5 | `claude-haiku-4-5-20251001` | `claude-haiku-4.5` | |
| 2.5 | Sonnet 4.5 (legacy) | `claude-sonnet-4-5-20250929` | `claude-sonnet-4.5` | |
| 2.6 | Opus 4.5 | `claude-opus-4-5-20251101` | `claude-opus-4.5` | |
| 2.7 | Sonnet 3.7 (very old) | `claude-3-7-sonnet-20250219` | `claude-sonnet-4.5` | |
| 2.8 | DeepSeek | `deepseek-3.2` | `deepseek-3.2` | |
| 2.9 | Qwen3 | `qwen3-coder-next` | `qwen3-coder-next` | |
| 2.10 | MiniMax | `minimax-m2.5` | `minimax-m2.5` | |
| 2.11 | Auto | `auto` | `auto` | |
| 2.12 | Unknown future model | `claude-sonnet-5-0` | `claude-sonnet-4.6` (fallback) | |

## Phase 3: Simple Text Responses (Streaming)

| # | Test | Request | Expected | Status |
|---|------|---------|----------|--------|
| 3.1 | Basic text | `stream:true, "Say HELLO"` | SSE with text "HELLO" | |
| 3.2 | Event order | Same as 3.1 | message_start → ping → content_block_start → content_block_delta* → content_block_stop → message_delta → message_stop | |
| 3.3 | Multi-word | `"List 3 colors"` | Multiple content_block_delta events | |
| 3.4 | Long response | `"Write a 200-word paragraph"` | Many deltas, no truncation | |
| 3.5 | Stop reason | Any | message_delta.delta.stop_reason = "end_turn" | |
| 3.6 | Message ID | Any | message_start.message.id starts with "msg_" | |
| 3.7 | Model echo | Send as "claude-sonnet-4-6" | message_start.message.model = "claude-sonnet-4-6" | |

## Phase 4: Simple Text Responses (Non-Streaming)

| # | Test | Request | Expected | Status |
|---|------|---------|----------|--------|
| 4.1 | Basic non-stream | `stream:false, "Say HELLO"` | JSON with content[0].text = "HELLO" | |
| 4.2 | Content array | Same | content is array with type "text" | |
| 4.3 | Stop reason | Same | stop_reason = "end_turn" | |
| 4.4 | Usage tokens | Same | usage.input_tokens > 0 | |

## Phase 5: System Prompt Handling

| # | Test | Request | Expected | Status |
|---|------|---------|----------|--------|
| 5.1 | String system | `system: "You are helpful"` | Request succeeds, system in history | |
| 5.2 | Array system | `system: [{type:"text", text:"..."}]` | Request succeeds | |
| 5.3 | System with cache_control | `system: [{text:"...", cache_control:{type:"ephemeral"}}]` | Cache control stripped, request succeeds | |
| 5.4 | Long system (>10KB) | System prompt > 10KB | Request succeeds, no truncation | |
| 5.5 | Multiple system blocks | Array of 3+ text blocks | All included in history | |

## Phase 6: Conversation History

| # | Test | Request | Expected | Status |
|---|------|---------|----------|--------|
| 6.1 | Single turn | 1 user message | Works | |
| 6.2 | Multi-turn | user→assistant→user | History populated correctly | |
| 6.3 | 5-turn conversation | 5 exchanges | All in history | |
| 6.4 | Content as string | `content: "hello"` | Works (string form) | |
| 6.5 | Content as array | `content: [{type:"text",text:"hello"}]` | Works (block form) | |

## Phase 7: Tool Definitions

| # | Test | Request | Expected | Status |
|---|------|---------|----------|--------|
| 7.1 | Single tool | 1 tool definition | Tool in request context | |
| 7.2 | Multiple tools | 5+ tools | All tools included | |
| 7.3 | Tool with complex schema | Nested properties, arrays | Schema preserved | |
| 7.4 | Long description | >10KB description | Truncated to limit | |
| 7.5 | Tool with cache_control | cache_control field | Stripped, no error | |
| 7.6 | 50+ tools | Many tools (like CC sends) | No error, all included | |

## Phase 8: Tool Use Flow (Response → Request)

| # | Test | Request | Expected | Status |
|---|------|---------|----------|--------|
| 8.1 | Tool use in response | Model calls a tool | tool_use content block in SSE | |
| 8.2 | Tool result in request | tool_result message | Passed to Kiro in context | |
| 8.3 | Multi-tool response | Model calls 2+ tools | Multiple tool_use blocks | |
| 8.4 | Tool use → result → response | Full roundtrip | Model sees tool output | |

## Phase 9: Claude Code Integration (`claude -p`)

| # | Test | Command | Expected | Status |
|---|------|---------|----------|--------|
| 9.1 | Simple prompt | `claude -p "Say HELLO"` | Prints "HELLO" | |
| 9.2 | Code generation | `claude -p "Write hello.py"` | Returns Python code | |
| 9.3 | With model flag | `claude -p "hi" --model haiku` | Uses Haiku model | |
| 9.4 | Large system prompt | Default CC system prompt | No "Improperly formed" error | |
| 9.5 | With tools | CC sends full tool list | Request succeeds | |

## Phase 10: Error Handling

| # | Test | Trigger | Expected | Status |
|---|------|---------|----------|--------|
| 10.1 | Invalid model | `model: "nonexistent"` | Anthropic error format: `{"type":"error","error":{...}}` | |
| 10.2 | Empty messages | `messages: []` | 400 error | |
| 10.3 | Token expired | Expired token | 403 → refresh → retry | |
| 10.4 | Backend 400 | Malformed request | Error forwarded to client | |
| 10.5 | Backend timeout | Very long request | Timeout handling | |

## Phase 11: All Models Respond

| # | Test | Model | Expected | Status |
|---|------|-------|----------|--------|
| 11.1 | Claude Opus 4.6 | `claude-opus-4.6` | Text response | |
| 11.2 | Claude Sonnet 4.6 | `claude-sonnet-4.6` | Text response | |
| 11.3 | Claude Sonnet 4.5 | `claude-sonnet-4.5` | Text response | |
| 11.4 | Claude Sonnet 4 | `claude-sonnet-4` | Text response | |
| 11.5 | Claude Haiku 4.5 | `claude-haiku-4.5` | Text response | |
| 11.6 | DeepSeek 3.2 | `deepseek-3.2` | Text response | |
| 11.7 | MiniMax M2.5 | `minimax-m2.5` | Text response | |
| 11.8 | MiniMax M2.1 | `minimax-m2.1` | Text response | |
| 11.9 | Qwen3 Coder Next | `qwen3-coder-next` | Text response | |
| 11.10 | Auto | `auto` | Text response | |

## Test Runner Design

Each test is a Go function that:
1. Starts the headless proxy server
2. Sends a specific HTTP request
3. Validates the response (status, headers, body structure, content)
4. Returns pass/fail with details

Tests are organized by phase and can run individually or as a suite.
Failures are reported with the exact request/response for debugging.

## Success Criteria

- All Phase 1-4 tests pass: Basic proxy functionality
- All Phase 5-7 tests pass: Request translation complete
- All Phase 8 tests pass: Tool use works
- All Phase 9 tests pass: Claude Code integration works
- All Phase 10 tests pass: Error handling correct
- All Phase 11 tests pass: All models verified

## Known Issues to Fix

1. **"Improperly formed request"** for Claude Code's 305KB requests (Phase 9)
   - Root cause: `buildCodeWhispererRequest` doesn't handle all fields CC sends
   - Fields to handle: cache_control, thinking, metadata, output_config, betas
   
2. **Thinking disabled** via CLAUDE_CODE_DISABLE_THINKING=1
   - Kiro doesn't return thinking blocks
   - Need to verify CC works correctly without thinking
   
3. **Tool results dropped** from history (Phase 8)
   - `getMessageToolUses()` exists but never called
   - `ToolUses` array always empty
   
4. **output_tokens always 0** in SSE responses
