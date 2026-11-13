# A chat's context window: its source, its setting and its lifecycle

Date: 2026-11-13. Read in code under `.tmp/`: pi, omp, opencode, codex (`codex-rs`), hermes-agent, openclaw (agents);
Orca, Omnigent, t3code, OpenMausBot, assistant-ui, AI Elements (interfaces); LiteLLM, Bifrost, new-api, CLIProxyAPI,
9router, 9router-go (gateways and catalogs). Hypomnema read at `origin/main` `697c7e0`.

The owner asked where a context window comes from, whether it can be set, and how it lives through a chat, before
a context meter is added to the composer ([the chat standard](2026-11-13-chat-composer-and-screen.md), part 2).

## Hypomnema today

- The launcher writes pi's `models.json` from the gateway's models as pi starts (`deploy/coordinator/hypomnema-pi`,
  `hypomnema pi-models`, `internal/controlplane/clientcli/pimodels.go`). It writes no `contextWindow`.
- pi gives a custom model without one 128000 (`pi/packages/coding-agent/src/core/provider-composer.ts:244`,
  schema default `model-config.ts:61`). Every gateway model is thus measured and compacted against 128k.
- pi compacts when `tokens > contextWindow - reserveTokens` (`compaction/compaction.ts:267-269`), with
  `reserveTokens=16384` and `keepRecentTokens=20000` (`settings-defaults.ts:9-13`), checked after each turn
  (`agent-session.ts:3217-3251`).

## Where the window comes from

| Reference | Source | Setting | Unknown |
|---|---|---|---|
| pi | catalog generated from models.dev and OpenRouter (`packages/ai/src/models.generated.ts`) | `models.json` `contextWindow`, `modelOverrides[].contextWindow` (`provider-composer.ts:199`) | 128000 |
| omp | bundled catalog refreshed from models.dev, live `/models` (`catalog/src/provider-models/openai-compat.ts`) | `contextWindow`, `maxContextWindow`, `maxTokens` per model (`config/models-config-schema-bundle.ts:238-240`) | 128000 (`config/custom-models.ts:137`) |
| opencode | models.dev via `models.opencode.ai` (`core/src/models-dev.ts:505`) | `provider.<id>.models.<id>.limit.{context,output}` (`schema/src/config/provider.ts:54`) | 200000 for a config-only model (`schema/src/model.ts:157`) |
| codex | bundled `models-manager/models.json`, provider `/models` (`manager.rs:399`) | `model_context_window`, capped by `max_context_window` (`model_info.rs:19-31`) | 272000 (`model_info.rs:129`) |
| hermes-agent | config, `/models`, models.dev or OpenRouter, a table (`agent/model_metadata.py:2212-2243`) | `model.context_length` | 256000 (`:261`) |
| openclaw | catalog and models.dev (`src/agents/context-resolution.ts:17-22`) | `contextWindow`, `contextTokens` (`src/config/zod-schema.core.ts:277-284`) | 200000 / 128000 |
| LiteLLM | `model_prices_and_context_window.json`, pulled from GitHub | a deployment's `model_info` (`types/router.py:293`) | — |
| CLIProxyAPI | `internal/registry/models/models.json` | `max-context-length` per model (`config/config_types.go:592`) | — |
| 9router-go | models.dev (`internal/providers/capabilities.go`) | a custom model's `ContextWindow`, `MaxOutput` (`internal/db/aliases.go:70-83`) | — |
| Bifrost, new-api | own datasheet / none | no window override | — |

Shared: a catalog, mostly models.dev, gives the default; the operator overrides it per model; a fixed number covers
an unknown model.

## How it lives in a session

- **Measured** after each response: the last reported usage (input, cache, usually output) plus an estimate of what
  was added since (pi `compaction.ts:140,196-224`; omp `session/session-stats.ts:282-383`; opencode
  `session/compaction.ts:1132-1156`; codex `context_manager/history.rs:1143-1167`). Hermes counts prompt tokens only.
- **Threshold:** the window less a reserve (16384 or 15 %: pi, omp, opencode) or a share (codex 90 %, openclaw 75 %,
  hermes 50 % raised to 75 % under 512K).
- **Compaction:** an LLM summarises the older history; the newest 15 000 to 20 000 tokens are kept as they are.
- **Right after:** usage from before is dropped: pi returns `tokens: null, percent: null` until the next answer
  (`agent-session.ts:4425-4450`), hermes `-1`; the others estimate again.
- **Overflow:** compact and retry: pi once (`agent-session.ts:3178-3214`), hermes and openclaw three times; codex's
  normal turn returns the error.
- **By hand:** every one has `/compact` (hermes `/compress`).

## How the interfaces show it

| Reference | Numbers from | Colours | After compaction | Compact button |
|---|---|---|---|---|
| Orca | the agent's own report (`main/claude/claude-context-usage.ts`, `main/pi/rpc/rpc-context-usage.ts:33-101`) | red at 90 % (`native-chat-context-usage-summary.ts:14`) | ring hidden until a report | `/compact` |
| t3code | the agent's report, a model manifest as fallback (`provider/ModelManifest.ts`) | warning 80 %, error 90 %, a tick at auto-compact (`ThreadDetailsUsage.tsx:92-108`) | a count without a bar | yes (`ChatView.tsx:2817`) |
| Omnigent | server: override, catalog, LiteLLM, 128K (`llms/context_window.py:109-132`) | grey, none | an estimate at once | `/compact` |
| OpenMausBot | engine's measure, catalog, regex, 128K (`server/context-budget.ts:4-13`) | 50 / 80 %, a warning from 80 % | hidden until a reply | "compact now" (`ContextWarning.tsx`) |
| assistant-ui | the developer's prop | 65 / 85 % (`utils/context-usage.ts`) | — | no |
| AI Elements | the developer's props | none | — | no |

## Proposed to the owner

1. A gateway model gets a context window and a maximum output, set on the Models page; left empty, the value of
   models.dev for that id, else pi's 128000. From LiteLLM `model_info`, CLIProxyAPI `max-context-length`, 9router-go,
   omp and opencode.
2. `hypomnema pi-models` writes `contextWindow` and `maxTokens` into pi's `models.json`, so pi compacts at the right
   time. From pi's `models.json` and Omnigent writing one per pi session.
3. After each turn the server asks pi `get_session_stats` and keeps `contextUsage` with the chat. From Orca and t3code.
4. A ring in the composer: "used / window", amber at 80 %, red at 90 %, a tick where pi compacts (window less 16384),
   "–" after a compaction until the next answer. From t3code and Orca; 80 / 90 replaces assistant-ui's 65 / 85
   proposed before, as the interfaces that run real agents use it.
5. A "Compact" action in the ring's tooltip that sends pi its compaction. From t3code and OpenMausBot.
6. pi's compaction defaults are kept (reserve 16384, keep 20000, one retry); no setting of Hypomnema's own. From pi
   and openclaw.
