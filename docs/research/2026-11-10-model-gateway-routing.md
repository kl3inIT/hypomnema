# Model gateway: routing, providers, translation and subscriptions

Date: 2026-11-10. Studied to revise the model gateway's design (issue #20) after the owner asked for a unified
OpenAI-compatible API, subscriptions served by the gateway, omp and opencode as workers, and room for images and
speech. Clones in `.tmp/`: 9router-go `64c156d`, CLIProxyAPI `c5251b5`, Bifrost `03ead19`, LiteLLM `5103d8a0`, new-api
`bd6bb53`, omp `660a31d`, opencode `145e948`. Paths are relative to each clone.

How it was read: §1 by the main session; §2 to §6 by Sonnet scouts whose reports are summarised here, not re-read
file by file. Nothing was run against a live upstream. "Inferred" marks what no source states. This note extends
[workers, accounts and the gateway](2026-11-07-workers-accounts-gateway.md), whose §3 judged 9router and CLIProxyAPI
"not used"; the owner's decisions in §7 change that.

## 1. Routing in 9router-go

- **Model name** (`internal/handlers/chat/resolution.go` L285): `provider/model`, then an alias, then a combo (a name
  for a list of models with a strategy: fallback, round-robin, sticky, fusion), then a guess at a default provider.
- **Account** (`connections.go` L237): filter the provider's connections by the model; take the one pinned to the
  session if it is still usable; otherwise rotate, skipping connections cooling down for the account or the model.
  The session id comes from `x-claude-code-session-id`, `session-id`, `x-opencode-session-id`, `x-session-affinity`
  and others (`internal/handlerutil/response.go` L216-226); a pin lasts 2 h idle, keyed by provider, model family and
  session (`session_affinity.go`).
- **Failover** (`fallback.go` L62): try each account; 429, 401 and 403 drop the pin; the error is classified and the
  (connection, model) is locked for a time that grows with a backoff level (`providers/errorclassify.go` L415); a
  retired model skips the remaining accounts; nothing is retried once bytes reached the client.
- **Keys** (`internal/policy/policy.go`): a plan overlaid by the key's own columns: allowed models, requests and
  tokens per minute, concurrency, a budget per period.
- **Protection** (`internal/resilience/`): a circuit breaker per provider and a semaphore per account.

## 2. Providers and translation

| Reference | Translation | Adding a provider | Model set |
|---|---|---|---|
| 9router-go | Hub is OpenAI Chat; the translator is chosen by conditionals in `handlers/chat/fallback.go` (L485-505, L1098), no registry. Gemini and OpenAI about 2.0k lines, Claude and OpenAI about 2.8k (`internal/translator`) | A 4-line entry (`internal/providers/providers.go` L398); 59 of 96 executor registrations are the generic OpenAI forwarder, about 37 are bespoke (`internal/proxy/executor/init.go`); runtime "nodes" with a prefix, an API type and a base URL (`providers/compatible_nodes.go` L30) | Static list, models.dev sync, live fetch |
| Bifrost | Typed hub (`core/schemas/bifrost.go` L835); one inbound adapter per client format (`transports/bifrost-http/integrations/`) | One wide `Provider` interface with every capability (`core/schemas/provider.go` L789); Groq and OpenRouter wrap exported OpenAI handlers (`core/providers/groq/groq.go`); custom provider by config (`base_provider_type`) | Allowlist per key (`core/schemas/account.go` L141) |
| LiteLLM | Hub is OpenAI Chat; one base class per capability (`litellm/llms/base_llm/chat/transformation.py` L91, and `responses`, `embedding`…) | A JSON table for OpenAI-compatible providers (`llms/openai_like/providers.json`); `openai/<name>` with `api_base` in config | `model_list` declared by the operator; equal names form a load-balanced group |
| new-api | Hub is OpenAI Chat with a converter registry that chains steps (`relaykit/relayconvert/request_registry.go`) | A 14-method `Adaptor` (`relay/channel/adapter.go` L18-35); 59 channel types onto 39 adaptors; a channel is a database row (`model/channel.go` L22-60) | `abilities` rows (group, model, channel) with priority and weight (`model/ability.go` L18-26) |
| CLIProxyAPI | Pairwise: 7 formats, 21 pair directories (`sdk/translator/registry.go`); single pairs reach 6 to 7 thousand lines | An `openai-compatibility` entry in the config file (`internal/config/config_types.go` L1052) | A model registry with aliases |

