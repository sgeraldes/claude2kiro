# Settings Improvements Analysis

Deep analysis of each setting: what's wrong, what to fix, what to add.

---

## SERVER SETTINGS

### 1. Server Port

**Current description:** "HTTP proxy server port"

**What's actually confusing:**
- Users don't know what "proxy" means in this context
- Doesn't explain the connection to Claude Code configuration

**Better description:**
"The port where this app listens. Claude Code sends requests to localhost:PORT. After changing, re-run 'Configure Claude' from the menu."

**Live data to show:**
- Current status: "Listening" or "Not started"
- Show if port is already in use by another process (check on settings open)

**App improvements:** None needed.

---

### 2. Auto-Start Server

**Current description:** "Start server when opening dashboard"

**What's wrong:** Description is actually clear.

**Live data to show:**
- Show current server state: "Server is currently: Running/Stopped"

**App improvements:** None needed.

---

### 3. Shutdown Timeout

**Current description:** "Time to wait for graceful shutdown"

**What's confusing:**
- "Graceful" is technical jargon
- Users don't know what happens during shutdown

**Better description:**
"When stopping the server, wait this long for in-progress requests to complete before forcing shutdown. Increase if you have long AI responses being generated."

**Live data to show:** None needed.

**App improvements:** None needed.

---

## LOGGING SETTINGS

### 4. Enable Logging

**Current description:** "Write logs to disk"

**What's wrong:** Clear enough.

**Live data to show:**
- If ON: "Logging to: [full expanded path]"
- If OFF: "Logs only kept in memory (lost on exit)"

**App improvements:** None needed.

---

### 5. Log Directory

**Current description:** "Where to store log files"

**What's missing:**
- Should show the EXPANDED path (resolve ~)
- Should show if directory exists/is writable

**Better description:**
"Folder for log files. Each day creates a file like '2025-01-15.log'."

**Live data to show:**
- Expanded path: "C:\Users\Name\.claude2kiro\logs\"
- Disk used: "2.5 MB" (already implemented)
- Number of log files: "12 files"
- Oldest file: "2025-01-03.log"

**App improvements:**
- Add button/key to "Open folder in explorer" (o key?)

---

### 6. Dashboard Retention

**Current description:** "In-memory session display time"

**What's confusing:**
- "In-memory" is technical
- Doesn't explain what a "session" is
- Doesn't distinguish from file retention clearly

**Better description:**
"How long sessions stay visible in the dashboard log list. Older sessions disappear from view but remain in log files. A 'session' is one Claude Code conversation (shown with colored bars in the list)."

**Live data to show:**
- Current sessions: "3 sessions loaded"
- Oldest visible: "from 2 hours ago"
- Memory used: "245 KB" (already implemented)

**App improvements:** None needed.

---

### 7. File Retention

**Current description:** "How long to keep log files on disk"

**What's wrong:** Actually clear.

**Live data to show:**
- Files that would be deleted: "0 files older than 7d"
- Or: "3 files (1.2 MB) would be deleted if set to 7d"

**App improvements:**
- Add a "Clean now" action that applies retention immediately

---

### 8. Max Log Size (MB)

**Current description:** "Maximum total log directory size"

**What's wrong:** Clear enough.

**Live data to show:**
- Current usage with visual bar: "[████████░░] 45 MB / 100 MB"
- Files that would be deleted: "None" or "2 oldest files (0.8 MB)"

**App improvements:** None needed.

---

### 9. Max Log Entries

**Current description:** "Maximum entries in memory"

**What's confusing:**
- "Entries" - what's an entry? A request? A line?
- Doesn't explain impact on scrolling

**Better description:**
"How many items to keep in the dashboard's log list. Each request AND each response is one entry. Higher = more scrollback history but more memory."

**Live data to show:**
- Current: "127 / 500 entries"
- Memory: "245 KB" (already implemented)
- "Can scroll back: ~2 hours" (estimate based on rate)

**App improvements:** None needed.

---

### 10. File Content Length

**Current description:** "Max chars saved per entry (0=unlimited)"

**What's confusing:**
- "Per entry" - what entry? The file entry?
- Users don't know how big typical requests/responses are

**Better description:**
"Characters saved per request/response in log files. Set to 0 to save complete content. Typical Claude response: 2,000-50,000 chars."

