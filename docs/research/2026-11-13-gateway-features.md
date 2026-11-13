# What the other gateways have that Hypomnema's lacks

Date: 2026-11-13. Question from the owner: after the gateway's pages (issue #199), what else do the reference
gateways do that Hypomnema's model gateway should add, now that it is reachable from outside so that tools such as
omp, opencode and Claude Code can use the owner's subscriptions.

Read in `.tmp/`, in their code and the docs inside the clones, at these revisions: CLIProxyAPI `c5251b5`, new-api
`bd6bb53`, LiteLLM `5103d8a0`, Bifrost `03ead19`, Helicone `7720d28`, ai-gateway `d41f082`, aibridge `d59c7ef`,
Coder `b798061` (its AI gateway parts), 9router `1fe586b`, 9router-go `64c156d`, omp `04cd1a7549` (providers,
accounts and their order) and OpenClaw `7b3c6edf5` (failover and auth profiles). A sub-agent read them; the paths
are its report and were not all opened again for this note.

## 1. What Hypomnema has already

OpenAI's `/v1/chat/completions` and `/v1/models`; `/v1/responses` passed to the Codex backend; Claude, Codex and
Antigravity subscriptions and OpenAI-compatible API providers; several accounts of a subscription, a session kept on
its account, an account that rests after a usage limit; the next target on a 429 or 5xx before anything is sent; a
model as a public name and an ordered list of targets; gateway keys kept hashed, shown once, limited to models; a
usage row per request; the 5-hour and weekly limits read; Antigravity's quota reserve.

## 2. Features, and how many of the twelve have each

Abbreviations: CPA CLIProxyAPI, NA new-api, LL LiteLLM, BF Bifrost, HC Helicone, AG ai-gateway, AB aibridge,
CD Coder, 9R 9router, 9G 9router-go, OMP omp, OC OpenClaw. Paths are relative to `.tmp/<repo>/`.

