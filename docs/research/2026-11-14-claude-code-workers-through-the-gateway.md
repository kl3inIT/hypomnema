# Claude Code workers through the model gateway, and the one-hour cache (2026-11-14)

Question: should a Claude Code worker reach Claude through the control plane's model gateway, as omp, opencode and
pi do, instead of with a subscription token sent to its node; and if so, how is the prompt cache kept for an hour,
which is what Claude Code gets when it signs in to a subscription itself. Asked by the owner, who asked for probes
"cho kỹ vì liên quan tới chi phí" (2026-11-13).

## What Claude Code does (its docs, read 2026-11-14)

- [prompt-caching.md](https://code.claude.com/docs/en/prompt-caching.md), *Which TTL each request gets*: "Unless
  you choose a TTL yourself, Claude Code requests the one-hour TTL only on a Claude subscription within your plan's
  included usage", for the main conversation; subagents, workflows and teammates get five minutes. Through a custom
  `ANTHROPIC_BASE_URL` the credential is the gateway's, so every request gets five minutes.
- Same page, *Choose the TTL yourself*: `promptCacheTtl` or `CLAUDE_CODE_PROMPT_CACHE_TTL` (`5m` or `1h`) for the
  main conversation, `subagentPromptCacheTtl` or `CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL` for the rest; v2.1.242 or
  later. "If you sign in with an API key or use a cloud provider, set `promptCacheTtl` to `1h`."
- Same page: through a gateway "whether caching works depends on the gateway"; a gateway that drops the markers
  bills the whole history as uncached input on every turn.
- [llm-gateway-protocol.md](https://code.claude.com/docs/en/llm-gateway-protocol.md): with `ANTHROPIC_BASE_URL`,
  Claude Code posts `/v1/messages?beta=true`; `anthropic-beta` and `anthropic-version` are to be forwarded
  unchanged. [llm-gateway.md](https://code.claude.com/docs/en/llm-gateway.md) and
  [llm-gateway-connect.md](https://code.claude.com/docs/en/llm-gateway-connect.md): `ANTHROPIC_BASE_URL` with
  `ANTHROPIC_AUTH_TOKEN` (or `apiKeyHelper`) is how a client is pointed at a gateway.

## What the references do

| Reference | What it does | Where |
|---|---|---|
| omp | For an OAuth (subscription) token it defaults to the long retention, `ttl: "1h"`, "match[ing] Claude Code's native policy … avoiding cold cache re-writes"; an API key gets five minutes | `.tmp/omp/packages/ai/src/providers/anthropic.ts`, `getCacheControl` |
| CLIProxyAPI | When it serves a Claude subscription with a Claude Code fingerprint, it rewrites every bare cache marker to `1h` (a marker the caller gave a `ttl` keeps it); a subagent keeps five minutes unless it asked for one hour; probes and helpers are left alone | `.tmp/CLIProxyAPI/internal/runtime/executor/claude_executor_execute.go` (around line 190), `claude_executor_cloaking.go`, `upgradeClaudeCacheControlTTLDoc` |
| CLIProxyAPI | Pools setup tokens (`claude setup-token`) with OAuth logins as accounts of one provider; a setup token is known by its metadata and skips the profile call | `claude_executor_auth.go`, `isClaudeSetupToken` |
| 9router-go | Forces `{"type":"ephemeral","ttl":"1h"}` on the last system block and the last cacheable tool | `.tmp/9router-go/internal/translator/request.go`, `AnchorClaudeCache` |

## Probes (server elitex, a throwaway `CLAUDE_CONFIG_DIR`, never the owner's machine)

Claude Code (`claude -p`, Haiku 5.5) through the gateway at its public address with a gateway key:

- Tools, `--resume`, an image, a subagent, thinking and web search all work.
- Without a TTL set, every write is five minutes. After 6 min 40 s idle the next turn wrote 17,846 tokens to the
  cache and read 0.
- With `CLAUDE_CODE_PROMPT_CACHE_TTL=1h`: every write is one hour, the subagent's too in this run (t1 17,231 written;
  t2 411 written, 34,758 read; subagent 602 written, 35,356 read). After an idle longer than five minutes the next
  turn wrote 55 and read 18,244.
- For comparison, a Task run directly under the subscription token on a node (Dispatch
  `dsp_4twkixyp26eaaoqqslkc5gnlta`) wrote with `ephemeral_1h`, as the docs say.

Not probed yet: an account switched in the middle of a session, a 429 from one account, and whether the quota
drawn for the same work differs between a direct run and one through the gateway.

## What this means (inference)

- The one-hour cache needs nothing of the gateway: the client asks for it with `CLAUDE_CODE_PROMPT_CACHE_TTL=1h`,
  and the gateway already forwards the markers and `anthropic-beta` unchanged. Rewriting markers at the gateway
  (CLIProxyAPI, 9router-go) matters only for clients that cannot be configured, such as an outside tool.
- A worker through the gateway is ranked across every Claude account like any other client (issue #319), instead
  of being tied to the one token sent to its node; that is the point of moving it.
