---
description: Show Kiro model mappings and credit multipliers
---

Here are the Kiro model mappings and credit costs:

## Model Mappings (Anthropic -> Kiro)

### Claude Opus (2.2x credits)
| Anthropic Model ID | Kiro Model ID |
|---------------------|---------------|
| claude-opus-4-8 | claude-opus-4.8 |
| claude-opus-4-7 | claude-opus-4.7 |
| claude-opus-4-6 | claude-opus-4.6 |
| claude-opus-4-5 | claude-opus-4.5 |
| claude-opus-4-1 / 4-20250514 | claude-opus-4.5 (mapped) |

### Claude Sonnet (1.3x credits)
| Anthropic Model ID | Kiro Model ID |
|---------------------|---------------|
| claude-sonnet-4-8 | claude-sonnet-4.8 |
| claude-sonnet-4-7 | claude-sonnet-4.7 |
| claude-sonnet-4-6 | claude-sonnet-4.6 |
| claude-sonnet-4-5 | claude-sonnet-4.5 |
| claude-sonnet-4-20250514 | claude-sonnet-4 |

### Claude Haiku (0.4x credits)
| Anthropic Model ID | Kiro Model ID |
|---------------------|---------------|
| claude-haiku-4-8 | claude-haiku-4.8 |
| claude-haiku-4-7 | claude-haiku-4.7 |
| claude-haiku-4-6 | claude-haiku-4.6 |
| claude-haiku-4-5 | claude-haiku-4.5 |

### Non-Claude models
| Anthropic Model ID | Kiro Model ID | Credits |
|---------------------|---------------|---------|
| deepseek / deepseek-3-2 | deepseek-3.2 | 0.25x |
| minimax / minimax-m2-5 | minimax-m2.5 | 0.25x |
| minimax-m2-1 | minimax-m2.1 | 0.15x |
| glm-5 | glm-5 | 0.5x |
| qwen / qwen3-coder-next | qwen3-coder-next | 0.05x |

## Family Fallback
When Claude Code sends a model not in the static map, the proxy matches by family name:
- Contains "opus" -> claude-opus-4.8
- Contains "sonnet" -> claude-sonnet-4.8
- Contains "haiku" -> claude-haiku-4.8
- Contains "deepseek" -> deepseek-3.2
- Contains "minimax" -> minimax-m2.5
- Contains "qwen" -> qwen3-coder-next

## Kiro Plans

| Plan | Credits/month | Price |
|------|--------------|-------|
| Free | 50 | $0 |
| Pro | 1,000 | $20 |
| Pro+ | 2,000 | $40 |
| Power | 10,000 | $200 |

## Notes
- "Auto" model (1.0x) lets Kiro choose the best model per task
- Kiro has a tool limit of ~85 tools per request (proxy truncates silently)
- Run `/kiro-proxy:credits` to check current usage
