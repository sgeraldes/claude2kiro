# Request diet: offline A/B gate and plan (2026-07-10, revised 2026-11-20)

## Decision

Full history and full tools stay the production default (`history_mode: full`, `tool_mode: full`, `aggressive_cache_points: false` in `internal/config/config.go`). The recent-history plus compact-tools profile is a candidate only. Aggressive cache points stay off: each cache point takes a slot in the same `max_tools_per_request` budget as the tool definitions, so with 85 tools it pushes 42 of them out of the request.

## Offline replay gate

Two tests in `request_diet_ab_test.go` run offline: no network, no token, no recorded traffic. They build a synthetic request shaped like a Claude Code session:

- 85 tool definitions with long descriptions and schemas;
- 14 assistant tool-use turns, each followed by an injected system message and the user's tool result;
- a current message that carries the result of the latest tool use;
- a long system prompt.

`TestRequestDietControlledABReplay` is the A/B gate.

It translates that request under three profiles:

| Profile | Settings |
|---|---|
| `control-full` | full history, full tools |
| `candidate-recent-compact` | `history_mode: recent`, `history_recent_turns: 6`, `tool_mode: compact`, `tool_compact_max_chars: 320` |
| `experimental-aggressive-cache` | full history, full tools, `aggressive_cache_points: true` |

For every profile it checks that:

1. the current user text survives, and the current tool result keeps its id, its `success` status and its content;
2. history entries alternate user and assistant;
3. every tool result in the history has its tool use earlier in the history, including the current one;
4. no stored reasoning was attached, so the byte count measures the diet alone;
5. the number of tool definitions is the expected one (85, or 43 for aggressive cache points).

The candidate passes only if it also:

6. keeps the control's 85 tools with the same names and identical schemas, each description cut to 320 characters or less;
7. keeps fewer history entries than the control, and every tool use the control has;
8. emits no cache points;
9. serializes at least 20% fewer bytes than the control.

`TestRequestDietReplayKeepsOlderCurrentToolUse` covers the protection the window cannot give. The current result answers turn 02, far outside the six recent turns, and turn 02's own user message carries no result. The test checks that the window and the historical tool results alone would drop turn 02, and that the candidate keeps it anyway.

Both tests restore the global config when they end. Run them with:

```powershell
& "C:/Program Files/Go/bin/go.exe" test . -run TestRequestDiet -v -count=1
```

On 2026-11-20 (v1.11.0) the gate logged: control 239,734 bytes and 57 history entries; candidate 131,530 bytes (45.1% less) and 35 entries; aggressive cache points 140,614 bytes with 43 tool definitions and 42 cache points.

`recent` history saves less than its name suggests on a tool-heavy session. A kept user message that carries a tool result needs its assistant turn, and that turn needs the user message before it, which answers the turn before. In the fixture every user turn answers the previous one, so the candidate keeps all 14 tool turns. It drops only the injected system messages outside the window and the filler assistant turns the translator puts after each of them. Measured on the same fixture, compact tool descriptions alone save 44.1% and recent history alone 1.0%.

The replay proves translation correctness and wire size only. It says nothing about credits, latency, tool choice or answer quality.

## Live comparison

Not run yet. The proxy has no way to split sessions between two arms: it is one user's local process and reads one config. A comparison today is two runs of the same task, each in its own copy of the inputs, one with the control settings and one with the candidate settings, as described in `docs/benchmarks/2026-11-19-task-time-and-model-choice.md`. Each request logs a `Request ...` line with `reqBytes`, `historyLen`, `tools` (definitions plus cache points), `historyMode` and `toolMode`, and each response logs a `Kiro metering` line with its credits.

Since July the request shape changed: v1.10.0 stopped sending tool results twice and started returning the model's reasoning in the history. Byte savings measured before v1.10.0 do not carry over.

Change the default only if the candidate run finishes the task with:

- no `TOOL_USE_RESULT_MISMATCH` or malformed-request error;
- the same tool calls succeeding;
- output of the same quality, judged side by side;
- at least 10% fewer credits.

## Open work

1. Give cache points their own budget, or place every tool definition before any cache point. Until then `experimental-aggressive-cache` expects 43 definitions; when this is fixed, that expectation changes to 85.
2. The request log counts cache points inside `tools`; it does not log tool definitions and cache points separately, nor how many history entries were kept to protect tool-use pairs.
3. When a new malformed-request class appears, add a synthetic fixture for it. Never store prompts, credentials or tool-result content.
