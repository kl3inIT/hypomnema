# Serving chat from a Codex subscription

Date: 2026-11-10. For issue #20 (the model gateway), step 3: a client sends an OpenAI chat completions request; the
gateway calls `https://chatgpt.com/backend-api/codex/responses` with the account's access token and answers in the
chat format.

Read by two Sonnet scouts in the clones under `.tmp/`: 9router-go at commit 64c156d and CLIProxyAPI at c5251b5. Paths
are in those repositories; `executor/` is 9router-go's `internal/proxy/executor/`, `Req` and `Resp` are CLIProxyAPI's
`internal/translator/codex/openai/chat-completions/codex_openai_request.go` and `codex_openai_response.go`, `Ex/` is
its `internal/runtime/executor/`. Login and refresh are in
[the subscription note](2026-11-10-subscription-login-and-refresh.md).

## 1. The request

Both translate the chat body into a Responses body, always with `stream: true` and `store: false`, whatever the
client asked.

| Chat | Responses | 9router-go | CLIProxyAPI |
|---|---|---|---|
| `system`, `developer` messages | | joined into `instructions`; the field is left out when there is none (`executor/transform.go` L332-340) | input messages of role `developer`; `instructions` is always sent, empty (`Req` L47, L190-192) |
| `user` text and images | `message` with `input_text`, `input_image` | L28-76; only `image_url` | L202-267; also files and audio |
| `assistant` text | `message` with `output_text` | L344-375 | L202-230 |
| `assistant.tool_calls` | one top-level `function_call` item each (`call_id`, `name`, `arguments`) | L377-398 | L343-356 |
| `tool` message | `function_call_output` | L401-418; only the first text block | L146-180; text or parts; one with no matching call is dropped |
| `tools` | flattened `{type: function, name, description, parameters, strict}`; `strict` false unless given | L458-536 | L452-481 |
| `tool_choice` | copied; the object form flattened | L541 | L491-515 |
| `response_format` | `text.format` | L451 | L387-402 |
| `reasoning_effort` | `reasoning.effort` | no `reasoning` when absent; `summary: auto` when present (L431-449) | `medium` when absent; no summary (`Req` L60-71) |
| `max_tokens` and its variants | | dropped (`executor/codex_token_params.go` L110-121) | `max_output_tokens`, at least 16 (`Ex/codex_executor_request.go` L73-94) |
| `temperature`, `top_p`, penalties, `seed`, `n`, `user`, `metadata`, `stream_options` | | dropped | not mapped |

- **Allowlist.** 9router-go deletes every key that is not `model`, `input`, `instructions`, `tools`, `tool_choice`,
  `stream`, `store`, `reasoning`, `service_tier`, `include`, `prompt_cache_key`, `client_metadata`, `text` or
  `parallel_tool_calls` (`codex_token_params.go` L13-28): the backend answers 400 to others.
- **`include: ["reasoning.encrypted_content"]`**: always in CLIProxyAPI (`Req` L72); in 9router-go only with an effort.
  Neither replays the encrypted reasoning for a chat client: the summary is streamed as `reasoning_content` and
  the next turn starts without it.
- **Ids and names.** A `call_id` longer than 64 characters is shortened (both). CLIProxyAPI also makes tool names fit
  `[A-Za-z0-9_-]{1,64}` and maps them back in the answer (`Req` L651-807, `Resp` L818-830).
- **No Codex CLI prompt is injected** by either.
- **`gpt-6*` models ("Responses Lite").** The two disagree. 9router-go moves the tools and the instructions into the
  input as developer items, sends `instructions: ""`, `tools: null`, `tool_choice: "auto"`,
  `parallel_tool_calls: false`, a `reasoning.context: "all_turns"`, and the header
  `x-openai-internal-codex-responses-lite: true` (`executor/codex_responses_lite.go` L70-123, `proxy/grokcli.go` L62).
  CLIProxyAPI only forces `parallel_tool_calls: false` for them (`Ex/codex_executor_request.go` L897-899). The owner's
  coordinator uses `gpt-6.1-sol`, so this is settled by a live request.
