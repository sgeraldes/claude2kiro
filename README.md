# kiro2cc - Use Claude Code with Kiro Authentication

A Go CLI tool that enables you to use [Claude Code](https://claude.ai/code) (Anthropic's official CLI) by authenticating through [Kiro](https://kiro.dev/) (Amazon's agentic AI IDE) instead of an Anthropic subscription.

```
┌─────────────────────────────────────────────────────────────────┐
│                                                                 │
│    Claude Code              Cherry Studio                       │
│         │                        │                              │
│         ▼                        │                              │
│   kiro2cc claude                 │                              │
│         │                        │                              │
│         ▼                        │                              │
│   kiro2cc export                 │                              │
│         │                        │                              │
│         ▼                        ▼                              │
│   kiro2cc server ◄───────────────┘                              │
│         │                                                       │
│         ▼                                                       │
│   Anthropic API ────► kiro2cc proxy ────► AWS Backend           │
│     (format)              :8080           (Claude models)       │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

## How It Works

**Kiro** is Amazon's AI-powered IDE (launched July 2025) that uses Claude models (Sonnet 4, Sonnet 4.5, Haiku 4.5, Opus 4.5) as its AI backend. When you pay for a Kiro subscription, you're purchasing access to Claude models through Amazon's infrastructure.

**kiro2cc** extracts Kiro's authentication token and creates a local proxy server that:

1. Accepts requests in **Anthropic API format** (what Claude Code expects)
2. Translates them to **AWS CodeWhisperer format** (Kiro's backend API)
3. Authenticates using your **Kiro subscription token**
4. Translates responses back to **Anthropic format**

This allows Claude Code and other Anthropic API-compatible tools to work with your Kiro subscription.

### Why "CodeWhisperer"?

The backend API still uses `codewhisperer.amazonaws.com` because:
- **CodeWhisperer** (2022) → **Amazon Q Developer** (April 2024) → **Kiro** (July 2025)
- The underlying API endpoints haven't been renamed yet

## Screenshots

### Claude Code
<img width="1920" height="1040" alt="Claude Code working with kiro2cc" src="https://github.com/user-attachments/assets/25f02026-f316-4a27-831c-6bc28cb03fca" />

### Cherry Studio
<img width="1920" height="1040" alt="Cherry Studio working with kiro2cc" src="https://github.com/user-attachments/assets/9bb24690-1e96-4a85-a7fc-bf7cdee95c09" />

## Prerequisites

1. **Install Kiro IDE** from [kiro.dev](https://kiro.dev/)
2. **Log in to Kiro** with Google, GitHub, or AWS Builder ID
3. **Have an active Kiro subscription** (Free tier includes 50 credits)

## Kiro Pricing Reference

| Plan | Price | Credits | Credit Cost |
|------|-------|---------|-------------|
| Free | $0/month | 50 | - |
| Pro | $20/month | 1,000 | $0.04/extra |
| Pro+ | $40/month | 2,000 | $0.04/extra |
| Power | $200/month | 10,000 | $0.04/extra |

**New users get 500 bonus credits** (valid for 30 days).

### Model Credit Multipliers

| Model | Credit Multiplier | Notes |
|-------|-------------------|-------|
| Auto | 1.0x | Recommended, ~23% cheaper than direct Sonnet |
| Claude Haiku 4.5 | 0.4x | Fastest, most economical |
| Claude Sonnet 4.0 | 1.3x | Direct access |
| Claude Sonnet 4.5 | 1.3x | State-of-the-art on SWE-bench |
| Claude Opus 4.5 | 2.2x | Most intelligent, experimental |

## Installation

### From Releases

Download the appropriate binary from the [Releases](https://github.com/bestK/kiro2cc/releases) page.

### Build from Source

```bash
go build -o kiro2cc main.go
```

## Usage

### Quick Start for Claude Code

```bash
# 1. Login (interactive menu with arrow keys)
./kiro2cc login

# Or specify method directly:
./kiro2cc login github     # Login with GitHub
./kiro2cc login google     # Login with Google
./kiro2cc login builderid  # Login with AWS Builder ID
./kiro2cc login idc d5     # Enterprise IdC (smart URL: 'd5' → https://d5.awsapps.com/start)

# 2. Configure Claude Code (one-time setup)
./kiro2cc claude

# 3. Start the proxy server
./kiro2cc server

# 4. In another terminal, set environment variables
# Linux/macOS:
eval $(./kiro2cc export)

# Windows CMD:
# Copy and paste the output from: ./kiro2cc export

# Windows PowerShell:
# Copy and paste the PowerShell commands from: ./kiro2cc export

# 5. Run Claude Code
claude
```

### Commands

#### Login (Browser-based Authentication)

```bash
# Interactive menu (recommended for first-time users)
./kiro2cc login

# Or specify method directly
./kiro2cc login github
./kiro2cc login google
./kiro2cc login builderid

# Enterprise Identity Center with smart URL
./kiro2cc login idc d5              # Expands to https://d5.awsapps.com/start
./kiro2cc login idc my-company      # Expands to https://my-company.awsapps.com/start
./kiro2cc login idc https://...     # Full URL also works
```

**Interactive Menu:**

When you run `./kiro2cc login` without arguments, you'll see an arrow-key navigable menu:

```
? Select login method:
  👉 GitHub - Social login via GitHub
     Google - Social login via Google
     AWS Builder ID - Free AWS developer account
     Enterprise Identity Center - Organization SSO
```

For Enterprise Identity Center, you'll be prompted for:
1. **Start URL** - Just enter the identifier (e.g., `d5` instead of full URL)
2. **Region** - Searchable list of AWS regions

**Supported Login Methods:**

| Method | Description |
|--------|-------------|
| `github` | Social login via GitHub account |
| `google` | Social login via Google account |
| `builderid` | AWS Builder ID (free AWS account for developers) |
| `idc` | Enterprise AWS Identity Center |

> **Note:** You don't need Kiro IDE installed to use these login methods. The tool handles OAuth directly.

**Smart URL Input:**

For Enterprise Identity Center, you can enter just the identifier:
- `d5` → `https://d5.awsapps.com/start`
- `my-company` → `https://my-company.awsapps.com/start`

**Settings Persistence:**

Your login settings are saved to `~/.aws/sso/cache/kiro2cc-login-config.json`. On subsequent logins:
- Run `./kiro2cc login` → Asks to reuse saved settings (Y/n)
- Say "n" → Shows interactive menu to choose a different method

#### Read Token Information

```bash
./kiro2cc read
```

Displays the current token status from `~/.aws/sso/cache/kiro-auth-token.json`.

#### Refresh Token

```bash
./kiro2cc refresh
```

Refreshes the access token using the stored refresh token.

#### Export Environment Variables

```bash
# Linux/macOS - execute directly
eval $(./kiro2cc export)

# Windows - copy and paste the output
./kiro2cc export
```

Sets:
- `ANTHROPIC_BASE_URL=http://localhost:8080`
- `ANTHROPIC_API_KEY=<your-kiro-access-token>`

#### Configure Claude Code

```bash
./kiro2cc claude
```

Updates `~/.claude.json` to mark onboarding as complete for use with kiro2cc.

#### Start Proxy Server

```bash
# Default port 8080
./kiro2cc server

# Custom port
./kiro2cc server 9000
```

Starts an HTTP server that proxies Anthropic API requests to Kiro's backend.

## API Endpoints

When the server is running:

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/messages` | POST | Anthropic Messages API (streaming & non-streaming) |
| `/health` | GET | Health check |

### Example Request

```bash
curl -X POST http://localhost:8080/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: your-token" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "max_tokens": 1024,
    "messages": [
      {"role": "user", "content": "Hello, Claude!"}
    ]
  }'
