# Configuration System Plan for Claude2Kiro

## Overview

This document outlines all configurable items found in the codebase and the plan for implementing a unified configuration system.

**Total Configurable Items Found: 98**

---

## Phase 1: Configuration Infrastructure

### 1.1 Configuration File Format
- **Format**: YAML (human-readable, supports comments)
- **Location**: `~/.claude2kiro/config.yaml`
- **Fallback**: Built-in defaults if no config file exists

### 1.2 Configuration Structure
```yaml
# ~/.claude2kiro/config.yaml

server:
  port: 8080
  shutdown_timeout: 5s

api:
  codewhisperer:
    endpoint: "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse"
    credits_endpoint: "https://codewhisperer.us-east-1.amazonaws.com/getUsageLimits"
  kiro:
    auth_endpoint: "https://prod.us-east-1.auth.desktop.kiro.dev"
    refresh_endpoint: "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken"
    usage_url: "https://kiro.dev/usage"

aws:
  default_region: "us-east-1"
  default_profile_arn: "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRVQMUK"

token:
  refresh_threshold: 5m
  expiry_margin: 15m
  proactive_refresh: true

http:
  timeout: 30s

oauth:
  login_timeout: 10m
  callback_host: "127.0.0.1"

streaming:
  event_delay_max: 300ms

logging:
  enabled: true
  directory: "~/.claude2kiro/logs/"
  file_name_format: "2006-01-02.log"
  max_memory_entries: 500
  buffer_capacity: 1000
  preview_length: 100
  file_content_length: 2000
  session_retention: 72h  # Show sessions from last N hours

ui:
  colors:
    primary: "#7D56F4"
    secondary: "#04B575"
    accent: "#F25D94"
    muted: "#626262"
    border: "#383838"
    border_focus: "#7D56F4"
    text: "#FAFAFA"
    text_muted: "#A0A0A0"
    error: "#FF5555"
    warning: "#FFAA00"
    success: "#04B575"
    info: "#7D56F4"

  session_colors:
    - "#FF6B6B"  # Red
    - "#4ECDC4"  # Teal
    - "#FFE66D"  # Yellow
    - "#95E1D3"  # Mint
    - "#F38181"  # Coral
    - "#AA96DA"  # Lavender
    - "#FCBAD3"  # Pink
    - "#A8D8EA"  # Light blue
    - "#FF9F43"  # Orange
    - "#78E08F"  # Green

  logviewer:
    list_width_percent: 35
    show_status_in_list: true
    show_duration_in_list: true
    show_path_in_list: false

  dashboard:
    tick_interval: 1s
    auto_start_server: false

  menu:
    spinner_speed: 80ms
    credits_refresh_interval: 30s

  status:
    message_duration: 3s
    token_info_duration: 5s

display:
  token_preview_length: 20
  url_max_length: 45
  error_msg_length: 50
  model_name_max_length: 20

paths:
  token_file: "~/.aws/sso/cache/kiro-auth-token.json"
  login_config: "~/.aws/sso/cache/claude2kiro-login-config.json"
  claude_config: "~/.claude.json"
  claude_config_backup: "~/.claude.json.backup"

features:
  skip_credits_for_builderid: true
  session_filtering: true
  file_logging: true
```

---

## Phase 2: Prioritized Implementation

### Priority 1: Critical User-Facing Settings (TUI Settings Menu)

| Setting | Current Value | Category |
|---------|---------------|----------|
| Server port | 8080 | Server |
| Log session retention | all | Logging |
| Show status in log list | false | Display |
| Show duration in log list | false | Display |
| Auto-start server | false | Behavior |
| Theme (color preset) | default | UI |

### Priority 2: Important Settings

| Setting | Current Value | Category |
|---------|---------------|----------|
| HTTP timeout | 30s | Network |
| Token refresh threshold | 5m | Token |
| Max log entries | 500 | Logging |
| Log file content length | 2000 | Logging |
| Credits refresh interval | 30s | UI |

### Priority 3: Advanced Settings

| Setting | Current Value | Category |
|---------|---------------|----------|
| API endpoints | hardcoded | API |
| AWS region | us-east-1 | AWS |
| File paths | hardcoded | Paths |
| All color values | hardcoded | UI |

---

## Phase 3: TUI Settings Menu Design

### Menu Structure
```
Settings
├── Server
│   ├── Port: 8080
│   └── Auto-start: Off
├── Logging
│   ├── Enabled: On
│   ├── Session Retention: 72 hours
│   ├── Max Entries: 500
│   └── Clear Logs
├── Display
│   ├── Log List
│   │   ├── Show Status: On
│   │   ├── Show Duration: On
│   │   └── Show Path: Off
│   └── Theme: Default
├── Network
│   ├── HTTP Timeout: 30s
│   └── Token Refresh: 5m
└── Advanced
    ├── API Endpoints...
    ├── File Paths...
    └── Reset to Defaults
```

