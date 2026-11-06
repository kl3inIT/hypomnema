# pi as brain over RPC from Go: spike A results (issue #2)

Date: 2026-11-06. Run on zm (Ubuntu 24.04) from a scratch Go program (not in the repository): pi 1.5.0
(`@earendil-works/pi-coding-agent`, Node 22.23), MCP server with `github.com/modelcontextprotocol/go-sdk` v1.8.0
(streamable HTTP on localhost), state in Postgres 18 (`postgres:18-alpine` container) through `pgx/v5` 5.12.0.
Model reached through the owner's 9router on zm (`cx/gpt-6-luna`), with a dedicated 9router key `hypomnema-spike`.

## Setup

- pi gets its own agent directory through `PI_CODING_AGENT_DIR`, so the owner's `~/.pi` is untouched. In it:
  - `mcp.json`: `{"mcpServers":{"hypo":{"url":"http://127.0.0.1:7801","toolExposure":{"create_task":"direct","list_tasks":"direct"}}}}`;
  - `models.json`: a provider with `"api":"openai-completions"`, `baseUrl` of the router, `"apiKey":"$NINEROUTER_API_KEY"`
    (read from the environment at request time, never written to the file).
- Start: `pi --mode rpc --no-session --model <provider>/<model id>`. Send `{"id":…,"type":"prompt","message":…}`;
  read stdout split on LF only (as `rpc.md` requires); the run is over at `agent_settled`.
- MCP tools appear to the model as `mcp__<server>__<tool>` (`mcp__hypo__create_task`).
- **Keep stdin open** while a run is active: closing it starts an orderly shutdown and the run never reports.

## Results

| Phase | What happened | Time |
|---|---|---|
| 1. Create tasks through MCP | pi called `create_task` twice; Go inserted rows 1 and 2; pi reported both ids | 6.1 s |
| 2. Kill pi mid-turn (SIGKILL after 5 s, asked for five tasks) | One task (`Extra 1`) was created before the kill; Postgres kept it; the other four were never started | — |
| 3. New pi process | `list_tasks` returned the three stored tasks; pi reported them correctly | 4.4 s |

- **State survives the brain**: nothing lived in pi; a fresh process continued from Postgres. This confirms the
  "pi decides, Postgres holds state" split.
- **A killed brain leaves partial work**: the control plane, not pi, must know what was done (here 1 of 5) and tell
  the next brain process (recovery note), as in the replay-policy rule in `conventions.md`.
- **Events used**: `response` (prompt `disposition`), `tool_execution_start` / `tool_execution_end` (tool name, args,
  result, `isError`), `message_end` (assistant text, `usage` with input/output/cache tokens), `agent_settled`.
- **Cost is not reported through a router**: `usage.cost.total` was 0 with `totalFromProvider:false`. Cost must be
  tracked where the model is billed (the router or provider), not taken from pi.
- **pi retries on its own**: with an upstream 503 it retried four times before `agent_settled`. The control plane
  should own retries (`set_auto_retry` false) or at least count them.

## 9router on zm (owner's gateway)

- Two instances run on zm: the owner's `9router.service` on `172.22.0.1:20128` (data in `~/.9router`) and another
  project's `zeromail-9router` container on `127.0.0.1:20128`. The owner's instance requires an API key for non-local
  calls; keys are managed at `/api/keys`, which accepts the local CLI token (`x-9r-cli-token`, derived from
  `~/.9router/machine-id` and `auth/cli-secret`, as `src/cli/api/client.js` does).
- 143 models listed. On 2026-11-06 only the `cx/` family (ChatGPT/Codex account) answered; `ag/` returned 403 and
  `ocg/` returned `api_key_expired` from upstream. A node's runtime capabilities must therefore be checked with a real
  call, as the Codex result in spike B already showed.
- Routing a subscription account through a gateway carries the terms-of-service risk noted in the platform research
  (3.1); the owner accepted it for this spike.

## Consequences for the design

- `brain` module: supervise `pi --mode rpc` as a child process; LF-only reader; keep stdin open; correlate responses
  by `id`; end of run at `agent_settled`; restart on exit; disable or count pi's own retries.
- `mcpbridge`: streamable HTTP MCP on localhost, tools with `direct` exposure; every call is checked by Go policy and
  recorded with a request id.
- After a brain crash, the next prompt carries a recovery note built from the event log (what the previous run
  created or started).
- Cost accounting reads the router or provider, not pi.
