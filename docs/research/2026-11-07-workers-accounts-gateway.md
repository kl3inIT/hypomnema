# Workers on many machines, accounts, the model gateway and prompt caching

Date: 2026-11-07. Studied to design the orchestration core (issue #19), the model gateway (#20) and the shared
infrastructure (#21). Clones in `.tmp/`: Orca `4c13ae46` (full clone later the same day), Omnara `0ef1711`, Coder
`b798061`, omp `660a31d`, Codex `f5ac7a7`, opencode `145e948`, OpenHands `91c2e38` and software-agent-sdk `e4160d1`,
actions-runner-controller `89f34c4`, actions/runner `5ba5044`, River `6a1d035`, and the clones listed in the platform
note (OpenClaw, Omnigent, Buzz, genie). Paths are relative to each clone. "Inferred" marks what no source states.

## 1. Workers on many machines

| Reference | Connection | Skills | Memory | Credentials | Pool |
|---|---|---|---|---|---|
| Omnara | Daemon dials out (WebSocket), heartbeat interval set by the server (`internal/machinedaemon/client_socket.go` L178-262) | Versioned archives on the control plane, pushed as offers and pulled with a digest (`internal/skills/broadcaster.go`, `machinedaemon/skillsync/skillsync.go` L86-120) | Stores on the control plane, read and written through tools (`docs/agents/configuration.mdx` L271-287) | Per process; git token fetched at use (`git_credentials.go` L130-170) | 12 providers; provision, inspect, wake, delete (`internal/machinepool/providers/provider.go` L55-112); idle deletion only when no work runs |
| Coder | Agent dials out over a tailnet, "never the reverse" (`docs/coder-agents/architecture.md` L49) | Repository scan plus skills on the control plane | `save_memory` / `read_memory` on the control plane, index first | Manifest on start; git through `GIT_ASKPASS` (`cli/gitaskpass.go`) | Prebuilt workspaces held at a target count by a reconciler (`enterprise/coderd/prebuilds/reconcile.go`) |
| GitHub Actions (ARC) | Runner long-polls out | n/a | none | Per job, in the job message | `min(minRunners + assigned, maxRunners)`; one job per runner (`cmd/ghalistener/scaler/scaler.go` L135-330) |
| OpenClaw | Node dials the gateway (TLS WebSocket) after pairing | Gateway skills plus node-published skills | Markdown on the gateway | Minted per dispatch; model keys stay on the gateway | Leases with idle and total lifetime; warm snapshots per repository |
| Omnigent | Host dials out (WebSocket tunnel) | Sent per session | none found | Codex device-code login on the host, driven by the server | Sandbox per launch; reaper |
| Buzz | `buzz-acp` dials the relay | Sent per deploy | On the relay | In the deploy payload, never stored | none |
| OpenHands | Client calls into the sandbox (the exception) | In repository and home | Files in repository and home | Per conversation | Dormant pods initialised on claim |
| Orca | SSH, then a relay installed on the host | Manual install per host (`docs/cli/skills.mdx` L65) | `CLAUDE.md` / `AGENTS.md` in the repository | Logged in by hand on each host | none |

Conclusions: nodes dial out; SSH only installs a node (even Orca's SSH mode installs a relay; Omnara's installer is
`curl … | sh` plus a one-time token, `docs/machines/connect.mdx`); repository skills come with the worktree, shared
skills and long-lived memory belong to the control plane; credentials are per Dispatch.

Coder runs its agent loop in the control plane and treats the workspace as hands; it added Claude Code and Codex as
ACP sub-agents in the workspace (`docs/coder-agents/acp-subagents.md`), which is Hypomnema's model.

## 2. Choosing the runtime, the model and the account

- Runtime: declared per role in configuration (Omnigent `executor.harness`, `docs/AGENT_YAML_SPEC.md` L56-100) or
  named by the coordinator per dispatch (Orca `worker-start --agent`, `docs/site/content/docs/cli/orchestration.mdx`
  L42-51). No reference routes on task type or quota automatically.
- Model and effort: a default per role, an explicit override per dispatch; an override that cannot be honoured fails
  rather than being swapped (Coder `docs/coder-agents/models.md` L319-356; genie `native-fallback.md` L33). Orca records
  requested and effective values.
- Accounts: Orca keeps one `CLAUDE_CONFIG_DIR` / `CODEX_HOME` per account and leaves refresh to the CLI
  (`src/main/claude-accounts/claude-profile-paths.ts` L48-65); switching is manual. OpenClaw rotates auth profiles with
  cooldowns as long as the provider says (`docs/concepts/model-failover.md` L288-368). omp's broker is the single
  writer of refresh tokens and hands out access tokens (`docs/auth-broker-gateway.md`).
- Claude subscription: since 2026 Anthropic allows Free, Pro and Max OAuth only in Claude Code and Claude.ai; third
  parties may not proxy requests with it. `claude setup-token` gives a one-year token for Claude Code
  (`CLAUDE_CODE_OAUTH_TOKEN`); Omnigent and OpenHands pass it into sandboxes. omp and CLIProxyAPI hold Claude OAuth and
  re-issue requests through their own clients, which is the forbidden pattern.
- Codex subscription: refresh tokens rotate and are not portable (OpenClaw `docs/concepts/oauth.md` L130). Codex's
  app-server accepts tokens from its controller (`account/login/start` type `chatgptAuthTokens`) and asks the controller
  for a new one on 401 (`account/chatgptAuthTokens/refresh`), keeping nothing on disk
  (`codex-rs/app-server-protocol/src/protocol/v2/account.rs` L98-114, `login/src/auth/manager.rs` L1912-1975,
  L2047-2080). It is marked "[UNSTABLE] FOR OPENAI INTERNAL USE ONLY".

## 3. Model gateways

| Gateway | Language, storage | Subscription traffic | Virtual keys, budgets | Use |
|---|---|---|---|---|
| Coder AI Gateway (`aibridge/`, `coderd/aibridged/`) | Go, Postgres | BYOK: the client's Claude or ChatGPT token forwarded; Coder token in a side header (`coderd/aibridged/http.go` L73-116) | Coder tokens, monthly budgets | Main model |
| Bifrost | Go (fasthttp), SQLite or Postgres | Claude OAuth forwarded; `/chatgpt_passthrough` relays bodies unchanged (`transports/bifrost-http/integrations/passthrough.go` L104-130) | Virtual keys, budgets | Reference |
| LiteLLM | Python, Postgres | Forwards, but adds a beta header and parses bodies | Virtual keys, budgets | Reference |
| omp auth gateway | TypeScript, SQLite | Re-issues requests through its own provider code | Client tokens scoped to models and accounts | Account pinning and pools only |
| CLIProxyAPI, 9router | Go, JavaScript | Hold subscription OAuth and disguise requests as Claude Code | none | Not used |
| Portkey, Envoy AI Gateway, Helicone | TS, Go, TS | Translate or too heavy | Hosted or partial | Not used |

The owner builds the gateway; requirements and the spike are in #20.

### Spike, 2026-11-07, on zm (Claude Code 2.1.144, Codex 0.147.0)

- Claude Code through a 150-line `httputil.ReverseProxy` with `CLAUDE_CODE_OAUTH_TOKEN`: works; the cache survives
  (a second run read 28,130 cached tokens; 0.110 → 0.013 USD); usage parsing needs the client's `Accept-Encoding`
  dropped upstream (as Coder does); responses carry `anthropic-ratelimit-unified-{5h,7d}-{utilization,reset}`. Claude
  Code still reaches `api.anthropic.com` directly for other endpoints and sends telemetry to Datadog.
- Codex through the proxy with a custom provider (`requires_openai_auth = true`, `supports_websockets = false`):
  works; bearer and `chatgpt-account-id` pass through; request bodies are zstd-compressed; responses carry
  `x-codex-{primary,secondary}-{used-percent,reset-at}`.
- Codex app-server with external tokens: a deliberately broken token gave 401, Codex asked for a refresh, the
  controller answered, the turn completed, and no `auth.json` was written.

## 4. Prompt caching

1. Pin one upstream account per session; move it only away from a bad account; an Anthropic pin goes cold after
   60 minutes idle (omp `docs/auth-broker-gateway.md` L362-368, `usage/claude.ts` L905-916; OpenClaw
   `model-failover.md` L316-326; Bifrost `core/sessionaffinity.go`).
2. Route on the client's session id: `X-Claude-Code-Session-Id`, Codex `session-id` (Bifrost `schemas/useragents.go`
   L79-96; Codex `core/src/client.rs` L712-734).
3. Never rewrite bodies or cache-relevant headers; Codex treats any change of model, tools, reasoning or cache key as a
   new chain (`client.rs` L430-497).
4. Keep model and reasoning level fixed within a session (OpenClaw `reference/prompt-caching.md` L24-38).
5. Worker prompts: a byte-stable system prompt, per-run facts in the user turn, Claude Code
   `--exclude-dynamic-system-prompt-sections`, tools sorted by name (OpenClaw `prompt-caching.md` L409-485).
6. Compaction without cache writes, trimming from the front (pi `compaction.ts` L626-631; Codex `compact.rs` L367).
7. Cost: 5-minute write 1.25× input, 1-hour write 2×, read 0.1× (pi `models.ts` L1262-1268); measure hit rate per
   session and turn, tagging misses with idle gap, model change or account change (pi `cache-stats.ts`).

## 5. The orchestration core

Read for #19: Orca `src/main/runtime/orchestration/db/` (schema v46, lifecycle transitions, dispatch claim, mailbox
deliveries, mutation receipts, circuit breaker at 3 failures, `MAX_CONCURRENT_DEFAULT = 4`); Omnara leases (90 s,
renewed about every 30 s), reapers and its own outbox tables, no River; Coder `coderd/x/chatd/chatstate` (transition
matrix, `runner_id` fencing, heartbeat stale at 30 s, message queue capped at 20, sub-agents limited to depth 1).
Gaps in Orca that the design closes: dependents of a failed Task stay `pending`; Gate resolution is not guarded; no
reaper compares `last_heartbeat_at`. River's SQLite driver calls itself "early testing" and its 2026 changelog fixes
several data-integrity bugs, so the outbox is a table of our own.
