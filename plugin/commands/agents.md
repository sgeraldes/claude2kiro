---
description: Show per-subagent stats (turns, ingested tokens, throughput) for the current session
allowed-tools:
  - Bash
---

Show per-subagent activity for the current Claude Code session. Claude Code puts
no subagent id on the wire (all subagents share one session id), so the proxy
can't attribute traffic to a named agent — but Claude Code persists each
subagent's transcript locally, and `claude2kiro agents` reads those to produce
per-named-agent stats: turns, total ingested tokens (the credit/load driver),
peak context, duration, and ingestion rate.

1. Print the per-agent table for the most recent session:
```bash
claude2kiro agents
```

2. To target a specific session, pass its id (the last 8 chars are shown in the
   proxy dashboard's "Session Info"):
```bash
claude2kiro agents <session-uuid>
```

3. The proxy also serves this as JSON (for the live web dashboard at
   `/dashboard`, which now has a Subagents card):
```bash
base="${ANTHROPIC_BASE_URL:-http://localhost:8080}"
curl -s --max-time 6 "$base/agents" 2>/dev/null || echo "Proxy not reachable"
```

Present a clear summary:
- One row per named subagent (e.g. `dev-licenses`, `dev-activity`), with turns,
  ingested tokens, peak context, duration, and ingestion rate.
- Call out the heaviest agents by ingested tokens (that's where credits go).

Note: Kiro does not report output tokens, so `output_tokens` reads 0 and
throughput is ingestion-based. This is expected, not a bug.
