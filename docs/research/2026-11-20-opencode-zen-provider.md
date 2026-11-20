# OpenCode Zen as a provider of the model gateway

Date: 2026-11-20. Issue [#965](https://github.com/kl3inIT/hypomnema/issues/965): serve OpenCode's models through
the model gateway. The planner's design (task `tsk_gmryblxxl5n7m5nm3j4zmk4yaq`) was approved by the owner on
2026-11-20 without its row 6 (Go's models from the Console list, to bring back `mimo-v2.5` and `mimo-v2.5-pro`):
the owner does not want those two models, and Go keeps reading `/inference/go/v1/models`. The owner also chose no
impersonation: the gateway never presents itself as OpenCode, so OpenCode's free models are not served. This note
keeps what the built part rests on; the Go provider it reuses is in
[2026-11-18-opencode-go-provider.md](2026-11-18-opencode-go-provider.md).

## Sources

| Source | What was read |
|---|---|
| opencode (`github.com/sst/opencode`, `7f90d8d`, cloned shallow into `.tmp/`) | `services/site/src/docs/content/console/api/models.mdx`, `console/api/inference.mdx`, `packages/ai/src/provider-error.ts`, `packages/ai/src/utils/provider-codes.ts` |
| omniroute (`github.com/diegosouzapw/OmniRoute`, `07a790f`) | `open-sse/executors/opencodeFreeTierContract.ts`, `open-sse/config/providerErrorRules.ts`, `open-sse/config/providers/registry/opencode/zen/index.ts` |
| 9router (`github.com/decolua/9router`, `1855aa4`) | `open-sse/providers/registry/opencode-zen.js`, `open-sse/executors/opencode-zen.js` |
| The planner's live check, 2026-11-20 | `POST /inference/openai/v1/chat/completions` with `Bearer public` and `longcat-2.5-free` from a server answered 429 `FreeUsageLimitError` ("Free models work in OpenCode…"); the public Zen list had 91 models: chat-completions 22, responses 34, messages 18, the rest on wires the gateway does not call |

No call was made with a key: the owner's key lives only on the control plane. The checks with it (the Console list
answers 200, one Zen call on each wire) are to run after deploy.

## What the references do

| Point | Behaviour | From |
|---|---|---|
| Zen is a provider of its own beside Go, each with its own key | two registry entries | 9router `providers/registry/opencode-zen.js:1-10` beside `opencode-go.js`; omniroute `config/providers/registry/opencode/zen/index.ts:4-9` |
| Root: OpenCode's inference API at `https://opencode.ai/inference`, paths `/openai/v1/chat/completions`, `/openai/v1/responses`, `/anthropic/v1/messages` | the references still call the older `/zen/v1` | opencode `console/api/inference.mdx:6,29-36`, `models.mdx:94,132`; 9router `opencode-zen.js:22,31-32`; omniroute `zen/index.ts:9` |
| Auth: `Authorization: Bearer <key>`; on the Messages path 9router sends `x-api-key` with `anthropic-version`, as the gateway already does for Go | | opencode `inference.mdx:19-27`; 9router `opencode-zen.js:31-32` |
| Free chat models answer 403 to any client but OpenCode, even with a key; OpenCode calls them as `Bearer public`, and 9router and omniroute imitate it | the owner refused imitating | opencode `inference.mdx:21-23`; 9router `executors/opencode-zen.js:288-290`; omniroute `opencodeFreeTierContract.ts` |
| The model list to offer "the same models OpenCode does" is the Console list, `GET /console/api/v1/models` with a key: providers keyed by id, Zen's is `opencode`, Go's `opencode-go`; disabled and deprecated models are left out; each model has `api` keyed by its wire, `limit` and `cost` | | opencode `models.mdx:6-17,21-45,107-128` |
| A free model is one priced 0 in and out | omniroute reads the price, not a `-free` suffix | opencode `models.mdx:119` ("`0` means free"); omniroute `opencodeFreeTierContract.ts:100-128` |
| Account caps (balance, budget, plan) are typed 402 or 429 errors with quota codes, not retried: `insufficient_quota`, `usage_not_included`, `billing_error`, `budgetlimiterror`, `creditlimitexceeded` (and Go's and the free tier's own), read from the body's `code`, `error_type`, `error.code`, `error.type`, `error.error_type`, or a message's `[code]` label | | opencode `provider-error.ts:89-98,145,245-252`, `utils/provider-codes.ts:8-35` |
| A 402 is a used-up quota | | omniroute `config/providerErrorRules.ts:121-125` |
| Zen spends the workspace's balance (`billing_source: balance`) | | opencode `inference.mdx:125-128` |

## What was built, and from where

| # | Decision | From |
|---|---|---|
| 1 | A provider kind `opencode_zen`, seeded by its migration as `opencode-zen`, served as `opencode-zen/<model>` like `opencode-go` | 9router, omniroute registries; Hypomnema migration 00120 |
| 2 | Its own runtime `opencode_zen`; keys added like Go keys (`accounts add opencode_zen <label> --token -` or the Models page); the same key may be added under both. A Go key resting on its monthly limit does not stop Zen and an empty balance does not stop Go | 9router and omniroute keep the two providers apart, each with its key; `modelgateway/accounts.go` |
| 3 | Root `https://opencode.ai/inference` with the three paths Go uses under `/inference/go` | opencode `inference.mdx` (Endpoints) |
| 4 | Go's adapter serves both (`modelgateway/opencode_adapter.go`): the wire per model from the list's `api`, the same translations and headers, the hashed `x-opencode-session`, `User-Agent: hypomnema/<version>`, no OpenCode identity | `opencode_adapter.go`; opencode `go.mdx`; owner, 2026-11-20 |
| 5 | Zen's models are the Console list's `opencode` provider, without models priced 0 and without wires other than chat, Responses and Messages; the provider is set to that list every hour, as Go's is | opencode `models.mdx`, `inference.mdx:21-23`; omniroute `opencodeFreeTierContract.ts`; `controlplane/gateway.go` `rosterRuntimes` |
| 7 | Go's own free models stay as they are, served as `opencode-go/*` under the Go key | omniroute `opencodeFreeTierContract.ts` ("For `opencode-go`, there is no free tier") |
| 8 | On Zen, a 402 or one of the five quota codes rests the key an hour (`quotaCooldown`, or a `Retry-After` up to two hours) and the next key is tried on a status that moves on (402, 403, 429); a 401 marks the key refused; any other refusal rests as every account does | opencode `provider-error.ts`; omniroute `providerErrorRules.ts`; `modelgateway/accounts.go` `cool` |
| 9 | Left out: the free lane (`Bearer public` and the imitation), Zen's balance and usage, recording `usage.cost`, a Zen price row (a Claude or GPT model is priced by its maker's row), Gemini, `interactions`, image models | owner, 2026-11-20; the issue |
| 10 | A bare model name (a profile's, a Task's, a role chain's) never picks Zen; Zen is reached by naming `opencode-zen`. Without it, every bare name Zen also lists (`claude-opus-5-5`, `deepseek-v4-pro`, named bare by Tasks on zm in the week to 2026-11-20) would be refused as offered by two providers (`orchestration/profiles.go` `checkModel`), at creation and at dispatch | 9router and omniroute address a model by its provider's alias (`ocz/<model>`; 9router `providers/registry/opencode-zen.js:3-8`); found while building, not in the approved design, so it is put to the owner |

Row 6 is not built (owner, 2026-11-20).

## To check after deploy

- `GET https://opencode.ai/console/api/v1/models` with the owner's key answers 200 with an `opencode` provider.
- The gateway's `GET /v1/models` lists `opencode-zen/*`, none of them priced 0.
- One Zen call answers 200 on each wire through the gateway: chat (`glm-5.3`), Messages (a `claude-*` model) and
  Responses (a `gpt-*` model).
