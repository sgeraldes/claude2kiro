# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**Claude2Kiro** is a Go CLI tool that enables Claude Code (and other Anthropic API-compatible tools) to work with Kiro subscriptions instead of direct Anthropic subscriptions.

### Background: Kiro and Its Relationship to Claude

**Kiro** is Amazon's agentic AI IDE (launched July 2025) that uses Claude models as its AI backend:
- Claude Sonnet 4.0, 4.5
- Claude Haiku 4.5
- Claude Opus 4.5 (experimental)

The backend API uses the legacy `codewhisperer.amazonaws.com` endpoint because:
- **CodeWhisperer** (2022) → **Amazon Q Developer** (April 2024) → **Kiro** (July 2025)
- The API endpoints haven't been renamed yet

### How It Works

1. User runs `claude2kiro login` (authenticates via browser, creates token at `~/.aws/sso/cache/kiro-auth-token.json`)
2. Claude2Kiro reads this token and starts a local proxy server
3. Claude Code sends requests to the proxy in Anthropic API format
4. Proxy translates to CodeWhisperer format and forwards to AWS
5. AWS responds with binary event stream
6. Proxy parses and converts back to Anthropic SSE format
7. Claude Code receives standard Anthropic responses

## Build and Development Commands

**IMPORTANT**: Go is not in bash PATH. Always use full path:

```bash
# Build the application
"C:/Program Files/Go/bin/go.exe" build -o claude2kiro.exe main.go

# Run tests
"C:/Program Files/Go/bin/go.exe" test ./...

# Run specific test in parser package
"C:/Program Files/Go/bin/go.exe" test ./parser -v

# Run the application
./claude2kiro [command]
```

## Application Commands

| Command | Description |
|---------|-------------|
| `./claude2kiro login` | Authenticate via browser (GitHub, Google, AWS Builder ID, Enterprise IdC) |
| `./claude2kiro read` | Display current token information |
| `./claude2kiro refresh` | Refresh access token using refresh token |
| `./claude2kiro export` | Output environment variable commands |
| `./claude2kiro claude` | Configure Claude Code's `~/.claude.json` |
| `./claude2kiro server [port]` | Start HTTP proxy server (default: 8080) |

## Architecture

### Project Structure

```
claude2kiro/
├── main.go                 # Core application (~1000 lines)
├── parser/
│   ├── sse_parser.go       # Binary response parser
│   └── sse_parser_test.go  # Parser tests
├── go.mod                  # Go module (no external deps)
├── README.md               # User documentation
├── CLAUDE.md               # This file
└── build.bat               # Windows build script
```

### Core Components

#### 1. Token Management (`main.go:337-545`)

- **Token file**: `~/.aws/sso/cache/kiro-auth-token.json`
- **Refresh endpoint**: `https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken`
- **Functions**:
  - `getTokenFilePath()` - Cross-platform token path resolution
  - `readToken()` - Display token info
  - `refreshToken()` - Refresh expired tokens
  - `exportEnvVars()` - Output env var commands for shell
  - `setClaude()` - Configure Claude Code's settings

#### 2. API Translation (`main.go:232-303`)

- **Function**: `buildCodeWhispererRequest()`
- **Model mapping** (`main.go:218-221`):
  ```go
  var ModelMap = map[string]string{
      "claude-sonnet-4-20250514":  "CLAUDE_SONNET_4_20250514_V1_0",
      "claude-3-5-haiku-20241022": "CLAUDE_3_7_SONNET_20250219_V1_0",
  }
  ```
- Converts Anthropic messages to CodeWhisperer conversation format
- Handles system messages as conversation history
- Transforms tool definitions to CodeWhisperer format

#### 3. HTTP Proxy Server (`main.go:571-970`)

- **Endpoint**: `POST /v1/messages`
- **Backend**: `https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse`
- **Features**:
  - Streaming (SSE) and non-streaming responses
  - Automatic token refresh on 403 errors
  - Random delay (0-300ms) between SSE events for natural streaming

#### 4. Response Parser (`parser/sse_parser.go`)

Parses AWS CodeWhisperer's binary event stream format:

```
┌─────────────┬──────────────┬─────────────┬─────────────┬──────────┐
│ Total len   │ Header len   │ Headers     │ Payload     │ CRC32    │
│ (4 bytes)   │ (4 bytes)    │ (variable)  │ (variable)  │ (4 bytes)│
└─────────────┴──────────────┴─────────────┴─────────────┴──────────┘
```

Converts to Anthropic SSE events:
- `message_start`
- `content_block_start`
- `content_block_delta` (text or tool input JSON)
- `content_block_stop`
- `message_delta`
- `message_stop`

### Key Data Structures

| Structure | Purpose |
|-----------|---------|
| `TokenData` | Auth token storage (access, refresh, expiry) |
| `AnthropicRequest` | Incoming API request format |
| `CodeWhispererRequest` | Outgoing AWS request format |
| `HistoryUserMessage` | Conversation history (user) |
| `HistoryAssistantMessage` | Conversation history (assistant) |
| `CodeWhispererTool` | Tool definition for AWS format |

### Request Flow Diagram

```
Claude Code ──────► POST /v1/messages ──────► Claude2Kiro proxy
                    (Anthropic format)              │
                                                    ▼
                                        Read ~/.aws/sso/cache/
                                        kiro-auth-token.json
                                                    │
                                                    ▼
                                        buildCodeWhispererRequest()
                                                    │
                                                    ▼
                          POST to codewhisperer.us-east-1.amazonaws.com
                               /generateAssistantResponse
                                                    │
                                                    ▼
                                        parser.ParseEvents()
                                        (binary → SSE events)
                                                    │
                                                    ▼
Claude Code ◄────── SSE stream ◄────────────────────┘
                    (Anthropic format)
```

## Development Notes

- **No external dependencies**: Uses only Go standard library
- **Cross-platform**: Windows, Linux, macOS support with appropriate path handling
- **Debug output**: Uncomment `os.WriteFile()` calls to save raw responses for debugging
- **Model mapping**: Update `ModelMap` when Kiro adds new models
- **ProfileArn**: Hardcoded AWS profile ARN for CodeWhisperer access

## Kiro Context

### Pricing (as of Dec 2025)

| Plan | Credits | Price |
|------|---------|-------|
| Free | 50 | $0/month |
| Pro | 1,000 | $20/month |
| Pro+ | 2,000 | $40/month |
| Power | 10,000 | $200/month |

### Model Credit Multipliers

| Model | Multiplier |
|-------|------------|
| Auto | 1.0x |
| Haiku 4.5 | 0.4x |
| Sonnet 4.0/4.5 | 1.3x |
| Opus 4.5 | 2.2x |

## Testing

```bash
# Run all tests
"C:/Program Files/Go/bin/go.exe" test ./...

# Run parser tests with verbose output
"C:/Program Files/Go/bin/go.exe" test ./parser -v

# Test the server manually
curl -X POST http://localhost:8080/v1/messages \
  -H "Content-Type: application/json" \
  -d '{"model": "claude-sonnet-4-20250514", "max_tokens": 100, "messages": [{"role": "user", "content": "Hello"}]}'
```
