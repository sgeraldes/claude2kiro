<p align="center">
  <img src="docs/images/claude2kiro-hero.png" alt="Claude2Kiro" width="640" />
</p>

<h1 align="center">Claude2Kiro</h1>

<p align="center">
  Use Claude Code with your Kiro subscription.
</p>

<p align="center">
  <img alt="GitHub Release" src="https://img.shields.io/github/v/release/sgeraldes/claude2kiro?color=success" />
  <img alt="Downloads" src="https://img.shields.io/github/downloads/sgeraldes/claude2kiro/total?color=blue" />
  <img alt="License" src="https://img.shields.io/badge/license-MIT-blue.svg" />
  <img alt="Platform" src="https://img.shields.io/badge/platform-Windows%20%7C%20macOS%20%7C%20Linux-8A2BE2" />
  <img alt="Built with Go" src="https://img.shields.io/badge/built%20with-Go-00ADD8?logo=go&logoColor=white" />
</p>

<p align="center">
  <a href="https://github.com/sgeraldes/claude2kiro/stargazers"><img alt="GitHub Stars" src="https://img.shields.io/github/stars/sgeraldes/claude2kiro?style=social" /></a>
</p>

<p align="center">
  <a href="#features">Features</a> •
  <a href="#install">Install</a> •
  <a href="#usage">Usage</a> •
  <a href="#docs">Docs</a> •
  <a href="#license">License</a>
</p>

Claude2Kiro is a local proxy and launcher that lets Claude Code use Kiro authentication through an Anthropic-compatible interface.

