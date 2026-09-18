# Request Diet Controlled A/B and Hardening Plan — 2026-07-10

## Decision

Keep `full` history and `full` tools as the production control. Canary `recent` history plus `compact` tool descriptions only after the offline replay passes. Do not enable aggressive cache points by default: cache points share the 85-entry tool-array budget and can displace actual tool definitions.

## Offline replay gate

`TestRequestDietControlledABReplay` builds a deterministic request calibrated to the observed Claude Code workload:

- 85 large tool definitions and schemas;
- 14 tool-use/result turns with injected system reminders;
- a current tool result whose matching tool use must remain in history;
- a long system prompt and long message content.

The test compares `control-full`, `candidate-recent-compact`, and `experimental-aggressive-cache`. A candidate passes only when it:

1. preserves current user text and current tool results;
2. keeps strict user/assistant history alternation;
3. retains a prior tool use for every retained tool result;
4. retains the same 85 callable tool definitions as the control;
5. reduces serialized request bytes by at least 20%.

Run with:

```powershell
go test . -run TestRequestDietControlledABReplay -v -count=1
```

This replay measures deterministic translation correctness and wire size. It does not claim to measure backend credits, latency, tool-choice quality, or answer quality.

## Live A/B canary

Use sticky session assignment so every turn in a conversation stays in one arm:

- **Control:** `history_mode: full`, `tool_mode: full`, `aggressive_cache_points: false`.
- **Candidate:** `history_mode: recent`, `history_recent_turns: 6`, `tool_mode: compact`, `tool_compact_max_chars: 320`, `aggressive_cache_points: false`.

Start at 5% of eligible internal sessions for 24 hours, then 25% for 48 hours. Eligible sessions must have tools enabled; stratify results by model, tool count, history length, and whether the request carries a tool result. Never assign by individual request.

Capture per arm:

- backend acceptance and malformed-request rate;
- `TOOL_USE_RESULT_MISMATCH` and other protocol errors;
- request bytes, credits, and first-token/end-to-end latency;
- tool-call success, retry rate, and user cancellation rate;
- sampled task-quality review using blinded control/candidate transcripts.

## Promotion and rollback gates

Promote only if all conditions hold:

- zero confirmed tool ancestry or alternation regressions;
- malformed-request rate no worse than control by more than 0.1 percentage points;
- tool-call success no worse than control by more than 1 percentage point;
- sampled task quality no worse than control by more than 2 percentage points;
- median request bytes improve by at least 20%;
- mean credits improve by at least 10% with a confidence interval excluding zero.

Rollback immediately on any confirmed protocol regression, tool-definition loss, or a 1 percentage-point increase in malformed requests over a rolling 100-request window. Rollback is a config change back to the control profile; retain the failed arm's metrics and anonymized request-shape counters for diagnosis.

## Hardening backlog

1. Give cache points a separate policy from the tool-definition budget, or reserve all tool definitions before inserting cache points.
2. Emit structured counters for source tools, emitted tool definitions, cache points, history entries, protected ancestry entries, and serialized bytes.
3. Add sanitized production-shape fixtures when new malformed-request classes appear; do not store prompts, credentials, or tool-result payloads.
4. Keep the offline replay in the regular Go suite and run the live benchmark before changing defaults.
