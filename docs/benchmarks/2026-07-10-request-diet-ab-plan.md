# Request diet: offline A/B gate and plan (2026-07-10, revised 2026-11-20)

## Decision

Full history and full tools stay the production default (`history_mode: full`, `tool_mode: full`, `aggressive_cache_points: false` in `internal/config/config.go`). The recent-history plus compact-tools profile is a candidate only. Aggressive cache points stay off: each cache point takes a slot in the same `max_tools_per_request` budget as the tool definitions, so with 85 tools it pushes 42 of them out of the request.

## Offline replay gate

`TestRequestDietControlledABReplay` in `request_diet_ab_test.go` runs offline: no network, no token, no recorded traffic. It builds a synthetic request shaped like a Claude Code session:

- 85 tool definitions with long descriptions and schemas;
- 14 assistant tool-use turns, each followed by an injected system message and a user tool result;
- a current message with a tool result whose tool use is in the history;
- a long system prompt.

It translates that request under three profiles:

| Profile | Settings |
|---|---|
| `control-full` | full history, full tools |
| `candidate-recent-compact` | `history_mode: recent`, `history_recent_turns: 6`, `tool_mode: compact`, `tool_compact_max_chars: 320` |
| `experimental-aggressive-cache` | full history, full tools, `aggressive_cache_points: true` |

For every profile it checks that:

1. the current user text and the current tool result survive;
2. history entries alternate user and assistant;
3. every tool result in the history has its tool use earlier in the history, including the current one;
4. no stored reasoning was attached, so the byte count measures the diet alone;
5. the number of tool definitions is the expected one (85, or 43 for aggressive cache points).

The candidate passes only if it also:

6. keeps all 85 tool definitions of the control, each description cut to 320 characters or less;
7. keeps fewer history entries than the control;
8. emits no cache points;
9. serializes at least 20% fewer bytes than the control.

The test restores the global config when it ends. Run it with:

```powershell
& "C:/Program Files/Go/bin/go.exe" test . -run TestRequestDietControlledABReplay -v -count=1
```

On 2026-11-20 (v1.11.0) it logged: control 240,747 bytes and 59 history entries; candidate 132,543 bytes (45% less) and 37 entries; aggressive cache points 141,627 bytes with 43 tool definitions and 42 cache points.

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
