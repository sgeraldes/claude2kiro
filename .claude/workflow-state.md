---
workflow: phased-dev
workflow_status: in_progress
current_phase: 2
total_phases: 4
started: 2026-04-02
last_updated: 2026-04-02T05:05:00
tool_uses_count: 80
---

# Kiro Bridge - Phased Development Workflow

## Project: Fix claude2kiro proxy for full Claude Code compatibility

## Phases

### Phase 1: Fix Core Protocol - COMPLETE
**Goal:** Make `claude -p "Say HELLO"` work through the proxy
**Result:** PASS - `claude -p "Say exactly: KIRO_BRIDGE_WORKS"` returns correct output
**Fixes applied:**
- cleanToolSchema(): Remove $schema, title, $defs from tool input schemas recursively
- maxTools = 85: Kiro rejects requests with 95+ tools (~260KB limit)
- Debug logging: Save CW request/response to temp dir for analysis
- Model IDs: All 11 Kiro models discovered via ListAvailableModels API
- SSE event ordering: content_block_stop before message_delta
- Non-streaming: Added Accept header + simplified event extraction
- CLAUDE_CODE_DISABLE_THINKING=1: Kiro doesn't return thinking blocks

### Phase 2: Streaming & Non-Streaming Compliance - IN PROGRESS
**Goal:** All SSE events correct, non-streaming returns content
**Status:** Basic streaming and non-streaming work. Need to verify tool_use events.

### Phase 3: Conversation Features (History, Tools, System Prompts)
**Goal:** Multi-turn, tool use, system prompts work
**Known issues:**
- Tool results in history never populated (getMessageToolUses exists but unused)
- ToolUses array always empty in assistant history messages

### Phase 4: Full Integration & All Models
**Goal:** Claude Code interactive mode works, all 11 models respond
**Model test results:**
- claude-sonnet-4.6: PASS
- claude-haiku-4.5: PASS
- claude-opus-4.6: PASS
- deepseek-3.2: TEMP_UNAVAILABLE (transient, model itself is down)
- qwen3-coder-next: PASS
- auto: PASS

## Quality Gates (per phase - ALL must be checked before workflow_status: complete)
- [x] /code-review - Code review launched (Phase 1)
- [ ] /simplify - Code clarity, consistency, dead code removal
- [ ] /delivery-gate - All tests pass, build clean
- [x] Regression - Go tests pass, streaming/non-streaming verified

## Completed Stages
- [x] Phase 1: Discovery - Captured 381KB request, identified tool schema + count as root cause
- [x] Phase 1: Fix - cleanToolSchema + maxTools + SSE order + non-streaming handler
- [x] Phase 1: Verify - claude -p returns "KIRO_BRIDGE_WORKS" through proxy
- [x] Phase 1: Code Review - Launched

## Key Decisions Made
- CLAUDE_CODE_DISABLE_THINKING=1 (Kiro doesn't return thinking blocks)
- ANTHROPIC_AUTH_TOKEN instead of API_KEY (avoids auth conflict)
- Model IDs via ListAvailableModels API (new format: claude-sonnet-4.6)
- Non-streaming needs Accept: text/event-stream header
- Tool limit: 85 max (Kiro rejects 95+, ~260KB body limit)
- Tool schemas: Strip $schema, title, $defs, $id, $comment recursively

## Next Steps
1. Phase 2: Verify SSE event order with tool_use responses
2. Phase 3: Fix tool result/use history for multi-turn conversations
3. Phase 4: Test claude2kiro run (interactive mode) end-to-end
4. Run /simplify and /delivery-gate
