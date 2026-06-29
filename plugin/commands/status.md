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

2. Resolve the proxy URL (prefer `ANTHROPIC_BASE_URL`, else the running proxy's
   advertised port, else `localhost:8080`) and check health + credits:
```bash
base="${ANTHROPIC_BASE_URL}"
if [ -z "$base" ]; then
  port="$(tr -d '[:space:]' < "$HOME/.claude2kiro/proxy.port" 2>/dev/null)"
  [ -n "$port" ] && base="http://127.0.0.1:$port" || base="http://localhost:8080"
fi
echo "Proxy URL: $base"
curl -s --max-time 6 "$base/health"  2>/dev/null || echo "Proxy not reachable"
echo
curl -s --max-time 10 "$base/credits" 2>/dev/null || echo "Credits endpoint not available"
```

3. Show recent log activity:
```bash
tail -5 ~/.claude2kiro/logs/$(date +%Y-%m-%d).log 2>/dev/null || echo "No logs today"
```

Present a formatted summary with:
- Proxy status (running/stopped)
- Current config values (port, timeouts, streaming delay, skip_permissions)
- Kiro credits (used/remaining/plan/days until reset)
- Recent log entries
