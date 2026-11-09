# The coordinator, the client API and the web chat

Date: 2026-11-09. Read from the clones in `.tmp/`; paths are relative to each clone. Inferences are marked.

The question: the owner wants to chat with a coordinator from a phone (pi on the server). The owner also wants the
agent on the laptop (Claude Code or Codex) to coordinate workers directly. How do the references let a built-in
coordinator, an outside agent, a web app and a CLI drive one orchestration?

## 1. One API, many clients

Every reference keeps the state in one place and lets every client call the same operations.

| Reference | An outside agent connects through | It learns orchestration from | It waits by | Web and mobile |
|---|---|---|---|---|
| Orca | The `orca orchestration` CLI: `run-create`, `task-create --deps`, `worker-start --on <server>`, `send`, `check --wait`, `ask --wait`, `gate-create`, `gate-resolve`; every mutation takes `--retry-request` (`src/cli/specs/orchestration*.ts`) | A stub `skills/orchestration/SKILL.md` (62 lines) that runs `orca skills get orchestration`. That prints a guide compiled into the binary, so it matches the binary's flags (`skill-guides/orchestration.md`, 114 lines, plus 159 lines of references) | Ending its turn. Orca types a pointer into the agent's terminal when mail arrives (`mailbox-pointer-pty-write.ts`), or `check --wait` blocks, with a keepalive every 15 s | Desktop and mobile apps call the same runtime; mobile goes through a cloud relay (`cloud/README.md`) |
| Omnara | A remote MCP server at `/mcp`. Its tools come from an allowlist of OpenAPI operations (`internal/httpapi/apimcp/manifest.go`), and each call runs in-process through the same handlers and middleware (`server.go:568-590`). A CLI and a REST API with personal tokens also exist | A SKILL.md | Polling: `omnara agents wait` polls `agents get`. The web uses SSE | The dashboard uses the same API through a hey-api SDK |
| OpenClaw | `openclaw mcp serve` (stdio MCP bridged to the gateway WebSocket): `events_wait`, `messages_send`, `permissions_respond`. Also ACP for IDEs and a CLI (`docs/cli/mcp/serve.md`, `docs/cli/acp.md`) | — | `events_wait` | The Control UI and mobile project the same session (`docs/concepts/session-attachment.md`) |
| Omnigent | The user's own Claude Code or Codex runs in tmux, with Omnigent's tools added over MCP (`harnesses/claude_native/bridge.py`). REST with a login token | The agent spec (Polly, `examples/polly/config.yaml`) | The inbox (`sys_read_inbox`) | Web messages are typed into the same session |
| genie | Only a task-ledger CLI; work is delegated through the host agent's own subagents (`skills/work/SKILL.md`) | Skills | The host agent's own waits | A board plugin over the same SQLite file |

In every reference, a run has one coordinator. There is no coordinator of coordinators. In Orca, whichever agent runs
the CLI is the coordinator. Omnara lets people and other agents post input to the same agent, with an `actor`
attribution (`docs/events/sending-input.mdx`).

## 2. Omnara's client API mechanics

- **Spec and code generation:**
  - The spec is hand-written: `api/openapi/openapi.yaml`.
  - oapi-codegen v2 generates models, a std-http strict server and the embedded spec into
    `internal/httpapi/openapi/api.gen.go` (`api/openapi/oapi-codegen.yaml`).
  - The tool is pinned in the tools module and run with `go tool`.
  - `openapi-check` regenerates and diffs, and runs in `verify-static`.
  - `oasdiff breaking` guards compatibility.
- **TypeScript SDK:** `@hey-api/openapi-ts`, with zod and TanStack Query plugins
  (`frontend/packages/sdk/openapi-ts.config.ts`). A freshness check diffs the generated directory.
- **MCP:**
  - Library: `modelcontextprotocol/go-sdk` v1.8.0, streamable HTTP, stateless, JSON responses.
  - Tool names are chosen by hand in a manifest; boot fails on an unknown operationId.
  - Annotations follow the HTTP method: GET is read-only, PUT and DELETE are idempotent.
  - Hidden tools are filtered from `tools/list` by the caller's grants.
  - A fresh `Idempotency-Key` is injected per call.
- **Tokens:**
  - Format: `omnara_{pat|org|daemon|oauth}_v1_` plus 43 base62 characters and a 6-character CRC32 checksum. The
    checksum is checked before the database is read.
  - Stored as a SHA-256 hash.
  - Personal tokens have no scopes; authorization comes from roles.
  - A device grant mints a personal token for a CLI.
  - The web uses `__Host-` session and CSRF cookies.
- **Events:**
  - SSE at `.../events/stream`, resumed with `Last-Event-ID` as the per-agent sequence.
  - `: heartbeat` every 10 s; the SDK treats 35 s of silence as a stall.
  - The stream is not an MCP tool; the paged `listEvents` is.
- **Questions:** `listAgentInteractions` with `include_subagents` and `resolveAgentInteraction`. A parent sees its
  children's questions by aggregation, not forwarding.
