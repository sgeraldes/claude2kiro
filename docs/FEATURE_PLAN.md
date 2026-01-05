# Claude2Kiro Feature Plan v0.3.0+

This document explains all planned features, their purpose, and implementation approach.

---

## 🔴 CRITICAL: Security & Stability

### 1. PowerShell Command Injection Fix

**Location**: `cmd/commands.go:434-513`

**Problem**: User-controlled directory paths are interpolated directly into PowerShell scripts:
```go
// DANGEROUS - allows arbitrary code execution
script := fmt.Sprintf(`Start-Process ... -WorkingDirectory "%s"`, userDir)
```

If `userDir` contains `"; malicious-command; "`, it executes arbitrary code.

**Solution**: Use PowerShell parameter binding instead of string interpolation:
```go
// SAFE - parameters are escaped properly
cmd := exec.Command("powershell", "-NoProfile", "-Command",
    "Start-Process", "-FilePath", "claude",
    "-WorkingDirectory", userDir,  // Passed as argument, not interpolated
    "-ArgumentList", args)
```

**Files**: `cmd/commands.go`

---

### 2. Token Redaction in Logs

**Location**: `internal/tui/logger/logger.go:203`

**Problem**: Full request/response bodies are logged, which may contain:
- Bearer tokens in Authorization headers
- API keys in request bodies
- Refresh tokens in responses

Log files at `~/.claude2kiro/logs/*.log` have 0644 permissions (world-readable).

**Solution**: Add token redaction before logging:
```go
func redactSensitiveData(body string) string {
    // Redact Bearer tokens
    body = regexp.MustCompile(`Bearer [A-Za-z0-9._-]+`).ReplaceAllString(body, "Bearer [REDACTED]")
    // Redact API keys
    body = regexp.MustCompile(`"(api_key|apiKey|x-api-key)":\s*"[^"]+"`).ReplaceAllString(body, `"$1": "[REDACTED]"`)
    // Redact access/refresh tokens
    body = regexp.MustCompile(`"(accessToken|refreshToken)":\s*"[^"]+"`).ReplaceAllString(body, `"$1": "[REDACTED]"`)
    return body
}
```

**Files**: `internal/tui/logger/logger.go`

---

### 3. Atomic Config/Token Saves

**Location**: `internal/config/config.go:167-185`, `main.go:1809,1882,2055`

**Problem**: Direct file overwrites can corrupt data if process crashes mid-write:
```go
// DANGEROUS - partial write on crash
os.WriteFile(path, data, 0644)
```

**Solution**: Write to temp file, then atomic rename:
```go
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
    // Write to temp file in same directory
    dir := filepath.Dir(path)
    tmp, err := os.CreateTemp(dir, ".tmp-*")
    if err != nil {
        return err
    }
    tmpPath := tmp.Name()

    // Clean up on failure
    defer func() {
        if err != nil {
            os.Remove(tmpPath)
        }
    }()

    // Write data
    if _, err = tmp.Write(data); err != nil {
        tmp.Close()
        return err
    }

    // Sync to disk
    if err = tmp.Sync(); err != nil {
        tmp.Close()
        return err
    }
    tmp.Close()

    // Set permissions
    if err = os.Chmod(tmpPath, perm); err != nil {
        return err
    }

    // Atomic rename
    return os.Rename(tmpPath, path)
}
```

**Files**: `internal/config/config.go`, `main.go` (token saves)

---

### 4. Unbounded HTTP ReadAll Fix

**Location**: `main.go:867`

**Problem**: `io.ReadAll()` without size limits allows OOM attacks:
```go
// DANGEROUS - no limit
body, _ := io.ReadAll(resp.Body)
```

**Solution**: Use `io.LimitReader`:
```go
// SAFE - limit to 100MB
const maxResponseSize = 100 * 1024 * 1024
body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
if err != nil {
    return fmt.Errorf("response too large (>100MB)")
}
```

**Files**: `main.go`

---

### 5. Sensitive File Permissions (0600)

**Location**: `internal/config/config.go:184`, various temp file creations

**Problem**: Config and token files use 0644 (world-readable):
```go
os.WriteFile(path, data, 0644)  // Anyone can read
```

**Solution**: Use 0600 for sensitive files:
```go
// Config files - may contain sensitive settings
os.WriteFile(configPath, data, 0600)

