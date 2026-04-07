---
description: Show current Kiro proxy status, config, and credits
allowed-tools:
  - Bash
---

Check the Kiro proxy status by reading its config and querying its endpoints. Run these commands and present a clear summary:

1. Read the proxy config:
```bash
cat ~/.claude2kiro/config.yaml 2>/dev/null || echo "No config file (using defaults)"
```

2. Check proxy health (use the ANTHROPIC_BASE_URL if set, otherwise localhost:8080):
```bash
curl -s ${ANTHROPIC_BASE_URL:-http://localhost:8080}/health 2>/dev/null || echo "Proxy not reachable"
```

3. Check Kiro credits:
```bash
curl -s ${ANTHROPIC_BASE_URL:-http://localhost:8080}/credits 2>/dev/null || echo "Credits endpoint not available"
```

4. Show recent log activity:
```bash
tail -5 ~/.claude2kiro/logs/$(date +%Y-%m-%d).log 2>/dev/null || echo "No logs today"
```

Present a formatted summary with:
- Proxy status (running/stopped)
- Current config values (port, timeouts, streaming delay, skip_permissions)
- Kiro credits (used/remaining/plan/days until reset)
- Recent log entries
