# OpenCode Go as a provider of the model gateway

Date: 2026-11-18. Issue [#854](https://github.com/kl3inIT/hypomnema/issues/854): serve OpenCode Go (OpenCode's
$10-a-month subscription for open models) through the model gateway, so every Bot and worker can use it and the
`ocg/*` models that still go through 9Router (#670) can move to the gateway. The design was approved by the owner in
conversation on 2026-11-18; this note keeps what it rests on.

## Sources

| Source | What was read |
|---|---|
| opencode (`github.com/sst/opencode`, `0220248`, cloned shallow into `.tmp/`) | `services/site/src/docs/content/console/go.mdx`, `packages/core/src/usage-limits.ts`, `packages/ai/src/provider-error.ts`, `packages/core/src/session/to-session-error.ts` |
| omp (`github.com/can1357/oh-my-pi`, `b279532`) | `packages/ai/src/usage/opencode-go.ts` |
| omniroute (`github.com/diegosouzapw/OmniRoute`, `main` on 2026-11-18) | `open-sse/config/providerErrorRules.ts` |
| 9router, hermes, litellm, bifrost, pi, openclaw | cited by the owner's design in the issue; their clones are on the owner's machine, not read again here |
| Go itself | `GET https://opencode.ai/inference/go/v1/models` (public, read 2026-11-18: 32 models) and models.dev's `api.json` provider `opencode-go` |

## What the references do

| Point | Behaviour | From |
|---|---|---|
| Go is its own provider, separate from Zen (pay-as-you-go) | different base URL and model set | opencode `services/site/src/docs/content/console/harnesses/index.mdx:42-47`; 9router `open-sse/providers/registry/opencode-go.js` vs `opencode-zen.js`; hermes, bifrost, pi |
| Base URL `https://opencode.ai/inference/go` (legacy `/zen/go/v1` still works) | | opencode `console/go.mdx:500-527`, `services/site/src/lib/legacy.ts:30-32`; litellm `llms/opencode/common_utils.py`; openclaw |
| Each model is called on its native wire, not translated: `/openai/v1/chat/completions` (GLM, Kimi, DeepSeek, MiMo, LongCat…), `/openai/v1/responses` (grok, gpt-*-luna), `/anthropic/v1/messages` (minimax-m3, qwen3.x, claude-haiku) | per-model transport table | opencode `go.mdx:500-527`; 9router `supportedFormats`; hermes `hermes_cli/models.py:2218-2235`; litellm `supported_endpoints`; pi `providers/opencode-go.ts` |
| Auth: one Console API key, `Authorization: Bearer` (Anthropic path: `x-api-key` + `anthropic-version`) | no OAuth | opencode docs/go; 9router; litellm |
| Send a stable `x-opencode-session` per conversation (Go returns 400 `MissingSessionID` without it; also the prompt-cache key) and a client User-Agent | | opencode `go.mdx:76-82,115`; hermes `agent/opencode_affinity.py:108`; bifrost `core/providers/opencode/utils.go:11-14`; pi `utils/identity.ts:122,134`; omp `opencode-go.ts` (sends `User-Agent` and `x-opencode-session` on the usage call); omniroute `.env.example:1016` |
| Model list from `GET /inference/go/v1/models` (or models.dev provider `opencode-go`), not hand-written | | opencode docs; openclaw `provider-catalog.ts` (live /v1/models + models.dev); hermes `models_dev.py:121`; pi |
| Limits: shared pool with 5-hour / weekly / monthly windows, read from `GET https://opencode.ai/zen/go/v1/usage` (`usage.{rolling,weekly,monthly}.{percent,status,resetsAt}`); 403 is a workspace with no Go subscription | shown as account limits | opencode `packages/core/src/usage-limits.ts:155-190`; omp `packages/ai/src/usage/opencode-go.ts:22-32,191-203`; 9router `open-sse/services/usage/opencode-go.js` |
| 429 "Monthly usage limit reached. Resets in N days…" = quota exhausted: lock that key until reset, move to the next key, do not retry | several keys rotate | omniroute `open-sse/config/providerErrorRules.ts:205-231` (`opencode-monthly-quota-resets-in`, `parseResetCountdownMs` at `:781-806`); opencode `packages/ai/src/provider-error.ts:89-97` (`GoUsageLimitError` not retried); 9router, omp (multi-key pools) |
| A multi-key pool ranks by the 5 hours and the week; the month is shown only | | omp `opencode-go.ts` `opencodeGoRankingStrategy` |

## What the live list says (2026-11-18)

Each row of `GET /inference/go/v1/models` carries `api`, an object keyed by the wire (`chat-completions`,
`responses`, `messages`, `generate-content`) with its base URL, and `limit.context` and `limit.output`. Of 32 models,
20 are on `chat-completions`, 5 on `responses` (grok-4.7, gpt-5.6-luna, gpt-6-luna, muse-spark-1.2-contributor,
muse-spark-1.3-contributor), 6 on `messages` (claude-haiku-5-5, minimax-m3, qwen3.7-plus, qwen3.8-flash, qwen3.8-max,
qwen4-max) and 1 on `generate-content` (gemini-4-argon). models.dev's `opencode-go` says the same through each model's `provider.npm` (`@ai-sdk/anthropic`
for the Messages models, `@ai-sdk/openai` for the Responses ones) and gives the provider's own API as the
OpenAI-compatible one at `/openai/v1`.

## What was built, and from where

| Decision | From |
|---|---|
| A provider kind `opencode_go`, seeded by migration 00120 as `opencode-go`, served as `opencode-go/<model>` like the subscriptions | every reference keeps Go as its own provider |
| Each key is an account of runtime `opencode_go`, added with `accounts add opencode_go <label> --token -` or the Models page's "Add account", kept in the vault; the accounts are picked, rested and rotated by the code every subscription uses (`subscription.Service.Token`, `modelgateway/accounts.go` `underAccounts`) | omniroute, 9router, omp multi-key pools |
| The wire of each model is read from the live list's `api`, kept with the hourly reading of the list; unknown is chat, the provider's own API in models.dev | opencode docs; models.dev; openclaw, hermes, pi read the live list |
| A route that speaks the model's wire passes the body on (`/v1/chat/completions` to chat, `/v1/messages` to Messages, `/v1/responses` to Responses); the chat route translates to the other two with the Claude translation without its disguise and the Codex translation without its lite shape; Gemini (`generate-content`) is left out | opencode `go.mdx`, 9router, litellm, hermes, pi; the issue leaves Gemini out |
| `x-opencode-session` is a hash of the gateway's session (the client's prompt cache key or session header, else its gateway key); `User-Agent: hypomnema/<version>` | opencode docs, pi, bifrost, hermes |
| The provider's list is set to Go's live list every hour, as Antigravity's roster is | openclaw, hermes, pi (live /v1/models) |
| `accounts limits` reads `/zen/go/v1/usage` and shows "5 hours", "week" and "month"; a window Go calls rate-limited is used up; the ranking uses the 5 hours and the week only, through the windows every subscription is ranked by | opencode `usage-limits.ts`, omp `opencode-go.ts` |
| A 403 on the usage marks the account's plan "no Go subscription" | opencode `usage-limits.ts` readGo, omp `opencode-go.ts` (401 and 403 flag the key) |
| A 429 "Monthly usage limit reached. Resets in N <unit>" or a `GoUsageLimitError` rests that key until the reset (no cap), or an hour when none is named, and the request moves to the next key at once | omniroute `providerErrorRules.ts`; opencode `provider-error.ts` |

## Left out

- OpenCode Zen, Gemini models on Go, the "Extra Usage" balance fallback (out of scope in the issue).
- models.dev's prices for Go models: the gateway's price book (`modelgateway/prices`) keeps the anthropic, google and
  openai vendors only, so a Go model is unpriced unless its name is one of theirs.
- Moving the `ocg/*` models off 9Router on zm: it needs a deployed release and a key, and stays with #670.
