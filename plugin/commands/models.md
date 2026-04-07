---
description: Show Kiro model mappings and credit multipliers
---

Here are the Kiro model mappings and credit costs:

## Model Mappings (Anthropic -> Kiro)

| Anthropic Model ID | Kiro Model ID | Credit Multiplier |
|---------------------|---------------|-------------------|
| claude-sonnet-4-20250514 | claude-sonnet-4.0 | 1.3x |
| claude-sonnet-4-5-20250620 | claude-sonnet-4.5 | 1.3x |
| claude-sonnet-4-6-20250620 | claude-sonnet-4.6 | 1.3x |
| claude-sonnet-4-6 | claude-sonnet-4.6 | 1.3x |
| claude-haiku-4-5-20251001 | claude-haiku-4.5 | 0.4x |
| claude-opus-4-20250514 | claude-opus-4.0 | 2.2x |
| claude-opus-4-5-20250521 | claude-opus-4.5 | 2.2x |
| claude-opus-4-6-20250626 | claude-opus-4.6 | 2.2x |

Non-Claude models (also available through Kiro):
| deepseek-r1 / deepseek-reasoner | deepseek-r1 | varies |
| minimax-m1 | minimax-m1 | varies |
| qwen-2.5-max / qwen-3 | qwen-2.5-max / qwen-3 | varies |

## Family Fallback
When Claude Code sends a model not in the static map, the proxy matches by family name:
- Contains "sonnet" -> claude-sonnet-4.6
- Contains "haiku" -> claude-haiku-4.5
- Contains "opus" -> claude-opus-4.6

## Kiro Plans

| Plan | Credits/month | Price |
|------|--------------|-------|
| Free | 50 | $0 |
| Pro | 1,000 | $20 |
| Pro+ | 2,000 | $40 |
| Power | 10,000 | $200 |

## Notes
- "Auto" model uses 1.0x multiplier
- Kiro has a tool limit of ~85 tools per request (Claude Code may send 150+, proxy truncates silently)
- Run `/kiro-proxy:credits` to check your current usage