// Token files - contain auth credentials
os.WriteFile(tokenPath, data, 0600)

// Log files - may contain request/response data
os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0600)
```

**Files**: `internal/config/config.go`, `main.go`, `internal/tui/logger/logger.go`

---

## 🟡 HIGH VALUE: Agent-Native CLI

### What is "Agent-Native"?

An **agent-native** tool means:
- **Any action a user can take, an agent can also take**
- **Anything a user can see, an agent can also see**

Currently, Claude2Kiro has gaps:

| Capability | Human (TUI) | Agent (CLI) | Gap |
|------------|-------------|-------------|-----|
| Check server status | ✅ Dashboard shows | ❌ No command | **Gap** |
| View credits | ✅ Stats panel | ❌ No command | **Gap** |
| Monitor logs | ✅ Log viewer | ❌ No streaming | **Gap** |
| Health check | ❌ N/A | ❌ No endpoint | **Gap** |
| Stop server | ✅ Ctrl+C | ❌ No graceful | **Gap** |

### 6. `status --json` Command

**Purpose**: Check if proxy server is running programmatically.

**Current state**: Only visible in TUI dashboard status panel.

**Proposed CLI**:
```bash
# Check if running
$ claude2kiro status
Server: running on port 8080
Uptime: 2h 34m
Requests: 156
Token: valid (expires in 4h 12m)

# JSON output for scripting
$ claude2kiro status --json
{
  "server": {
    "running": true,
    "port": 8080,
    "pid": 12345,
    "uptime_seconds": 9240
  },
  "token": {
    "valid": true,
    "expires_at": "2025-01-02T18:30:00Z",
    "expires_in_seconds": 14832
  },
  "stats": {
    "requests_total": 156,
    "requests_today": 42
  }
}
```

**Implementation**:
1. Create status file at `~/.claude2kiro/server.pid` when server starts
2. Write JSON with PID, port, start time
3. `status` command reads this file + checks if process alive
4. Query running server's `/internal/status` endpoint for live stats

**Files**: `cmd/commands.go`, `main.go` (add /internal/status endpoint)

---

### 7. `credits --json` Command

**Purpose**: Check Kiro credit balance programmatically.

**Current state**: Only visible in TUI stats panel (fetches from `getUsageLimits` API).

**Proposed CLI**:
```bash
# Human-readable
$ claude2kiro credits
Plan: Pro ($20/month)
Credits: 847 / 1,000 remaining
Reset: January 15, 2025

# JSON for scripting
$ claude2kiro credits --json
{
  "plan": "pro",
  "credits": {
    "used": 153,
    "remaining": 847,
    "total": 1000
  },
  "reset_date": "2025-01-15",
  "overage_rate": 0.04
}
```

**Implementation**:
1. Extract credit fetching logic from TUI to shared function
2. Add `credits` command to CLI
3. Support `--json` flag for machine-readable output

**Files**: `cmd/commands.go`, extract from `internal/tui/dashboard/dashboard.go`

---

### 8. `/health` Endpoint

**Purpose**: Simple endpoint for orchestration tools, load balancers, process managers.

**Current state**: No health endpoint exists.

**Proposed endpoint**:
```bash
# Simple health check
$ curl http://localhost:8080/health
{"status": "healthy", "version": "0.3.0"}

# Detailed health (optional)
$ curl http://localhost:8080/health?detailed=true
{
  "status": "healthy",
  "version": "0.3.0",
  "uptime_seconds": 3600,
  "token_valid": true,
  "token_expires_in": 14400,
  "memory_mb": 45,
  "goroutines": 12
}
```

**Use cases**:
- Docker health checks: `HEALTHCHECK CMD curl -f http://localhost:8080/health`
- Kubernetes liveness probe
- Process manager restart on unhealthy
- Load balancer routing

**Implementation**:
```go
http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
    health := map[string]interface{}{
        "status":  "healthy",
        "version": Version,
    }

    if r.URL.Query().Get("detailed") == "true" {
        health["uptime_seconds"] = time.Since(startTime).Seconds()
        health["token_valid"] = isTokenValid()
        // ... more details
    }

    json.NewEncoder(w).Encode(health)
})
```

