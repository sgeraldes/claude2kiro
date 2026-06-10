# Usage

## Quick start

```bash
claude2kiro
```

This opens the interactive TUI, which is the easiest way to use Claude2Kiro.

You can also use the direct command flow:

```bash
claude2kiro login
claude2kiro run
```

## Core commands

### `claude2kiro`

Launches the interactive TUI.

From there you can:
- log in to Kiro
- start the proxy
- inspect sessions and logs
- open settings
- launch Claude Code

<p align="center">
  <img src="images/proxy-session-dashboard.png" alt="Claude2Kiro interactive TUI dashboard" width="900" />
</p>

### `claude2kiro login`

Opens the browser-based login flow and stores your Kiro credentials locally.

Supported login methods:
- GitHub
- Google
- AWS Builder ID
- Enterprise Identity Center

Examples:

```bash
claude2kiro login github
claude2kiro login google
claude2kiro login builderid
claude2kiro login idc d5
claude2kiro login idc my-company
```

### `claude2kiro run`

Starts the local proxy and launches Claude Code through it.

This is the main day-to-day command.

It:
- starts the proxy
- prepares the Claude Code environment
- installs or refreshes the local plugin
- launches Claude Code using your Kiro-backed access

<p align="center">
  <img src="images/claude-code-using-proxy-light.png" alt="Claude Code running through Claude2Kiro" width="900" />
</p>

When launched this way, Claude Code shows that the session is powered by **Kiro via claude2kiro proxy**.

It also installs the local **kiro-proxy** plugin so you can use these slash commands inside Claude Code:
- `/kiro-proxy:status`
- `/kiro-proxy:credits`
- `/kiro-proxy:logs`
- `/kiro-proxy:models`
- `/kiro-proxy:config`

### Choosing a model

Claude Code's `/model` dialog only lists the models built into your Claude Code
binary — it does not query the proxy. On an older Claude Code build the newest
models are simply missing from that dialog (Homebrew in particular lags behind;
prefer the native installer from <https://claude.ai/install.sh> and keep Claude
Code updated).

Every model Kiro exposes works regardless of what the dialog shows:

- **in-session**: type `/model claude-opus-4-8` (any id from `/kiro-proxy:models`,
  Anthropic-style `claude-opus-4-8` or Kiro-style `claude-opus-4.8`)
- **at launch**: `claude2kiro run --model claude-opus-4-8`
- **via env**: `ANTHROPIC_MODEL=claude-opus-4-8 claude2kiro run`
- **as default**: set `"model": "claude-opus-4-8"` in `~/.claude/settings.json`

Run `claude2kiro models` (CLI) or `/kiro-proxy:models` (inside Claude Code) to
see the live list with credit multipliers.

Being logged into claude.ai when you **launch** `claude2kiro run` is fine: with
the proxy environment set, requests go through the proxy and are served by
Kiro. Login state at launch only changes which entries the `/model` dialog
shows.

What you must **not** do is run `/login` (or log out and back in) *inside* a
claude2kiro session. Choosing "Claude account with subscription" there switches
the session out of API mode — from that point requests go directly to Anthropic
on your subscription, bypassing Kiro entirely. If that happens, exit Claude
Code and relaunch with `claude2kiro run` to get back on Kiro billing.

#### No Claude account needed — but Kiro model lists are per-account

claude2kiro does **not** require a Claude/Anthropic account or subscription:
Claude Code runs in API mode against the local proxy. Without a claude.ai
login the `/model` dialog just shows fewer entries — switching by id
(`/model <id>`) works the same.

Kiro, however, exposes models **per account and plan**: `claude2kiro models`
(or `/kiro-proxy:models`) shows the list for *your* account, and a model
missing from that list is rejected by Kiro even though the proxy forwards it.
When the proxy has to resolve a generic model id (like a bare `opus` alias),
it picks the highest version *your* account offers — which is why an account
without preview models lands on Opus 4.6.

Quick checks:

- **Which model is the session on?** The statusline shows the id Claude Code
  sends; `curl -s "$ANTHROPIC_BASE_URL/resolve?model=<id>"` (inside a session)
  or `claude2kiro resolve <id>` shows the Kiro model that serves it and
  whether your account has it (`in_live_catalog`).
- **Does it actually respond?** `claude2kiro test "Reply with OK" <kiro-id>`
  sends a real request (uses a fraction of a credit).

If Claude Code's input freezes after `/model <id>` (Esc once or twice
recovers), update Claude Code — Homebrew lags several versions behind; use
the native installer from <https://claude.ai/install.sh>.

### `claude2kiro update`

Downloads the latest release and switches the launcher to the new version.

Use this when you want to upgrade without reinstalling manually.

## Full command list

| Command | Description |
|---|---|
| `claude2kiro` | Launch the interactive TUI |
| `claude2kiro login` | Open the browser-based login flow |
| `claude2kiro read` | Show saved token information |
| `claude2kiro refresh` | Refresh the current token |
| `claude2kiro export` | Print environment variables for manual proxy usage |
| `claude2kiro claude` | Configure Claude Code globally |
| `claude2kiro run [args...]` | Start proxy and launch Claude Code |
| `claude2kiro server [port]` | Run the proxy without launching Claude Code |
| `claude2kiro update` | Download the latest release |
| `claude2kiro logout` | Remove saved credentials |

## Update flow

If installed via the quick installers, Claude2Kiro uses a launcher plus a versioned binary.

- launcher: `~/.local/bin/claude2kiro` or `%USERPROFILE%\.local\bin\claude2kiro.exe`
- app: `~/.claude2kiro/bin/claude2kiro-X.Y.Z` or `%USERPROFILE%\.claude2kiro\bin\claude2kiro-X.Y.Z.exe`

`claude2kiro update` downloads a new versioned binary and switches `current.txt` to it.

## Configuration

Settings are saved in `~/.claude2kiro/config.yaml`. 
You can edit them directly, or press `p` (Settings) in the TUI dashboard.

Some useful settings:
- **Auto-Start Server**: automatically start the proxy server when the app is launched.
- **Debug Mode**: save raw request and response data to `~/.claude2kiro/debug/` for troubleshooting.

## Troubleshooting

### Token file not found

Run:

```bash
claude2kiro login
```

### 403 Unauthorized

Your token may be expired. Try:

```bash
claude2kiro refresh
```

### Claude Code not found

Install Claude Code first, then run:

```bash
claude2kiro run
```