### Hotkeys
- `o` - Open settings from dashboard
- `Esc` - Back/Cancel
- `Enter` - Select/Toggle
- `r` - Reset to default (per item)

---

## Phase 4: Implementation Tasks

### Task 1: Create Config Package
- [ ] Create `internal/config/config.go`
- [ ] Define Config struct matching YAML structure
- [ ] Implement Load/Save functions
- [ ] Implement defaults
- [ ] Add validation

### Task 2: Create Settings TUI Component
- [ ] Create `internal/tui/settings/settings.go`
- [ ] Implement settings list view
- [ ] Implement value editing (text, toggle, select)
- [ ] Add keyboard navigation
- [ ] Add save/cancel functionality

### Task 3: Integrate Config Throughout Codebase
- [ ] Replace hardcoded values in `main.go`
- [ ] Replace hardcoded values in `cmd/commands.go`
- [ ] Replace hardcoded values in `internal/tui/*.go`
- [ ] Replace hardcoded values in `internal/tui/dashboard/*.go`
- [ ] Replace hardcoded values in `internal/tui/menu/*.go`
- [ ] Replace hardcoded values in `internal/tui/logger/*.go`

### Task 4: Add Settings Menu to Main Menu
- [ ] Add "Settings" option to main menu
- [ ] Wire up navigation
- [ ] Add settings state persistence

### Task 5: Session Filtering Features
- [ ] Add session retention filter (24/48/72h/all)
- [ ] Add "since last start" filter option
- [ ] Add time-based session grouping
- [ ] Add session cleanup on retention expiry

---

## Detailed Item Reference

### Network & API (9 items)

| Item | File:Line | Current Value | Config Key |
|------|-----------|---------------|------------|
| Default HTTP port | main.go:559, tui.go:72 | 8080 | server.port |
| CodeWhisperer endpoint | main.go:813 | https://codewhisperer... | api.codewhisperer.endpoint |
| Credits endpoint | commands.go:825 | https://codewhisperer.../getUsageLimits | api.codewhisperer.credits_endpoint |
| Kiro auth endpoint | main.go:1178 | https://prod.us-east-1.auth... | api.kiro.auth_endpoint |
| Kiro refresh endpoint | main.go:1971 | https://prod.../refreshToken | api.kiro.refresh_endpoint |
| Kiro usage URL | commands.go:928 | https://kiro.dev/usage | api.kiro.usage_url |
| OAuth callback host | main.go:1248 | 127.0.0.1 | oauth.callback_host |
| AWS default region | commands.go:162 | us-east-1 | aws.default_region |
| Default profile ARN | main.go:378 | arn:aws:codewhisperer:... | aws.default_profile_arn |

### Timeouts & Intervals (13 items)

| Item | File:Line | Current Value | Config Key |
|------|-----------|---------------|------------|
| Token refresh threshold | commands.go:136, main.go:693 | 5m | token.refresh_threshold |
| HTTP client timeout | commands.go:187, main.go:1471 | 30s | http.timeout |
| IdC login timeout | main.go:1342 | 10m | oauth.login_timeout |
| Server shutdown timeout | dashboard.go:306 | 5s | server.shutdown_timeout |
| SSE event delay max | main.go:915 | 300ms | streaming.event_delay_max |
| Status message duration | tui.go:227 | 3s | ui.status.message_duration |
| Token info duration | tui.go:324 | 5s | ui.status.token_info_duration |
| Dashboard tick interval | dashboard.go:286 | 1s | ui.dashboard.tick_interval |
| Credits retry interval | dashboard.go:383 | 30s | ui.dashboard.credits_retry_interval |
| Token expiry thresholds | session.go:94-101 | 5m, 30m | ui.token.expiry_thresholds |
| Menu spinner speed | menu.go:340 | 80ms | ui.menu.spinner_speed |
| Menu credits refresh | menu.go:460 | 30s | ui.menu.credits_refresh_interval |
| Token expiry margin | main.go:1442 | 15m | token.expiry_margin |

### UI/Display Styling (23 items)