**Files**: `main.go`

---

### 9. Graceful Shutdown (SIGTERM Handler)

**Purpose**: Clean shutdown when receiving SIGTERM/SIGINT signals.

**Current state**: Process killed abruptly, no cleanup:
- In-flight requests dropped
- Connections not closed gracefully
- No shutdown hooks

**Proposed behavior**:
```bash
# Ctrl+C or kill -TERM
$ claude2kiro server
Server running on port 8080...
^C
Received SIGTERM, shutting down gracefully...
Waiting for 3 in-flight requests to complete...
Closing connections...
Server stopped.
```

**Implementation**:
```go
func runServer(port string) {
    server := &http.Server{Addr: ":" + port}

    // Start server in goroutine
    go func() {
        if err := server.ListenAndServe(); err != http.ErrServerClosed {
            log.Fatal(err)
        }
    }()

    // Wait for shutdown signal
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    <-sigChan

    // Graceful shutdown with timeout
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    log.Println("Shutting down gracefully...")
    if err := server.Shutdown(ctx); err != nil {
        log.Printf("Shutdown error: %v", err)
    }

    // Cleanup
    cleanup()
    log.Println("Server stopped.")
}
```

**Files**: `main.go`

---

### 10. Multi-filter Search Bar

**Purpose**: Advanced filtering in TUI dashboard (already planned in `toasty-shimmying-church.md`).

**Features**:
- Type filters: `[x]req [x]res [ ]inf [ ]err`
- Text search: Real-time filtering
- Date filter: "After" timestamp (persisted)
- Session filter: Existing s/S cycling

**Status**: Plan exists, implementation pending.

**Files**: `internal/tui/dashboard/filterbar.go` (new), `dashboard.go`, `logviewer.go`

---

### 11. Column-based Log List

**Purpose**: Properly aligned, sortable columns in log list.

**Current state**: Ad-hoc string formatting, columns not perfectly aligned.

**Proposed**:
```
┌─────┬─────┬────────┬────────┬──────────────────────────────┐
│ #   │ STA │  SIZE  │  DUR   │ PREVIEW                      │
├─────┼─────┼────────┼────────┼──────────────────────────────┤
│ 001 │ 200 │ 14.2MB │ 1.234s │ Hello, I can help with...    │
│ 002 │ 200 │  2.1KB │  340ms │ Here's a code example...     │
│ 003 │ ERR │   847B │   45ms │ Rate limit exceeded          │
└─────┴─────┴────────┴────────┴──────────────────────────────┘
```

**Implementation**: Use `lipgloss.Table()` or custom table renderer.

**Files**: `internal/tui/dashboard/logviewer.go`

---

### 12-13. Settings Improvements

**12. Settings descriptions**: Add detailed help text for each setting.
**13. Live data in settings**: Show real-time stats (disk usage, memory, entry count).

**Status**: Partially implemented in v0.3.0 release.

---

## 🔵 MEDIUM VALUE: Architecture/Performance

### 14-15. Split Large Files

**Problem**:
- `main.go`: 2,653 lines (HTTP server, auth, CLI, proxy all mixed)
- `logviewer.go`: 2,736 lines (rendering, parsing, filtering all mixed)

**Solution**: Extract to focused packages:
```
main.go (300 lines) - CLI entry point only
internal/
  server/
    server.go      - HTTP server setup
    handlers.go    - Request handlers
    middleware.go  - Logging, auth middleware
  auth/
    token.go       - Token management
    refresh.go     - Token refresh logic
  proxy/
    translate.go   - API translation
    streaming.go   - SSE handling
```

**Effort**: 2-3 days each, major refactor.

---

### 16. Eliminate Type Duplication

**Problem**: Same types defined in multiple places:
- `TokenData` in `main.go` AND `cmd/commands.go`
- `CreditsInfo` in `main.go` AND dashboard
- Various message types duplicated

**Solution**: Create `internal/types/types.go` with shared definitions.

---

### 17. Fix O(n²) String Sanitization