- **Idempotency:**
  - An `Idempotency-Key` header on creates.
  - A scoped unique column on each resource table.
  - A mismatch is `idempotency_key_conflict`.

## 3. Running pi as the coordinator

pi 1.5.0, the version of spike A ([pi over RPC](2026-11-06-pi-rpc-brain.md)). Paths are under
`pi/packages/coding-agent/`.

- **RPC** (`docs/rpc.md`, `docs/rpc-commands.md`):
  - Records are JSONL split on LF.
  - `prompt` takes `streamingBehavior: steer|followUp` while a run streams. There are also `steer`, `follow_up`,
    `abort`, `get_state`, `compact` and `set_auto_retry`.
  - A run ends at `agent_settled`, not `agent_end`.
  - Events: `message_update` (deltas; `message_end` is authoritative), `tool_execution_*`, `compaction_*` and
    `auto_retry_*` (`docs/json.md`).
- **Sessions:**
  - JSONL, version 3 (`docs/session-format.md`).
  - `--session-dir <dir> --session-id <id>` opens or creates the session with that id, giving one file per
    conversation (`docs/cli.md:80-109`).
  - Auto-compaction triggers near the context window and keeps the original entries (`docs/compaction.md`).
- **Agent directory:** `PI_CODING_AGENT_DIR` holds `settings.json`, `mcp.json`, `models.json`, `AGENTS.md`,
  `SYSTEM.md`, `APPEND_SYSTEM.md` and `skills/` (`docs/configuration.md`).
- **MCP is built in** (`docs/mcp.md`):
  - Configured in `mcp.json`.
  - Transports: stdio, or streamable HTTP with `headers` such as `Authorization: Bearer ${TOKEN}`.
  - Exposure per server; `direct` puts the tools in the prompt.
  - A 60 s request timeout, which progress notifications reset.
- **Tools:**
  - `--tools 'mcp__hypomnema__*'` leaves only the MCP tools; `--no-tools` would remove MCP tools too.
  - The defaults are `read`, `bash`, `edit` and `write` (`docs/cli.md:113-153`).
- **How Omnigent runs pi** (`omnigent/inner/pi_executor.py`):
  - `--mode rpc --no-session` with a clean environment allowlist and a temporary `PI_CODING_AGENT_DIR`.
  - `agent_settled` is the turn boundary; `steer` and `abort` are used.
  - pi is respawned when it exits or its model changes, and the latest compaction is kept.
  - Its tools are bridged through a generated extension, not MCP.
- **OpenClaw** used pi in-process, then internalized it. It disabled pi's auto-retry because its own retry replayed
  tool calls (`CHANGELOG/2026.5.22.md:29,255`), as spike A found.
- **Wording worth borrowing** from OpenClaw's coordinator prompt (`src/agents/system-prompt.ts:669-672,866`):
  - "Delegate a bounded, independent task only when parallel execution or an independent review provides a
    concrete benefit."
  - "Reserve `sessions_spawn` for delegated work with its own deliverable."
  - Child results must be a result or a concrete blocker (`docs/tools/subagents/announce.md`).

## 4. Orca's coordinator rules

From `skill-guides/orchestration.md` and its references.

- **Each turn:**
  - Start every independent worker, then end the turn. "Report back" still means end the turn.
  - Never poll or run watch scripts.
  - Never make checkouts yourself.
- **Specs:** each spec is self-contained: target, change, constraints, what the worker may edit, and the evidence
  that proves it.
- **Questions:** answer a `question` when the answer follows from the task or the user's instructions; otherwise ask
  the user.
- **Liveness:** silence or a timeout never proves a worker exited. Act only on proof.
- **Retries:**
  - Retry on the same Task.
  - A circuit breaker fails a Task after three failed starts, and must never be routed around.
  - A mutation with an unknown result is replayed with its request id, never blindly repeated.
- **Recovery:** after lost context, read the inbox and the worker list.
- **Packaging:** a stub skill fetches the guide from the binary, so the guide always matches the binary.

## 5. Reaching the server from a laptop and a phone

| Reference | How |
|---|---|
| OpenClaw | Binds to loopback; reached through Tailscale Serve or Funnel, an SSH tunnel, or a tailnet bind (`docs/gateway/remote.md`) |
| Omnigent | Deployed, or a Cloudflare quick tunnel or Tailscale for a laptop server (README 523-553); auth by trusted header (Cloudflare Access), OIDC or built-in accounts (`server/auth.py`) |
| Omnara | Hosted, or self-hosted behind a Cloudflare tunnel, Tailscale Funnel or ngrok (`docs/self-hosting/deployment.mdx`) |
| Orca | A cloud relay that pairs outbound WebSockets, with end-to-end encryption |

aioffice-app (read on 2026-11-09):

- Nginx Proxy Manager in Docker serves public ports 80 and 443 for the production apps there.
- Neither Tailscale nor cloudflared is installed.