> **Part of [claude2all](https://github.com/sgeraldes/claude2all)** — one isolated Claude Code launcher per backend: Kimi K3, AWS Bedrock, OpenAI (ChatGPT OAuth), Kiro (this project), and multiple claude.ai accounts.

## Features

- Use **Kiro authentication** instead of an Anthropic subscription
- Launch **Claude Code** through a local proxy with one command
- Inspect requests, responses, and sessions in the built-in **TUI dashboard**
- Install with a **PowerShell** or shell script and keep it updated with the launcher
- Run as a headless proxy for other Anthropic-compatible tools

<p align="center">
  <img src="docs/images/claude2kiro-demo-hq.gif" alt="Claude2Kiro demo" width="900" />
</p>

## Install

### Windows

```powershell
irm https://raw.githubusercontent.com/sgeraldes/claude2kiro/main/install.ps1 | iex
```

Installs `claude2kiro.exe` to `%USERPROFILE%\.local\bin` and adds that directory to your user `PATH` if needed.

### Linux / macOS

```bash
curl -fsSL https://raw.githubusercontent.com/sgeraldes/claude2kiro/main/install.sh | bash
```

### Releases

Download binaries from:

- <https://github.com/sgeraldes/claude2kiro/releases>

### Fresh machine? Nothing else to install first

Claude2Kiro bootstraps a machine that has never had Claude Code or Claude Desktop on it:

- `claude2kiro run` detects a missing `claude` CLI and offers to install it (Windows: winget's native `Anthropic.ClaudeCode` build — no Node.js needed — falling back to Anthropic's install script or npm; Linux/macOS: install script, then npm). It also seeds `~/.claude.json` so Claude Code skips its first-run onboarding/login wizard entirely — **no Anthropic account is needed for the CLI**.
- `claude2kiro desktop` (Windows) detects a missing Claude Desktop, offers to install it via winget, and writes the gateway config that routes all Desktop surfaces (Chat, Cowork, Code) through the proxy. Claude Desktop itself does require a one-time claude.ai sign-in on first launch. Model traffic still bills to your Kiro subscription, but note that Desktop gates its tabs by plan **client-side**: Chat works on a free account, while the Code and Cowork tabs only unlock with a paid claude.ai seat (Pro/Team/Enterprise) — the proxy cannot bypass that entitlement check.

## Usage

### `claude2kiro`

```bash
claude2kiro
```

Starts the interactive TUI. This is the simplest way to use the app.

From there you can:
- log in to Kiro
- start the proxy
- open the dashboard
- change settings
- launch Claude Code

<p align="center">
  <img src="docs/images/proxy-session-dashboard.png" alt="Claude2Kiro interactive TUI dashboard" width="900" />
</p>

The interactive TUI is where you can monitor sessions, inspect requests and responses, and manage the proxy without memorizing commands.

### `claude2kiro login`

```bash
claude2kiro login
```

Opens the browser-based login flow and saves your Kiro credentials locally.

Supported login methods:
- GitHub
- Google
- AWS Builder ID
- Enterprise Identity Center

Use this first if you have not authenticated yet, or if you need to switch accounts.

### `claude2kiro run`

```bash
claude2kiro run
```

The quickest way to start a session. It starts an embedded proxy, launches Claude Code pointing at it, and tears the proxy down when Claude Code exits.

**If a proxy is already running** (a TUI dashboard or `claude2kiro server`), `run` detects it and **attaches to that proxy instead of starting a second one** — so you can keep a TUI open and just `claude2kiro run` from any other terminal without spawning extra proxies. When no proxy is running, it falls back to starting its own self-contained one on a random port, and multiple such instances run independently.

Pass `--no-attach` to force a dedicated proxy for the session even when one is already running:

```bash
claude2kiro run --no-attach
```

<p align="center">
  <img src="docs/images/claude-code-using-proxy-light.png" alt="Claude Code running through Claude2Kiro" width="900" />
</p>

When Claude Code is launched this way, you will see that the session is being powered by **Kiro via claude2kiro proxy** instead of direct Anthropic billing.

It also installs the local **kiro-proxy** plugin, which gives you slash commands inside Claude Code:
- `/kiro-proxy:status`
- `/kiro-proxy:credits`
- `/kiro-proxy:logs`
- `/kiro-proxy:models`
- `/kiro-proxy:config`

> [!TIP]
> Claude Code's `/model` dialog only lists the models built into your Claude Code
> version — it does not know about Kiro. Any Kiro model works anyway: type
> `/model <id>` (e.g. `/model claude-opus-4-8`), or launch with
> `claude2kiro run --model <id>`. See `/kiro-proxy:models` for the live list,
> or [Choosing a model](docs/USAGE.md#choosing-a-model).

### `claude2kiro remote`

```bash
claude2kiro remote
```

Launches Claude Code connected to an already-running proxy (started by the TUI or `claude2kiro server`).

Use this when you want multiple Claude Code sessions sharing the same proxy. The TUI dashboard will show all requests from every connected session in one place.

```
# Terminal 1 — start the TUI and its proxy
claude2kiro

# Terminal 2 — connect a Claude Code session to it
claude2kiro remote

# Terminal 3 — connect another one
claude2kiro remote --resume
```

### `claude2kiro desktop` (Windows)

```powershell
claude2kiro desktop
```

Ensures Claude Desktop is installed (offers a winget install when missing), makes sure the proxy is running, points Desktop's gateway config at it, and launches the app. All Desktop surfaces — Chat, Cowork, and the Code tab — then route through your Kiro subscription.

Desktop reads the gateway config only at launch, so if it is already running you'll be asked whether to restart it.

> [!NOTE]
> On first launch Claude Desktop asks for a claude.ai sign-in, and no Anthropic usage is billed — model traffic goes through the local proxy to Kiro. However, Desktop's plan gating is client-side: a free account only unlocks the Chat tab; the Code and Cowork tabs require a paid claude.ai seat (Pro/Team/Enterprise) regardless of where traffic routes.

### `claude2kiro update`

```bash
claude2kiro update
```

Downloads the latest release and switches the launcher to the new version.

Use this when you want to upgrade without reinstalling manually.

## Common commands

| Command | Purpose |
|---|---|
| `claude2kiro` | Open the interactive TUI for login, dashboard, settings, and launch actions |
| `claude2kiro login` | Authenticate with Kiro and save credentials locally |
| `claude2kiro run` | Launch Claude Code — attaches to a running proxy if one exists, else starts its own (`--no-attach` forces its own) |
| `claude2kiro remote` | Launch Claude Code connected to an already-running proxy (TUI or server) |
| `claude2kiro desktop` | Install/configure/launch Claude Desktop routed through the proxy (Windows) |
| `claude2kiro update` | Download and switch to the latest released version |
| `claude2kiro agents [session]` | Per-subagent stats (turns, ingested tokens, throughput) from local Claude Code transcripts |
| `claude2kiro logout` | Remove saved credentials |
| `claude2kiro server [port]` | Run only the headless proxy for advanced/manual setups |

## Docs

- [Installation](docs/INSTALLATION.md)
- [Usage](docs/USAGE.md)
- [Winget](docs/WINGET.md)
- [Protocol translation](docs/PROTOCOL_TRANSLATION.md)
- [Anthropic SSE format](docs/ANTHROPIC_SSE_FORMAT.md)
- [CodeWhisperer binary format](docs/CODEWHISPERER_BINARY_FORMAT.md)

## Adoption

Claude2Kiro is useful if you:

- already pay for **Kiro** and want to use **Claude Code** with it
- want a local **Anthropic-compatible endpoint** for tools that speak the Messages API
- want a simpler install and update path than building from source

### Proxy modes

| Mode | Command | Proxy lifecycle | Multi-session |
|---|---|---|---|
| **Run** | `claude2kiro run` | Attaches to a running proxy if one exists; otherwise its own proxy, stopped with Claude Code | Auto-shares a running proxy |
| **Dashboard** | `claude2kiro` | Persistent — managed from the TUI | Shared via `claude2kiro run` / `remote` |
| **Server** | `claude2kiro server [port]` | Persistent — runs until stopped (Ctrl+C) | Shared via `claude2kiro run` / `remote` |

**Run** is the simplest option. If a proxy is already running (TUI or server), `run` attaches to it — the same as `claude2kiro remote`, so a TUI you keep open is reused by every `run` from any terminal. When no proxy is running, `run` starts a self-contained one on a random port, launches Claude Code, and tears that proxy down on exit. Use `claude2kiro run --no-attach` to always start a dedicated proxy regardless.

**Dashboard and server** both start a persistent proxy on a fixed port (default 8080). Additional Claude Code sessions attach automatically via `claude2kiro run`, or explicitly with `claude2kiro remote`. All requests are logged to the same files on disk.

The proxy is stateless — it reads the auth token from disk on every request and holds no session data in memory. This means you can freely switch between dashboard and server mode on the same port without affecting connected clients. Only a request that is actively streaming at the exact moment of the switch would need to retry.

## License

MIT