**Location**: `internal/tui/logger/logger.go:324-327`

**Problem**:
```go
// Each iteration creates new string - O(n²) for large bodies
for strings.Contains(s, "  ") {
    s = strings.Replace(s, "  ", " ", -1)
}
```

**Solution**: Single-pass regex or custom scanner:
```go
// O(n) - single pass
s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
```

---

### 18. Buffered File I/O

**Problem**: Each log entry = 1 syscall to write.

**Solution**: Use buffered writer with periodic flush:
```go
type BufferedLogger struct {
    writer *bufio.Writer
    file   *os.File
    mu     sync.Mutex
}

func (l *BufferedLogger) Write(entry string) {
    l.mu.Lock()
    defer l.mu.Unlock()
    l.writer.WriteString(entry)
}

func (l *BufferedLogger) Flush() {
    l.mu.Lock()
    defer l.mu.Unlock()
    l.writer.Flush()
}
```

---

### 19. HTTP Connection Pooling

**Problem**: New HTTP clients created inconsistently.

**Solution**: Single shared client with connection pool:
```go
var httpClient = &http.Client{
    Transport: &http.Transport{
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout:     90 * time.Second,
    },
    Timeout: 30 * time.Second,
}
```

---

### 20. Incremental Log Filtering

**Problem**: Every filter change rescans all entries O(n).

**Solution**: Maintain pre-filtered indices:
```go
type FilteredView struct {
    allEntries     []*LogEntry
    byType         map[EntryType][]*LogEntry  // Pre-indexed by type
    filtered       []*LogEntry                // Current view
}
```

---

## 🟢 NICE-TO-HAVE: Polish

| # | Feature | Description | Priority |
|---|---------|-------------|----------|
| 21 | Theme support | Light/dark/custom themes | Low |
| 22 | Full-text search | Search across all log content | Medium |
| 23 | Log export | Export to JSON/CSV | Medium |
| 24 | Prometheus metrics | /metrics endpoint | Low |
| 25 | Multi-account | Switch between Kiro accounts | Low |
| 26 | Browser-based login | OAuth in browser instead of CLI | Medium |
| 27 | Credits tracking | Alert when credits low | Medium |
| 28 | Test coverage | Increase from ~5% to 60% | Ongoing |

---

## 🎯 Recommended Implementation Order

### Phase 1: Security (Day 1)
1. ✅ Fix PowerShell injection (#1) - 2h
2. ✅ Token redaction (#2) - 2h
3. ✅ File permissions 0600 (#5) - 30m
4. ✅ Atomic writes (#3) - 3h

### Phase 2: Agent-Native CLI (Day 2)
5. ✅ `/health` endpoint (#8) - 1h
6. ✅ `status --json` (#6) - 2h
7. ✅ `credits --json` (#7) - 2h
8. ✅ Graceful shutdown (#9) - 2h

### Phase 3: Performance (Day 3)
9. ✅ HTTP ReadAll limit (#4) - 1h
10. ✅ O(n²) string fix (#17) - 1h
11. ✅ Buffered I/O (#18) - 2h
12. ✅ Connection pooling (#19) - 2h

### Phase 4: UX Polish (Day 4+)
13. ✅ Multi-filter search bar (#10) - 4h
14. ⚠️ Column-based log list (#11) - 6h
15. ⚠️ Type duplication fix (#16) - 4h

### Phase 5: Architecture (v0.4.0)
16. ⚠️ Split main.go (#14) - 2-3 days
17. ⚠️ Split logviewer.go (#15) - 2-3 days

---

## File Impact Summary

| File | Changes |
|------|---------|
| `main.go` | #3, #4, #8, #9, #19 |
| `cmd/commands.go` | #1, #6, #7 |
| `internal/config/config.go` | #3, #5 |
| `internal/tui/logger/logger.go` | #2, #5, #17, #18 |
| `internal/tui/dashboard/filterbar.go` | #10 (new file) |
| `internal/tui/dashboard/logviewer.go` | #10, #11 |
| `internal/tui/dashboard/dashboard.go` | #10 |

---

*Plan created: 2025-01-02*
*Target: v0.3.1 (Security + Agent-Native) → v0.4.0 (Architecture)*
