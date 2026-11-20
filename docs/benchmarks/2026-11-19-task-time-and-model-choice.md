# Task time through the proxy, and which model to choose (2026-11-19)

Why a task that takes about 15 minutes on Claude Code with an Anthropic subscription took 50 to 80 minutes through claude2kiro, what was changed, and what the measurements say about choosing a model. Every number below was measured on 2026-11-19 against the live Kiro backend. Each row is one run unless it says otherwise.

## The task

One real agent task: read a 12 KB brief and an 83 KB HTML mockup, and write a new self-contained HTML mockup (100 to 130 KB) from them. Same prompt, each run in its own copy of the inputs, 45 minute ceiling.

## Result: the whole task

| Client | Model | Wall time | Requests | Credits | Output |
|---|---|---|---|---|---|
| claude2kiro v1.9.0 | auto | 34.6 min | 25 | 36.87 | 125,838 bytes, syntax ok |
| claude2kiro v1.10.0 | auto | 13.5 min | 32 | 19.45 | 117,862 bytes, syntax ok |
| claude2kiro v1.10.0 | claude-opus-5, thinking off | 17.5 min | 70 | 68.24 | 130,624 bytes, syntax ok |
| Kiro CLI 3.5.0 (official client) | auto (its default) | 30.3 min | not available | not available | 103,813 bytes, syntax ok |
| Claude Code, Anthropic subscription | Opus | 14.5 min | not available | not available | 123,226 bytes, syntax ok |

All five finished with exit code 0. None had a request over 120 seconds, an error line, or a resent request. "Syntax ok" is `node --check` on the page's script; design quality was not judged. Some runs overlapped in time, so machine load and rate limits may have affected them.

With `auto`, v1.10.0 finished the task in 39% of the time and 53% of the credits of v1.9.0, and in the same range as Claude Code on the Anthropic subscription. In the v1.10.0 run 27 of 32 requests carried the model's earlier reasoning.

## What was wrong

Found by replaying one real step of that task (91,000 input tokens) straight against the backend and through the proxy.

1. The proxy read the whole backend answer before sending anything. A client that heard nothing for five to six minutes resent the identical request while the first kept running. Measured with real Claude Code and a backend silent for 400 seconds: 1.8.1 sent three requests and had not finished after 15 minutes; v1.9.0 finished in 6m40s with one request.
2. The backend streams the model's reasoning, signed. The proxy dropped it. The official Kiro CLI sends it back in the next request's history. Without it, `auto` planned again from scratch: a second turn took 290 seconds and 4.22 credits and made a trivial tool call; with the reasoning returned it took 44.5 seconds and 1.36 credits and wrote the file.
3. The proxy never told the backend that the client had thinking off. The backend's default is adaptive thinking on. On that step: Opus 5.5 adaptive 329 s and 8.26 credits; Sonnet 5.5 adaptive with effort low 367 s and 7.50; Opus 4.8 with thinking disabled 13.2 s and 0.86; Sonnet 5 disabled 5.2 s and 0.43; Haiku 5.5 disabled 4.7 s and 0.11. Lowering effort did not shorten the reasoning.
4. The backend's stop reason was ignored. A response that spent its whole output budget on reasoning and stopped at the limit with no content was reported to the client as a finished turn.
5. Every tool result was sent twice, in the message text and in the structured tool results. On that step 99,159 of about 340,000 bytes were duplicates.
6. The effort table was hardcoded up to Claude 4.8, so for the 5.x models the effort the client asked for was dropped.
7. `main` identified as Kiro 0.11.107, which the backend rejects.

Releases: v1.9.0 fixed 1, 6 and 7. v1.10.0 fixed 2, 3, 4 and 5.

## One step, before and after

Same step, two turns, through the proxy.

| Proxy | Model | Turn | Time | Credits | Request size |
|---|---|---|---|---|---|
| v1.9.0 | claude-opus-5 | 1 | 11.4 s | 1.20 | 365 KB |
| v1.10.0 | claude-opus-5 | 1 | 4.8 s | 0.72 | 250 KB |
| v1.9.0 | claude-opus-5 | 2 | 3.3 s | 0.59 | 367 KB |
| v1.10.0 | claude-opus-5 | 2 | 2.8 s | 0.39 | 251 KB |
| v1.9.0 | auto | 1 | 235.5 s | 3.67 | 365 KB |
| v1.10.0 | auto | 1 | 376.8 s | 5.79 | 250 KB |

The first turn of `auto` on this step took 235 to 460 seconds across six runs on the same day, on either version. `auto` accepts a request to turn thinking off and ignores it. The gain for `auto` shows on the whole task, where the plan made in that first turn is kept instead of being remade.

## Choosing a model

What the measurements support:

- **Long agent tasks: `auto`.** On the whole task it was the fastest and the cheapest of the three proxy runs. Its credit rate is 1x.
- **`claude-opus-5` with thinking off is fast per step and expensive per task.** 4.8 seconds on the heavy step, but 70 requests and 68.24 credits for the task, 3.5 times the credits of `auto`. It fits short, interactive work where each answer should arrive in seconds.
- **`claude-opus-5.5` and `claude-sonnet-5.5` cannot turn thinking off** (their catalog schema lists only `adaptive`, and `between_tools` for Sonnet). They reason for minutes on a heavy step at 2x and 1.3x. Not measured on the whole task.
- **`claude-haiku-5.5` (0.06x)** answered the heavy step in 4.7 seconds for 0.11 credits with thinking off. Not measured on the whole task.

What the measurements do not support: a rule from one step. On a single step `auto` looked eight times more expensive than Opus 5 (5.79 against 0.72 credits); on the whole task it cost less than a third.

Limits of these numbers: one run per row, one task, one day. The backend does not report which model served an `auto` request (no `modelId` arrived in any frame), so what `auto` chose is not known. Models and prices change; the table should be measured again when the catalog changes (`claude2kiro models`).

## How to repeat it

The proxy log now carries what is needed per request: `queueWait`, `firstEvent`, `stopReason`, `reasoningChars`, `reasoningAttached`, `reasoningBytes`, `toolResultChars`, and the `Kiro metering` line with the credits. To compare two setups on a task: give each its own copy of the inputs, run `claude2kiro run --no-attach -p "<prompt>"` with `ANTHROPIC_MODEL` set, time it, and sum the metering lines of that run's proxy session.