**Live data to show:**
- Average entry size in current session: "~3,200 chars"
- Largest entry today: "45,000 chars"
- "At current setting: would truncate 12% of entries"

**App improvements:** None needed.

---

### 11. Preview Length

**Current description:** "Chars shown in list preview"

**What's confusing:**
- "List preview" - what list? Where?
- Doesn't show what it looks like

**Better description:**
"Characters shown after the timestamp in the LEFT panel's log list. Example: '│ 10:30:45 ▶ 2.1K {"messages":[{"role":"us...' - this controls how much of the '{"messages...' part you see."

**Live data to show:**
- Current terminal width: "120 columns"
- Available space for preview: "~65 chars after timestamp/status"
- "Current setting uses: 100 chars (will truncate)"

**App improvements:**
- Could auto-calculate optimal preview length based on terminal width and list width %

---

## DISPLAY SETTINGS

### 12. Show Status in List

**Current description:** "Display HTTP status code"

**PROBLEM IDENTIFIED:**
- Description says "requests" but status codes are on RESPONSES only
- More fundamentally: this should be a COLUMN that appears in the same position for ALL entries
- Currently the layout shifts around depending on entry type

**ACTUAL FIX NEEDED - COLUMN-BASED LAYOUT:**

The log list should have consistent columns:
```
│ TIME     TYPE  #NUM  STATUS  SIZE   DURATION  PREVIEW
│ 10:30:40  ▶    #01   ---     2.1K   ---       {"messages":[...
│ 10:30:42  ◀    #01   200     1.5K   345ms     {"content":...
│ 10:30:43  ●    ---   ---     ---    ---       Token refreshed
│ 10:30:44  ▶    #02   ---     3.0K   ---       {"messages":[...
│ 10:30:45  ✖    ---   ERR     ---    ---       Connection timeout
│ 10:30:50  ◀    #02   403     0.2K   125ms     {"error":...
```

Column values by entry type:
| Column   | REQ      | RES       | INF  | ERR  |
|----------|----------|-----------|------|------|
| #NUM     | #01, #02 | #01, #02  | ---  | ---  |
| STATUS   | ---      | 200, 403  | ---  | ERR  |
| SIZE     | 2.1K     | 1.5K      | ---  | ---  |
| DURATION | ---      | 345ms     | ---  | ---  |

**Better description:**
"Show a status column in the log list. Shows HTTP status (200, 403, 500) for responses, 'ERR' for errors, and '---' for requests/info messages. The column always appears in the same position for consistent alignment."

**Live data to show:**
- Status breakdown: "200:48 | 403:1 | 500:1 | ERR:2"

**App improvements:**
- MAJOR: Refactor list rendering to use fixed columns
- The setting toggles visibility of the STATUS column
- Request number (#01, #02) should be a SEPARATE setting

---

### 13. Show Duration in List

**Current description:** "Display request duration"

**PROBLEM:** Same column alignment issue as status.

**With column-based layout:**
- Duration column always in same position
- Shows actual duration for RES entries (345ms, 2.1s)
- Shows "---" for REQ, INF, ERR entries

**Better description:**
"Show a duration column in the log list. Shows how long each request took (e.g., 345ms, 2.1s) for responses, '---' for other entries. Helps identify slow requests at a glance."

**Live data to show:**
- Stats from current session:
  - "Avg: 2.3s | Slowest: 45s | Fastest: 280ms"
  - "Requests today: 127"

**App improvements:**
- Part of the column-based layout refactor
- Consider showing duration in appropriate units (ms for <1s, s for >1s)

---

### 14. Show Path in List

**Current description:** "Display URL path"

**What's wrong:** Actually clear, but could explain why it's off by default.

**Better description:**
"Show the API path (/v1/messages) in the list. Usually OFF because all requests go to the same endpoint. Turn ON if debugging routing issues."

**Live data to show:**
- Unique paths seen: "1 path: /v1/messages" or "3 paths: /v1/messages, /v1/complete, ..."

**App improvements:** None needed.

---

### 15. List Width %

**Current description:** "Width of log list panel"

**What's confusing:**
- Which panel is "list panel"? Left or right?
- What does the other panel show?

**Better description:**
"Width of the LEFT panel (log list) as percentage of screen. The RIGHT panel (entry details) gets the remaining space. 35% = narrow list + wide details. 50% = equal split."

**Live data to show:**
- Current terminal: "120 columns wide"
- Left panel: "42 columns (35%)"
- Right panel: "78 columns (65%)"
- "Preview can fit: ~25 chars"

**App improvements:**
- Show a mini ASCII diagram of the split?

---

### 16. Theme

**Current description:** "Color theme"

**What's wrong:** Clear, but should show what it affects.

**Better description:**
"Color scheme. 'default' uses your terminal's colors. 'dark' and 'light' force specific palettes. Change if text is hard to read."

**Live data to show:**
- Preview of key colors: Show sample colored text with current theme

**App improvements:**
- Could add more themes: "high-contrast", "colorblind-friendly"

---

### 17. Help Panel Position

**Current description:** "Where to show extended help"

**What's wrong:** Clear enough.

**Live data to show:**
- Current dimensions: "Help panel: 45 columns x 20 rows"

**App improvements:** None needed.

---

## NETWORK SETTINGS

### 18. HTTP Timeout

**Current description:** "Timeout for HTTP requests"

**What's confusing:**
- Which requests? To AWS? From Claude Code?
- What happens on timeout?

**Better description:**
"Max time to wait for AWS/Kiro to respond. If exceeded, request fails with timeout error. Long AI responses (code generation) can take 30-60 seconds."

**Live data to show:**
- Longest request today: "45 seconds"
- Timeouts today: "0" or "2 requests timed out"

**App improvements:** None needed.

---

### 19. Token Refresh Threshold

**Current description:** "Refresh token when expiring within"

**What's confusing:**
- Technical OAuth concepts
- Users don't know what tokens are

**Better description:**
"Your Kiro login expires periodically. This refreshes it automatically before expiration. With 5m: if token expires in 4 minutes, refresh now rather than fail mid-request."

**Live data to show:**
- Token expires: "in 3h 45m"
- Last refresh: "2 hours ago"
- "Will auto-refresh in: 3h 40m"

**App improvements:** None needed.

---

### 20. Max Streaming Delay

**Current description:** "Random delay between SSE events"

**What's confusing:**
- "SSE events" is very technical
- Users don't know this affects typing appearance

**Better description:**
"Adds slight delay between text chunks during streaming responses (the 'typing' effect). 0 = instant (robotic feel). 300ms = natural typing feel. Higher = slower but smoother."

**Live data to show:**
- Streaming active: "Yes/No"
- Average chunks per response: "~150"

**App improvements:** None needed.

---

## ADVANCED SETTINGS

### 21-25. Endpoint URLs & AWS Region

**Current issue:** These are fine as "don't touch unless you know what you're doing" settings.

**Live data to show for each:**
- Last successful call: "2 minutes ago"
- Or error: "Last call failed: 403 Forbidden"

**App improvements:** None needed for most users.

---

## NEW SETTINGS TO ADD

Based on this analysis:

### A. Show Request Number in List
**Purpose:** Show sequential number (#01, #02) for each request/response pair
**Display:** "│ 10:30:45 ▶ #01 OK 2.1K..."
**Default:** ON
**Why:** Instantly correlate requests with their responses

### B. Show Body Size in List
**Purpose:** Already implemented, but should be a toggle
**Display:** "│ 10:30:45 ▶ #01 OK 2.1K..."
**Default:** ON
**Why:** Some users may not want the extra column

### C. Show System Messages
**Purpose:** Toggle visibility of INF (●) and ERR (✖) entries
**Options:**
- ON = Show all entries including info/error messages
- OFF = Only show REQ (▶) and RES (◀) entries
**Default:** ON
**Why:** When debugging specific requests, system messages are noise. When troubleshooting auth/connection issues, they're essential.

### D. Auto-Scroll Mode
**Purpose:** Control auto-follow behavior when new entries arrive
**Options:**
- "always" = always jump to newest
- "smart" = only if already at bottom (default)
- "never" = never auto-scroll
**Default:** smart
**Why:** Some users want to read old entries without being yanked to bottom

### E. Timestamp Format
**Purpose:** How to display times
**Options:**
- "time" = 15:04:05 (default)
- "datetime" = Jan 15 15:04
- "relative" = 2m ago
**Default:** time
**Why:** Different preferences, "relative" useful for seeing "how long ago"

### F. Compact Mode
**Purpose:** Reduce visual spacing
**Effect:** Smaller margins, denser list
**Default:** OFF
**Why:** Fit more entries on screen for users with smaller terminals

---

## IMPLEMENTATION PRIORITY

### Phase 1 - Fix descriptions and add live data
1. Fix "Show Status in List" to say RESPONSES not requests
2. Add live stats to relevant settings (status breakdown, timing stats, etc.)
3. Expand log directory path in help panel

### Phase 2 - New settings
1. Show Request Number in List (correlates req/res)
2. Show Body Size in List (make current behavior toggleable)
3. Auto-Scroll Mode

### Phase 3 - Polish
1. Timestamp Format option
2. Compact Mode
3. Better theme support

---

## QUESTIONS FOR USER

1. Request numbering - should it be per-session or global?
2. Should the request number reset daily or keep counting?
3. Compact mode - how compact? Just less spacing or also smaller text?

---

## MAJOR REFACTOR: COLUMN-BASED LOG LIST

The log list currently builds each line differently based on entry type.
This causes visual inconsistency - things jump around.

**Current (inconsistent):**
```
│ 10:30:40 ▶ 2.1K {"messages":[...
│ 10:30:42 ◀ 200 1.5K 345ms {"content":...
│ 10:30:43 ● Token refreshed
│ 10:30:45 ✖ Connection timeout
```

**Proposed (fixed columns with compact headers):**
```
│ TIME     T  #   STA  SIZE  DUR   PREVIEW
│ 10:30:40 ▶ #01  OK   2.1K  12ms  {"messages":[...
│ 10:30:42 ◀ #01  200  1.5K  2.3s  {"content":...
│ 10:30:43 ● ---  ---  ---   ---   Token refreshed
│ 10:30:46 ▶ #02  400  0.1K  5ms   Invalid JSON...
│ 10:30:48 ◀ #02  403  0.2K  1.1s  {"error":"Access...
```

Note: Headers shown for illustration - actual display may not show headers (just data rows).

**Columns (left to right):**
1. Session color bar (│) - 1 char
2. TIME - 8 chars (10:30:40)
3. T (type ▶ ◀ ● ✖) - 1 char
4. # (request number) - 3 chars - NEW, toggleable
5. STA (status OK/200/400/ERR/---) - 3 chars - toggleable
6. SIZE (2.1K/---) - 4 chars - toggleable
7. DUR (duration 12ms/2.3s/---) - 5 chars - toggleable
8. PREVIEW (rest of line)

**REQ vs RES data:**
| Column | REQ (▶)              | RES (◀)              | INF (●) | ERR (✖) |
|--------|----------------------|----------------------|---------|---------|
| #      | #01, #02...          | #01, #02... (same)   | ---     | ---     |
| STA    | OK or 400 (parse)    | 200, 403, 500 (AWS)  | ---     | ---     |
| SIZE   | Request body size    | Response body size   | ---     | ---     |
| DUR    | Time to receive/parse| Round-trip to AWS    | ---     | ---     |

**Settings that control columns:**
- Show Request Number: toggles # column
- Show Status in List: toggles STA column
- Show Body Size in List: toggles SIZE column (NEW setting)
- Show Duration in List: toggles DUR column

**Implementation location:** `internal/tui/dashboard/logviewer.go` in `renderListPanel()`

**Benefits:**
- Visual consistency - eyes can scan a column
- Easier to spot patterns (all 200s, then a 403)
- Request numbers make it trivial to match req/res pairs
- Each column toggleable = user customization

---

## IMPLEMENTATION ORDER

### Phase 1: Column-based list (MOST IMPACTFUL)
1. Refactor `renderListPanel()` to use fixed columns
2. Add request number tracking (in logger.go, already have RequestID but need sequential #)
3. Add "Show Request Number" setting
4. Add "Show Body Size" setting (make current behavior toggleable)
5. Fix all setting descriptions for column settings

### Phase 2: Live data in settings help panel
1. Add stats tracking (status breakdown, timing stats)
2. Update each setting's help panel to show relevant live data
3. Add visual elements (progress bars for disk usage, etc.)

### Phase 3: New settings
1. Auto-Scroll Mode (always/smart/never)
2. Timestamp Format (time/datetime/relative)
3. Compact Mode
