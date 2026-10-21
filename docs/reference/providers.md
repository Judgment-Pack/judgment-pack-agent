# Providers

## Sources

Schemas:

- `contract/schemas/capability.json`

Package files (`packages/harness`), with the symbols read:

- `capabilities.ts`: `PROVIDER_CAPABILITIES`

> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.

Every role selects a host connection through models.<role>.provider: same, other, or a connected ID. The package refuses an unavailable provider before any request. Cross-family failover is refused because prefix and cache do not carry.

## Tier map

| Family | low | medium | high |
| --- | --- | --- | --- |
| openai | `low` | `high` | `xhigh` |
| anthropic | `low` | `high` | `xhigh` |
| gemini | `4096` | `8192` | `24576` |
| codex | `low` | `medium` | `high` |

## Fallback tier map

| Family | low | medium | high |
| --- | --- | --- | --- |
| anthropic | `4000` | `8000` | `16000` |
| gemini | `low` | `medium` | `high` |

The optional fallbackTiers capability member supplies Anthropic enabled budgets and Gemini levels. Connection overrides apply to this map on every fallback request. A different critic binding opens its slot using the selected connection, even at the author’s family and tier; the identical binding and tier retain learned refusals. Resume refuses a changed family before dispatch and retains saved crossProvider rows with continued: false in a stopped checkpoint.

## Wire compatibility

| Family | Cache breakpoints | Thinking | Tool schemas narrowed | Images |
| --- | --- | --- | --- | --- |
| openai | false | openai-effort | false | false |
| anthropic | true | anthropic-thinking | false | true |
| gemini | false | gemini-thinking | true | true |
| codex | false | openai-effort | false | false |

The host may override a connection’s tiers and endpoint capabilities. Model loops retain off/on/ultra as their existing vocabulary; on uses medium and ultra uses high. Agent capability tiers are passed by their host binding. The matrix describes the default model wires, not the properties of every model served behind them.