Common to all five: the client-facing model name is separate from the upstream model; an OpenAI-compatible provider
is data; only a provider with its own wire format needs code. Bifrost, LiteLLM and 9router-go keep a passthrough path
beside translation. Bifrost and LiteLLM convert Gemini natively; neither uses Google's OpenAI-compatible endpoint.

Other capabilities: LiteLLM has a base class per capability; Bifrost and new-api put all of them in one interface
where most providers answer "unsupported"; 9router-go adds a URL per capability to the provider entry (`ImageURL`,
`TTSURL`, `STTURL`, `providers/providers.go` L10-96).

## 3. Subscriptions as an upstream

| | Codex | Claude | Antigravity |
|---|---|---|---|
| Login | PKCE with a loopback callback on port 1455; CLIProxyAPI also has a device-code flow (`sdk/auth/codex_device.go`) | PKCE with a loopback callback (CLIProxyAPI port 54545), Claude Code's own client id | Google OAuth; CLIProxyAPI loopback on port 51121; a project id found with `loadCodeAssist` then `onboardUser` |
| Upstream | `chatgpt.com/backend-api/codex/responses` | `api.anthropic.com/v1/messages?beta=true` | `daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent`, a Gemini-shaped envelope; serves Gemini, Claude and gpt-oss models |
| Body changes | Few: drop sampling and identity fields, force `stream`, an allowlist of keys (9router-go `internal/proxy/executor/codex_token_params.go` L94-135) | Many: a billing system block with a signature, a generated `metadata.user_id`, tool names remapped and mapped back, the caller's system prompt moved (CLIProxyAPI `internal/runtime/executor/claude_executor_cloaking.go`, `claude_signing.go`); 9router-go's version is simpler (`handlers/chat/claude_cloaking.go`) | Tool names suffixed and decoy tools added, schemas cleaned, thought signatures replayed (9router-go `translator/antigravity.go`; CLIProxyAPI `antigravity_reasoning_replay.go`) |
| Limit signals | `usage_limit_reached` with `resets_at`; `x-codex-*` headers; `backend-api/wham/usage` | `anthropic-ratelimit-unified-*` headers; `api/oauth/usage` | Per-model `remainingFraction` and `resetTime` from `fetchAvailableModels`; `QUOTA_EXHAUSTED`, `RetryInfo`; 9router-go caps 3 requests in flight per account (`internal/connlimit/connlimit.go` L30) and detects a 403 `TOS_VIOLATION` (`providers/antigravity_ban.go`) |
| Size, non-test | 9router-go about 2.6k lines; CLIProxyAPI about 13.4k | 9router-go about 1.6k; CLIProxyAPI about 19k | 9router-go about 3.6k; CLIProxyAPI about 19k |

- **Refresh.** 9router-go: a mutex per connection, a background loop and a retry on 401 (`proxy/oauth/singleflight.go`,
  `background.go`). CLIProxyAPI for Codex: single-flight, a journal on disk for a refresh whose outcome is unknown, a
  file lock between processes, and adopting a token another process already rotated
  (`internal/auth/codex/refresh_journal*.go`, `codex_executor_auth.go` L154-260). Both park an account whose grant is
  dead.
- **Upkeep.** Client versions and fingerprints are hardcoded in both (`codex_cli_rs/0.159.0`, `claude-cli/2.1.280`,
  `antigravity/cli/1.5.1`). Only CLIProxyAPI's Antigravity version adapts, from a manifest fetched every 6 hours
  (`internal/misc/antigravity_version.go`). 9router-go's `AdoptRequiredClaudeCodeVersion` has no caller.