- **`prompt_cache_key`**: the client's; else 9router-go uses a session header of the client or the account's id,
  CLIProxyAPI a UUID derived from the client's API key (`Ex/codex_executor_request.go` L147-231). Both send the same
  value as the session header.

## 2. Headers

| | 9router-go (`proxy/grokcli.go` L74-112, `providers/client_identity.go`) | CLIProxyAPI (`Ex/codex_executor_request.go` L513-583) |
|---|---|---|
| `Authorization` | `Bearer <access token>` | the same |
| `chatgpt-account-id` | the ChatGPT account id | the same |
| `originator` | `codex_cli_rs` | `codex-tui` |
| `User-Agent` | `codex_cli_rs/0.159.0` | `codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)` |
| `version` | `0.159.0` | only if the client sent it |
| session | `session_id` | `Session-Id` |
| `Accept` | `text/event-stream` for a streaming client only | always `text/event-stream` |

Neither sends `OpenAI-Beta` over HTTP. The versions are constants that need upkeep. CLIProxyAPI has a WebSocket
transport, used only when the client itself is a WebSocket client.

## 3. The answer

Upstream always streams Responses events; both turn each into chat chunks (`executor/stream.go` L37-510;
`Resp` L145-467).

| Event | Chat chunk |
|---|---|
| `response.output_text.delta` | `delta.content` |
| `response.reasoning_summary_text.delta`, `response.reasoning_text.delta` | `delta.reasoning_content` |
| `response.output_item.added` with a `function_call` | a tool call with the next index, its `call_id` and name, empty arguments |
| `response.function_call_arguments.delta` | the arguments' fragment, for the call matched by `item_id` |
| `…arguments.done`, `response.output_item.done` | the arguments, only when no fragment carried them |
| `response.completed` | `finish_reason` (`tool_calls` when a call was seen, else `stop`) and `usage` |
| `response.incomplete` | CLIProxyAPI: `length` or `content_filter`. 9router-go does not handle it and ends with `stop` |
| `response.failed`, `error` | no chunk: an error (below) |

- Usage: `input_tokens`, `output_tokens`, `input_tokens_details.cached_tokens` and
  `output_tokens_details.reasoning_tokens` become the chat names.
- **A client that did not ask for a stream**: both read the whole stream and build one `chat.completion`
  (`executor/codebuddy.go` `sseToOpenAIJSON`; `Resp` L482-688). CLIProxyAPI rebuilds the output from the
  `output_item.done` events, since `response.completed` can arrive with an empty `output`
  (`Ex/codex_executor_terminal.go` L96-145).

## 4. Errors and limits

- **401**: one refresh, one retry (9router-go `handlers/chat/fallback.go` L1096-1139; CLIProxyAPI
  `sdk/cliproxy/auth/conductor_refresh.go` L1126-1162).
- **Usage limit**: a 429 whose error type is `usage_limit_reached`, with `resets_at` (Unix seconds) or
  `resets_in_seconds`. Both keep the account out until then (9router-go `handlers/chat/codex_quota_error.go` L18-82;
  CLIProxyAPI `Ex/codex_executor_terminal.go` L336-357, L538-559). CLIProxyAPI also treats "model is at capacity" as a
  429 that passes.
- **Other statuses**: 9router-go passes the status and the body on; CLIProxyAPI reshapes some into OpenAI's error object.
- **A failure inside a stream** (`response.failed`, `error`): before anything was sent, an error with a status
  guessed from the error's type; after, an error frame and the end of the stream. A stream that ends with no terminal
  event is an error in both.
- Neither gates requests on the `x-codex-*` headers; CLIProxyAPI reads them for its quota display.

## 5. Read for Hypomnema

- The chat format stays the gateway's one format; this adapter translates to and from Responses, as both references
  do. The adapter needs both directions, where the OpenAI-compatible one only builds a request.
- Where the references agree, follow them. Where they differ (instructions, `gpt-6*`, `max_tokens`, effort default,
  identity headers), start from 9router-go: the owner's coordinator already works through 9router, the JavaScript original
  it was ported from (whose code was not read for this note). Settle each with a live request before the
  coordinator is moved.
- `response.incomplete` and tool-name mapping follow CLIProxyAPI, which handles what 9router-go leaves out.