| Feature | Where | Count |
|---|---|---|
| Anthropic's `/v1/messages` (and `count_tokens`) | CPA `sdk/api/handlers/claude/`; NA `relay/claude_handler.go`; LL `litellm/proxy/anthropic_endpoints/`; BF `transports/bifrost-http/integrations/anthropic.go`; HC `worker/src/routers/anthropicProxyRouter.ts`; AG `internal/translator/anthropic_*.go`; AB `intercept/messages/`; CD `aibridge/intercept/`; 9R `src/app/api/v1/messages/`; 9G `COMPARISON.md`; OMP `packages/ai/src/auth-gateway/` | 11 |
| Request and response logs | CPA `internal/logging/request_logger.go`; NA `controller/log.go`; LL `litellm/proxy/spend_tracking/`; BF `plugins/logging/`; HC (its core); AB `recorder/`; CD `docs/ai-governance/ai-gateway/audit.md`; 9R `src/app/api/usage/request-logs/`; 9G `handlers/consolelog.go` | 9 |
| A usage page | CPA `management.html`, `internal/tui/`; NA `web/`; LL `ui/litellm-dashboard/`; BF `ui/`; HC `web/pages/dashboard.tsx`; CD `site/src/pages/AIBridgePage/`; 9R `src/app/(dashboard)/dashboard/usage/`; 9G `web/` | 8 |
| Images | CPA `sdk/api/handlers/openai/openai_images_handlers.go`; NA `relay/image_handler.go`; LL `litellm/proxy/image_endpoints/`; BF; AG `internal/translator/imagegeneration_openai_openai.go`; 9R `open-sse/handlers/imageGenerationCore.js`; 9G; OMP `routes/images.ts` | 8 |
| Speech and transcription | CPA `openai_speech_handlers.go`; NA `relay/audio_handler.go`; LL; BF `handlers/inference.go`; AG `internal/translator/openai_speech.go`; 9R `ttsCore.js`, `sttCore.js`; 9G; OMP `routes/speech.ts` | 8 |
| Embeddings | NA `relay/embedding_handler.go`; LL `litellm/embeddings/`; BF `handlers/inference.go`; AG `internal/translator/openai_embeddings.go`; 9R `open-sse/handlers/embeddingsCore.js`; 9G; OMP `routes/embeddings.ts` | 7 |
| A price per request | NA `setting/ratio_setting/`; LL `litellm/cost_calculator.py`; BF `framework/modelcatalog/`; HC `packages/cost/`; AG `internal/llmcostcel/`; CD `coderd/aibridge/prices/`; 9G `internal/pricing/` | 7 |
| A budget per key, user or team | NA `model/token.go`; LL `litellm/proxy/management_endpoints/budget_management_endpoints.py`; BF `plugins/governance/`; HC `worker/src/lib/managers/UsageLimitManager.ts.ts`; AG `docs/capabilities/traffic/quota-policy.md`; CD `coderd/aibridge/budget/`; 9G `internal/budget/` | 7 |
| Weights or a strategy among accounts | CPA `internal/credentialweight/`; NA `service/channel_select.go`; LL `litellm/router_strategy/`; BF `core/keyselectors/weightedrandom.go`; AG `docs/capabilities/traffic/`; 9G `internal/combos/smart/`; OMP `packages/ai/src/auth/select.ts` | 7 |
| Requests or tokens per minute per key | NA `middleware/model-rate-limit.go`; LL `litellm/proxy/hooks/parallel_request_limiter_v3.py`; BF `plugins/governance/`; HC `worker/src/lib/rate-limit/`; AG `internal/ratelimit/`; 9G `internal/policy/` | 6 |
| Reasoning effort mapped between providers | CPA `internal/thinking/`; NA `setting/reasoning/suffix.go`; LL; BF `core/encryptedreasoning.go`; HC `docs/gateway/concepts/reasoning.mdx`; 9G `internal/thinking/` | 6 |
| Model aliases and wildcards | CPA `sdk/cliproxy/auth/oauth_model_alias.go`; NA `model/channel.go`; LL `litellm/router.py`; AG `docs/capabilities/traffic/model-virtualization.md`; 9R `open-sse/services/combo.js`; 9G `internal/combos/` | 6 |
| Guardrails and redaction | LL `litellm/proxy/guardrails/guardrail_hooks/`; NA `setting/sensitive.go`; HC `docs/features/advanced-usage/llm-security.mdx`; AG `internal/redaction/`; 9G `internal/guardrails/`; BF (enterprise docs only) | 6 |
| OpenTelemetry or Prometheus | LL `litellm/integrations/opentelemetry.py`; BF `plugins/otel/`; AG `internal/tracing/`; AB `tracing/`; CD (docs); 9G `internal/observ/` | 6 |
| Alerts by Slack, e-mail or webhook | LL `litellm/integrations/SlackAlerting/`; HC `worker/src/lib/managers/AlertManager.ts`; NA `service/user_notify.go`; BF `handlers/webhooks.go`; CD `coderd/aibridgedserver/notifications.go`; 9G (budget alert) | 6 |
| Users, teams and SSO | LL `team_endpoints.py`, `scim/`; NA; BF; HC; CD; 9G `handlers/sso/` | 6 |
| Gemini's own `/v1beta` | CPA `sdk/api/handlers/gemini/`; NA `relay/gemini_handler.go`; LL `litellm/proxy/google_endpoints/`; BF `integrations/genai.go`; 9R `src/app/api/v1beta/` | 5 |
| An MCP gateway | LL `litellm/proxy/mcp_server/`; BF `handlers/mcp.go`; AB `mcp/`; AG `internal/mcpproxy/`; 9R `src/app/api/mcp/` | 5 |
| Health checks that turn off a broken provider | NA `controller/channel-test.go`; LL `litellm/proxy/health_check.py`; 9G `internal/modelhealth/`; OMP `packages/ai/src/auth/health.ts` | 4 |
| Configuration written for a CLI tool | 9R `src/app/api/cli-tools/`; 9G `internal/clientconfig/`; BF `cli/`; CD (docs `ai-gateway/clients/`, no files written) | 4 |
| Retry rounds and backoff set by the owner | CPA `config.example.yaml` `routing.retry`; LL `litellm/router.py`; BF `docs/features/retries-and-fallbacks.mdx`; OC `src/agents/failover-policy.ts` | 4 |
| A response cache | LL `litellm/caching/`; HC `worker/src/lib/util/cache/`; 9G `internal/promptcache/` | 3 |
| A semantic cache | LL `litellm/caching/redis_semantic_cache.py`; BF `plugins/semanticcache/`; 9G `internal/semanticcache/` | 3 |
| A circuit breaker | AB `circuitbreaker/`; CD `aibridge/circuitbreaker/`; 9G `internal/resilience/` | 3 |
| Body and header rewrite rules | CPA `config.example.yaml` `payload:`; NA (ParamOverride); AG `internal/bodymutator/`, `internal/headermutator/` | 3 |
| Prompt compression | 9R `open-sse/rtk/`; 9G `internal/tokensaver/`; LL `litellm/compression/` | 3 |
| A man-in-the-middle proxy for tools with no base URL setting | CD `enterprise/aibridgeproxyd/`; 9R `dashboard/mitm/`; 9G `internal/mitm/` | 3 |
| A proxy per account | CPA (`proxy-url` per auth); 9R `src/app/api/proxy-pools/`; 9G `internal/proxy/` | 3 |
| A playground | NA `controller/playground.go`; HC `web/pages/playground.tsx`; 9R `dashboard/basic-chat/` | 3 |
| Failures sorted by kind | OC `src/agents/auth-profiles/`, `docs/concepts/model-failover.md`; CPA `sdk/cliproxy/auth/classification.go` | 2 |
| A ping that starts an account's 5-hour window | 9G `internal/schedule/autoping.go`; 9R (a setting) | 2 |
| Accounts ordered by the quota they have left | OMP `packages/ai/src/auth/rank.ts`, `policy.ts` | 1 |
| A credential broker that never gives a client the token | OMP `packages/ai/src/auth-broker/` | 1 |

Notes on rows that are not plain:

- Bifrost's guardrails are in its enterprise docs only (`docs/enterprise/guardrails/`); no code was found.
- omp puts an account whose 5-hour window is nearly used behind accounts with more left, and knows when an account
  draws on paid overage such as Codex credits. Hypomnema keeps a session on its account and rests an account after a
  limit, but does not rank accounts by what is left.
- OpenClaw turns an account off for 10 minutes on a billing or revoked-key error, rests a rate-limited account only
  for the time the provider states, and can rest one model while the account serves the others.
- 9router-go registers its proxy routes at the root (`/messages`), not under `/v1`. Its `tracing/` package keeps
  latency figures in memory; it is not OpenTelemetry.
- Coder lists the clients and shows dashboards in docs and its UI; it does not write configuration files.

## 3. Left out as not fitting a gateway used by one owner (inferred)

Users, teams and SSO; semantic and response caches; prompt compression; guardrails; the man-in-the-middle proxy;
OpenTelemetry; alerts by Slack or e-mail. None was proposed.

## 4. Proposed, and what the owner chose

Proposed order: Anthropic's `/v1/messages` first, because eleven of the twelve have it and it is what Claude Code
needs to use the gateway, while omp and opencode already work over OpenAI's format; then the usage page with
request logs (issue #199, step 4), configuration for opencode, omp and Claude Code, and accounts ordered by the
quota they have left (omp).

The owner chose `/v1/messages` first (owner, 2026-11-13). How the references serve it is studied in the issue that
builds it.
