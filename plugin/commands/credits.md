---
description: Show Kiro subscription credits usage
allowed-tools:
  - Bash
---

Check Kiro credit usage by querying the proxy's credits endpoint:

```bash
curl -s ${ANTHROPIC_BASE_URL:-http://localhost:8080}/credits 2>/dev/null
```

Parse the JSON response and present a clear summary:
- Credits used / limit
- Credits remaining
- Days until reset
- Subscription plan name
- Usage percentage (with a visual bar if possible)

If the endpoint is not available, explain that the proxy must be running and suggest `claude2kiro run` or starting the TUI.
