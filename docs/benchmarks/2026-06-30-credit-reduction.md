# Credit Reduction Benchmark - 2026-06-30

## Setup

- Local binary: `claude2kiro.exe` built as `1.4.5.2606301937`
- Claude Code: `2.1.197`
- Model: `claude-sonnet-4-6`
- Harness: `scripts/benchmark-credit.ps1`

The harness backs up `~/.claude2kiro/config.yaml`, starts a local proxy per
mode, validates response text, records request metrics from proxy logs, and
restores config and process environment in `finally`.

## No-Tool Matrix

Prompt sequence in one resumed Claude Code session:

1. `Reply with exactly: Hi`
2. `In one sentence, say what a local API proxy does. Do not use tools.`
3. `Reply with exactly: Done`

Run directory:

`logs/credit-benchmark-20260630-194028`

| Mode | Requests | Metering events | Credits | Credit delta vs full | Request bytes | Byte delta vs full | History lens | Tool counts | Result |
|---|---:|---:|---:|---:|---:|---:|---|---|---|
| `full` | 3 | 3 | 0.860433 | baseline | 888904 | baseline | 6, 8, 10 | 85, 85, 85 | valid |
| `current-only` | 3 | 3 | 0.734537 | -14.6% | 645041 | -27.4% | 0, 0, 0 | 85, 85, 85 | valid |
| `recent-compact` | 3 | 3 | 0.768121 | -10.7% | 646716 | -27.2% | 4, 4, 4 | 85, 85, 85 | valid |
| `none-text` | 3 | 3 | 0.274566 | -68.1% | 323056 | -63.7% | 6, 8, 10 | 0, 0, 0 | valid |
| `aggressive-cache` | 3 | 3 | 0.550409 | -36.0% | 693488 | -22.0% | 6, 8, 10 | 85, 85, 85 | valid |

## Tool Matrix

Prompt sequence in one resumed Claude Code session:

1. Run PowerShell `Write-Output "TOOL_OK_1"` and reply exactly `TOOL_OK_1`.
2. Run PowerShell `Write-Output "TOOL_OK_2"` and reply exactly `TOOL_OK_2`.

The harness validates the final returned texts `TOOL_OK_1` and `TOOL_OK_2`.
`none-text` is intentionally excluded because it disables tools.

Run directory:

`logs/credit-benchmark-20260630-193800`

| Mode | Requests | Metering events | Credits | Credit delta vs full | Request bytes | Byte delta vs full | History lens | Tool counts | Result |
|---|---:|---:|---:|---:|---:|---:|---|---|---|
| `full` | 4 | 4 | 1.072287 | baseline | 1186269 | baseline | 6, 8, 10, 12 | 85, 85, 85, 85 | valid |
| `current-only` | 4 | 4 | 0.745121 | -30.5% | 908106 | -23.5% | 0, 2, 0, 2 | 85, 85, 85, 85 | valid |
| `recent-compact` | 4 | 4 | 0.775442 | -27.7% | 841301 | -29.1% | 4, 4, 4, 6 | 85, 85, 85, 85 | valid |
| `aggressive-cache` | 4 | 4 | 0.712586 | -33.5% | 924814 | -22.0% | 6, 8, 10, 12 | 85, 85, 85, 85 | valid |

## Debug Notes

The first tool benchmark exposed two protocol invariants that request diet modes
must preserve:

- `current-only` initially dropped the previous assistant `tool_use` before a
  current user `tool_result`, causing `TOOL_USE_RESULT_MISMATCH`.
- `recent-compact` initially kept a historical user `tool_result` while dropping
  its earlier assistant `tool_use`, causing the same backend mismatch.

Both cases now have regression tests in `credit_mitigations_test.go`. The
history filter keeps the minimal matching user/assistant tool-use pair for
current and selected historical tool results.

## Findings

- `none-text` is the largest saver for tasks that explicitly do not need tools.
- `aggressive-cache` saved credits in both final runs while preserving full
  history, but it should remain experimental until repeated runs reduce variance.
- `current-only` and `recent-compact` now pass simple tool workflows after the
  tool-use/tool-result protection fixes.
- Full history and full tools remain the conservative defaults.

## Release Gate

Validated locally:

- `go test ./...`
- `cmd /c build.bat 1.4.5`
- `scripts/benchmark-credit.ps1 -Binary .\claude2kiro.exe -Scenario tool`
- `scripts/benchmark-credit.ps1 -Binary .\claude2kiro.exe -Scenario no-tool`

The 1.4.5 release can proceed only with these settings still marked
experimental and disabled by default.