- **A disagreement.** 9router-go omits Antigravity's `requestType` because it caused false 429s; CLIProxyAPI sends
  `agent`. To be tested with the owner's account.
- **Licences.** 9router-go and CLIProxyAPI are MIT; new-api is AGPL-3.0.

## 4. omp and opencode as headless workers

| | omp | opencode |
|---|---|---|
| Headless | `omp --mode rpc` (JSON lines on stdio, `docs/rpc.md`); `omp acp` | `opencode serve` (HTTP and SSE, `packages/protocol/src/groups/`); `opencode acp` |
| Permission requests | `extension_ui_request`, answered with `extension_ui_response` | `permission.asked` event, answered at `POST /api/session/:sid/permission/:rid/reply` |
| Pointing at a gateway | `~/.omp/agent/models.yml`: `baseUrl`, `apiKey`, `api`, `headers` (`docs/models.md` L47-110) | `opencode.json`: `provider.<id>.options.baseURL`, `apiKey`, `headers` (`packages/web/src/content/docs/providers.mdx` L2546) |
| Isolated configuration | `PI_CONFIG_DIR`, `OMP_PROFILE` | `OPENCODE_CONFIG_CONTENT`, `XDG_DATA_HOME` |
| Session id sent | A header only on the Anthropic wire; `prompt_cache_key` in the body on Responses | Always: `x-opencode-session-id`, `x-session-affinity`, and `prompt_cache_key` (`packages/core/src/session/model-request.ts` L312-327) |

omp has `google-antigravity` built in (`packages/ai/src/registry/oauth/google-antigravity.ts`) and can take
credentials from a remote broker (`OMP_AUTH_BROKER_URL`, `OMP_AUTH_BROKER_TOKEN`); the broker's protocol was not
read. opencode has no Antigravity provider of its own. Both speak ACP, so one runner for both is possible (inferred).

## 5. Which gateways are in use

GitHub API, 2026-11-10: LiteLLM 63k stars (Python), CLIProxyAPI 58k (Go, MIT), new-api 52k (Go, AGPL-3.0), 9router
32k (JavaScript, MIT), Portkey 13k (little activity since May), Bifrost 9k (Go, Apache-2.0). one-api has had no
release since February 2025; TensorZero is archived. Stars are not usage: CLIProxyAPI and 9router are popular with
individuals proxying subscriptions (the scout's judgement, not verified).

## 6. 9router-go's dashboard

Svelte 5, about 100 components (`web/src/components/`). The four areas the owner wants as a model: accounts and
providers (`connections/`), the model table (`combos/`), keys (`keys/`, `plans/`), usage and quota (`analytics/`,
`QuotaTrackerView.svelte`). Only the file list was read.

## 7. Decided by the owner, 2026-11-10

- One gateway module with two surfaces: native (bytes unchanged, for vendor CLIs) and unified (OpenAI-compatible,
  translating through an OpenAI-shaped hub).
- A declared model table; adapters in a registry, one small interface per capability so images and speech can be
  added without touching existing adapters; OpenAI-compatible providers as data.
- One store of accounts in the control plane, added once in the web client, used by direct-mode workers and by the
  gateway; only the control plane refreshes a refresh token.
- The gateway serves Codex, Antigravity and Claude subscriptions, in that order, porting the logic from 9router-go
  and CLIProxyAPI. The owner is the only user and accepts that a vendor may block an account. This replaces the
  earlier rules that a Claude subscription is used only by Claude Code and that the gateway never builds a request
  with a subscription.
- omp and opencode become workers; each kind of worker has a model access mode (direct, or through the gateway).
- The web screens follow 9router-go's dashboard.

The design is in issue #20; the decision record is written with the first code, as
[decisions](../decisions/README.md) requires.
