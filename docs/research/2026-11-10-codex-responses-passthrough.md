# Serving the Codex CLI from a gateway: `/v1/responses`

Date: 2026-11-10. For issue #107: a Codex CLI worker is to use the account logged in on the control plane, through
the model gateway. Read by two Sonnet scouts in the clones under `.tmp/`: the Codex CLI at f5ac7a7, 9router-go at
64c156d, CLIProxyAPI at c5251b5, 9router at 1fe586b, and for how others point Codex at a gateway durable-harness,
Coder, LiteLLM and hermes-agent.

## 1. What the Codex CLI needs

- One wire format. `wire_api` has the single value `responses`; `"chat"` is refused when the config is read
  (`codex-rs/model-provider-info/src/lib.rs` L110-143). So a gateway that only has `/v1/chat/completions` cannot
  serve it.
- A provider is named without touching `~/.codex/config.toml`: `-c model_providers.<id>.base_url=…`, `env_key`,
  `wire_api=responses`, `supports_websockets=false`, `model_provider`, `model`, `model_context_window`, with
  `--ignore-user-config`; its state lives under `CODEX_HOME` (durable-harness, Coder's docs, 9router-go's
  `internal/clientconfig/codex.go`).
- Headless: `codex exec --json`, resumed with `codex exec resume <thread id>`; approvals are never asked.
- It sends `session-id` and `thread-id` headers with a Responses request.

## 2. How three gateways serve it

| | 9router-go | CLIProxyAPI | 9router |
|---|---|---|---|
| Routes | `POST /responses`, `/responses/compact` (`handlers/router.go` L128) | the same under `/v1` and `/backend-api/codex`, and a WebSocket on `GET /v1/responses` | `/v1/responses`, `/v1/responses/compact` |
| The body for a Codex model | Kept, not translated: `stream` on, `store` off, a list of refused fields deleted, `service_tier` `fast` to `priority`, stored items' references and ids stripped, the lite shape, then an allowlist of 14 fields (`proxy/executor/codex_token_params.go`) | Kept, with a deny list; `parallel_tool_calls` and `include` forced | The original of 9router-go's, and more: default instructions, `reasoning.summary`, an effort from a `-high` suffix |
| The client's headers | None passed on; the session is read from `session-id` and its like (`handlerutil/response.go` L217) and sent as `session_id` | The Codex ones passed on (`Thread-Id`, `X-Codex-*`, `Originator`, `Version`, `User-Agent`) | None; the session as 9router-go |
| The answer | The stream copied as it comes (`proxy/sse_copy.go`); usage read from the ending event; null counts in `usage` made 0 | Parsed and framed again, with keep-alives | folded to JSON when the client asked for no stream |
| A refusal | Its status and body; 401 refreshed once; `usage_limit_reached` blocks the account until its reset, the same functions as the chat route | 429 and a cooldown | the same as 9router-go |
| A model of another provider | Translated to chat and back (`responses_bridge.go`) | translated | translated |

## 3. Read for Hypomnema

- 9router-go is the same language and shape as the gateway, and the chat route already follows it for the Codex
  backend, so its passthrough is the one to port: steps 2 to 11 of its chain, without the translation to chat.
- Only 9router-go makes null usage counts 0, only CLIProxyAPI passes Codex's own headers on and has a WebSocket,
  only 9router adds default instructions: each is left out until a Codex CLI shows it is needed.
