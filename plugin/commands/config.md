---
description: View or modify Kiro proxy settings
argument-hint: "[setting] [value]"
allowed-tools:
  - Bash
  - Read
  - Write
---

Manage the claude2kiro proxy configuration at `~/.claude2kiro/config.yaml`.

If no arguments are provided, read and display the current config with explanations.

If a setting and value are provided, update the config file.

Available settings and their defaults:

| Setting | YAML Path | Default | Description |
|---------|-----------|---------|-------------|
| port | server.port | 8080 | Proxy listen port (TUI mode) |
| delay | network.streaming_delay_max | 0 | Streaming delay between SSE events (ms) |
| timeout | network.http_timeout | 30s | HTTP timeout for Kiro requests |
| log-dir | logging.directory | ~/.claude2kiro/logs/ | Log file directory |
| log-enabled | logging.enabled | true | Enable file logging |
| max-entries | logging.max_entries | 500 | Max log entries in memory |
| skip-perms | advanced.skip_permissions | true | Pass --dangerously-skip-permissions to claude |
| comparison | advanced.comparison_mode | false | Debug: send to both Anthropic and Kiro |
| direct | advanced.anthropic_direct | false | Bypass: send only to Anthropic |

Note: Max concurrent Kiro requests (4) and max tools per request (85) are hardcoded and cannot be changed via config.

Steps:
1. Read `~/.claude2kiro/config.yaml` (create if missing)
2. If viewing: show all current values in a table
3. If setting: update the YAML value and save
4. Confirm the change

IMPORTANT: The config file uses YAML format. Preserve existing values when editing. Duration values like timeout use Go duration format (e.g., "30s", "5m", "300ms").
