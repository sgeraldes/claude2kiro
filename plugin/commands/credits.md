---
description: Show Kiro subscription credits usage
allowed-tools:
  - Bash
---

Check Kiro credit usage by querying the proxy's credits endpoint. Resolve the
proxy URL robustly: prefer `ANTHROPIC_BASE_URL`, fall back to the port written
by the running proxy (`~/.claude2kiro/proxy.port`), then `localhost:8080`:

```bash
base="${ANTHROPIC_BASE_URL}"
if [ -z "$base" ]; then
  port="$(tr -d '[:space:]' < "$HOME/.claude2kiro/proxy.port" 2>/dev/null)"
  [ -n "$port" ] && base="http://127.0.0.1:$port" || base="http://localhost:8080"
fi
curl -s --max-time 10 "$base/credits" 2>/dev/null
```

Parse the JSON response and present a clear summary:
- Credits used / limit
- Credits remaining
- Days until reset
- Subscription plan name
- Usage percentage (with a visual bar if possible)

If the endpoint is not reachable, explain that the proxy must be running and
suggest `claude2kiro run`, `claude2kiro desktop`, or starting the TUI.