| Item | File:Line | Current Value | Config Key |
|------|-----------|---------------|------------|
| Primary color | styles.go:7 | #7D56F4 | ui.colors.primary |
| Secondary color | styles.go:8 | #04B575 | ui.colors.secondary |
| Accent color | styles.go:9 | #F25D94 | ui.colors.accent |
| Muted color | styles.go:10 | #626262 | ui.colors.muted |
| Border color | styles.go:11 | #383838 | ui.colors.border |
| Border focus color | styles.go:12 | #7D56F4 | ui.colors.border_focus |
| Text color | styles.go:13 | #FAFAFA | ui.colors.text |
| Muted text color | styles.go:14 | #A0A0A0 | ui.colors.text_muted |
| Error color | styles.go:15 | #FF5555 | ui.colors.error |
| Warning color | styles.go:16 | #FFAA00 | ui.colors.warning |
| Success color | styles.go:17 | #04B575 | ui.colors.success |
| Info color | styles.go:18 | #7D56F4 | ui.colors.info |
| App padding | styles.go:25 | 1, 2 | ui.app.padding |
| Title margin | styles.go:31 | 1 | ui.title.margin_bottom |
| Box padding | styles.go:37 | 0, 1 | ui.box.padding |
| Help margin | styles.go:48 | 1 | ui.help.margin_top |
| Status bar padding | styles.go:54 | 0, 1 | ui.status.padding |
| Menu item padding | styles.go:86 | 2 | ui.menu.padding_left |
| Session label width | styles.go:101 | 14 | ui.session.label_width |
| Log list width % | logviewer.go:87 | 35% | ui.logviewer.list_width_percent |
| Max log entries | logviewer.go:16 | 1000 | logging.max_memory_entries |
| Session colors | logviewer.go:51-62 | 10 colors | ui.session_colors |

### Data Limits & Sizes (10 items)

| Item | File:Line | Current Value | Config Key |
|------|-----------|---------------|------------|
| Tool description limit | main.go:388 | 10000 | api.max_tool_desc_length |
| Truncation suffix | main.go:397 | ...(truncated) | api.truncation_suffix |
| Preview length | logger.go:330 | 100 | logging.preview_length |
| File content length | logger.go:180 | 2000 | logging.file_content_length |
| Token preview length | commands.go:381 | 20 | display.token_preview_length |
| Model name max length | logger.go:110 | 20 | logging.model_name_max_length |
| URL max length | dashboard.go:670 | 45 | display.url_max_length |
| Error msg length | dashboard.go:763 | 50 | display.error_msg_length |
| Logger buffer capacity | tui.go:57 | 500 | logging.buffer_capacity |
| File read buffer | logger.go:419 | 64KB/1MB | logging.read_buffer_size |

### Feature Toggles (5 items)

| Item | File:Line | Current Value | Config Key |
|------|-----------|---------------|------------|
| Skip credits for BuilderId | commands.go:99 | true | features.skip_credits_for_builderid |
| Auto-start server | tui.go:162-167 | varies | ui.dashboard.auto_start_server |
| File logging | tui.go:60-70 | true | features.file_logging |
| Session filtering | logviewer.go:79-80 | true | features.session_filtering |
| Proactive token refresh | main.go:693-710 | true | token.proactive_refresh |

### File Paths (8 items)

| Item | File:Line | Current Value | Config Key |
|------|-----------|---------------|------------|
| Token file | commands.go:78 | ~/.aws/sso/cache/kiro-auth-token.json | paths.token_file |
| Login config | main.go:948 | ~/.aws/sso/cache/claude2kiro-login-config.json | paths.login_config |
| Log directory | tui.go:61 | ~/.claude2kiro/logs/ | paths.log_directory |
| Log filename format | logger.go:244 | 2006-01-02.log | logging.file_name_format |
| Claude config | commands.go:523 | ~/.claude.json | paths.claude_config |
| Script dir (Windows) | commands.go:602 | ~/.claude2kiro/bin/ | paths.script_dir_windows |
| Script dir (Unix) | commands.go:651 | ~/.local/bin/ | paths.script_dir_unix |
| Claude config backup | commands.go:549 | ~/.claude.json.backup | paths.claude_config_backup |

---

## Session Filtering Feature Details

### Filter Options
1. **All** - Show all sessions (current behavior)
2. **Last 24 hours** - Sessions active in last 24h
3. **Last 48 hours** - Sessions active in last 48h
4. **Last 72 hours** - Sessions active in last 72h
5. **Since last start** - Sessions since proxy was started
6. **Current session only** - Only the active session

### Implementation Notes
- Store session first-seen timestamp
- Filter sessions based on last activity time
- Add configurable default filter
- Persist filter preference

---

## Notes

- Configuration file should be created on first run with defaults
- Invalid config values should fall back to defaults with warning
- Consider adding `claude2kiro config` CLI command for editing
- Consider adding config validation on startup
- Consider adding config migration for future versions