```

## Supported Models

The proxy maps Anthropic model names to Kiro's internal model IDs:

| Anthropic Model Name | Kiro Model ID |
|---------------------|---------------|
| `claude-sonnet-4-20250514` | `CLAUDE_SONNET_4_20250514_V1_0` |
| `claude-3-5-haiku-20241022` | `CLAUDE_3_7_SONNET_20250219_V1_0` |

> **Note:** Model mappings may need updates as Kiro adds support for new models.

## Token File Format

The tool reads tokens from `~/.aws/sso/cache/kiro-auth-token.json`:

```json
{
  "accessToken": "eyJhbGciOiJSUzI1NiIs...",
  "refreshToken": "eyJjdHkiOiJKV1QiLCJl...",
  "expiresAt": "2025-12-27T00:00:00Z"
}
```

This file is created automatically when you log into Kiro IDE.

## Automatic Builds

This project uses GitHub Actions for CI/CD:

- **On Release**: Automatically builds binaries for Windows, Linux, and macOS
- **On Push/PR**: Runs tests automatically

## Architecture

```
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐
│   Claude Code   │     │  kiro2cc proxy  │     │   AWS Backend   │
│  (or any tool)  │────►│   :8080         │────►│  CodeWhisperer  │
│                 │     │                 │     │                 │
│ Anthropic API   │     │ Translates      │     │ Claude Models   │
│ format          │◄────│ requests &      │◄────│ (Sonnet, Opus,  │
│                 │     │ responses       │     │  Haiku)         │
└─────────────────┘     └─────────────────┘     └─────────────────┘
                               │
                               ▼
                        ┌─────────────────┐
                        │ ~/.aws/sso/     │
                        │ cache/kiro-     │
                        │ auth-token.json │
                        └─────────────────┘
```

### Request Flow

1. Client sends Anthropic API request to `localhost:8080/v1/messages`
2. Proxy reads Kiro auth token from filesystem
3. Request is translated to CodeWhisperer format
4. Request is sent to `codewhisperer.us-east-1.amazonaws.com`
5. Binary response is parsed and converted to Anthropic SSE format
6. Response is streamed back to client

## Features

- **Streaming Support**: Full SSE streaming with natural response timing
- **Tool Use**: Complete support for Anthropic's tool_use feature
- **Auto Token Refresh**: Automatically refreshes expired tokens on 403 errors
- **Cross-Platform**: Works on Windows, Linux, and macOS
- **Minimal Dependencies**: Only uses [promptui](https://github.com/manifoldco/promptui) for interactive menus

## Troubleshooting

### "Token file not found"

Make sure you have:
1. Installed Kiro IDE
2. Logged in at least once
3. The token file exists at `~/.aws/sso/cache/kiro-auth-token.json`

### "403 Unauthorized"

Your token may have expired. Run:
```bash
./kiro2cc refresh
```

Or log into Kiro IDE again to get a fresh token.

### Model not supported

Check if the model you're requesting is in the supported models list. You may need to update the `ModelMap` in `main.go` for newer models.

## Disclaimer

This tool uses Kiro's authentication in ways that may not be officially supported by Amazon. Use at your own risk. Amazon could change their API or block this usage at any time.

## License

MIT

## Contributing

Contributions are welcome! Please feel free to submit issues and pull requests.
