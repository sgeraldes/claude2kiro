# Credit Reduction Roadmap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce Claude2Kiro credit/token burn while keeping Claude Code functional.

**Current status:** Protocol parity, experimental request-diet controls, metering logs, and a repeatable benchmark harness are implemented with conservative defaults. Local 1.4.5 validation now passes both no-tool and tool-using benchmark matrices.

**Architecture:** Separate protocol correctness from request shrinking. Protocol parity should be low risk and default-safe; request shrinking should stay behind explicit experimental settings until benchmark data proves it preserves behavior and saves credits.

**Tech Stack:** Go, Claude2Kiro request translation, existing TUI settings, existing Go tests, local Claude Code headless credit benchmarks.

---

## Phase 1: Protocol Parity

Adopt safe pieces from kirocc:

- [x] Add current Kiro runtime endpoint helper: `runtime.<region>.kiro.dev`.
- [x] Add native effort support: `additionalModelRequestFields.output_config.effort`.
- [x] Add `envState` from Claude Code's `<env>` system block.
- [ ] Update request headers/User-Agent shape closer to current Kiro CLI after local capture validates the exact runtime endpoint requirements.
- [x] Parse/log Kiro `meteringEvent` credits when present.

Expected savings: small or none. Main benefit is correctness and cleaner measurements.

## Phase 2: Benchmark Harness

Make the credit test repeatable:

- [x] Run the same no-tool prompt suite across modes.
- [x] Record request bytes, tool count, history length, token estimates, Kiro credits.
- [x] Compare current full payload, cachePoint modes, history elision modes, and tool compact/no-tool modes.
- [x] Run the same matrix on a tool-using prompt suite before release.

Expected value: lets us stop guessing.

## Phase 3: Experimental Request Diet

Add opt-in settings, not defaults yet:

- [x] `history_mode=full`: current behavior.
- [x] `history_mode=recent`: send only last N turns.
- [x] `history_mode=current_only`: stable `conversationId`, only current turn.
- [x] `tool_mode=full`: current behavior.
- [x] `tool_mode=compact`: trim long tool descriptions.
- [x] `tool_mode=none_text`: remove tools and convert prior tool calls/results to text.
- [x] Preserve required tool-use/tool-result history pairs when trimming history.

Expected savings: potentially large, but riskier.

## Phase 4: CachePoint Experiment

Keep current `cache_control -> cachePoint` behavior.

Add opt-in aggressive cachePoint mode:

- [x] Inject a cachePoint after tool definitions even when Claude Code did not send `cache_control`.
- [x] Measure simple no-tool prompt behavior.
- [x] Measure tool-using workflow behavior.

Expected savings: unknown; low risk if backend accepts it.

## Adopt

From kirocc:

- [x] Protocol parity.
- [x] Runtime endpoint helper.
- [x] Native effort.
- [x] `envState`.
- [x] Credit metering logs.

From kiro-gateway:

- [ ] Token estimator.
- [ ] Payload size guard.
- [x] History trimming.
- [x] Tool-content-to-text conversion for no-tools mode.
- [x] Repeatable local benchmark harness.

Avoid:

- Replacing our stable session ID with kiro-gateway's message-hash conversation ID.
- Making no-tools mode default.

## Recommended Defaults

Now:

- Stable conversation ID default ON.
- Full tools and full history remain default.

Later, only after benchmark proof:

- Maybe enable compact tool descriptions.
- Maybe enable aggressive cachePoint.
- Do not default to history elision unless marker-memory tests pass reliably and credits drop materially.
