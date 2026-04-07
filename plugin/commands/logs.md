---
description: View recent Kiro proxy logs
argument-hint: "[count|errors|today]"
allowed-tools:
  - Bash
---

Show recent proxy log entries from `~/.claude2kiro/logs/`.

Behavior based on argument:
- No argument or "today": show last 20 entries from today's log
- A number (e.g., "50"): show last N entries
- "errors": show only error entries
- "all": show summary of all log files with sizes and dates

Commands:
```bash
# Today's log
ls -la ~/.claude2kiro/logs/$(date +%Y-%m-%d).log 2>/dev/null

# Last entries
tail -20 ~/.claude2kiro/logs/$(date +%Y-%m-%d).log 2>/dev/null || echo "No logs for today"
```

Format the output clearly:
- Highlight errors in the output
- Show request/response pairs together
- Summarize: total requests, errors, average response time if visible
