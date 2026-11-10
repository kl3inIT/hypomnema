# Serving chat from an Antigravity subscription

Date: 2026-11-10. For issue #20 (the model gateway), step 4: a client sends an OpenAI chat completions request; the
gateway calls Google's Cloud Code backend (`v1internal:streamGenerateContent`) with an Antigravity account's access
token and answers in the chat format. The backend serves Gemini, Claude and gpt-oss models in one Gemini-shaped format.

Read by two Sonnet scouts in the clones under `.tmp/`: 9router-go at commit 64c156d and CLIProxyAPI at c5251b5. Paths
are in those repositories: `tr/` is 9router-go's `internal/translator/`, `chat/` its `internal/handlers/chat/`, `Req`
and `Resp` are CLIProxyAPI's `internal/translator/antigravity/openai/chat-completions/antigravity_openai_request.go`
and `…_response.go`, `Ex/` its `internal/runtime/executor/`. Login and refresh are in
[the subscription note](2026-11-10-subscription-login-and-refresh.md).

## 1. The request

Both build a Gemini body and wrap it in an envelope.

| Chat | Gemini | 9router-go (`tr/gemini.go`) | CLIProxyAPI (`Req`) |
|---|---|---|---|
| `system` | `systemInstruction`, role `user`, a text part each | L292-296 | L150-160; one that comes after the conversation began becomes a user turn |
| `user`, `assistant` text | `contents[]` with role `user` or `model` | L297-320 | L165-241 |
| image as a data URL | `inlineData` | L1065-1082, dropped over 5 MB | L176-197 |
| image as a URL | | `fileData` with a guessed type (L1083-1090) | dropped |
| `assistant.tool_calls` | `functionCall{id, name, args}` | L330-369 | L253-283 |
| `tool` message | `functionResponse{id, name, response}` in a `user` turn | `{result}` or `{output}` (L375-419) | `{result: "<string>"}` (L295-325) |
| `tools` | `tools[0].functionDeclarations`, schemas cleaned | L454-477 | L337-430 |
| `tool_choice` | `toolConfig.functionCallingConfig.mode` | `VALIDATED` by default, `ANY`, `NONE`; a named function is ignored | `AUTO`, `ANY`, `NONE`; a named function gives `allowedFunctionNames`; `VALIDATED` for Claude models |
| `response_format` | `generationConfig.responseMimeType` and a schema | L570-575 | L87-100 |
| `reasoning_effort` | `generationConfig.thinkingConfig` | a level for Gemini 3 and 4, a budget otherwise; none for Claude, whose effort is in the model's id (`thinking/gemini.go`) | a level where the model lists levels, else a budget; Claude's budget kept under the output limit (`internal/thinking/provider/antigravity/apply.go`) |
| `max_tokens` | `maxOutputTokens` | capped at 64000 | kept for Claude, deleted for other models |
| `temperature`, `top_p` | same names | skipped for Gemini 4 | kept |
| `stop` | | dropped | dropped |

- **Envelope** (9router-go `tr/antigravity.go` L44-51, L862-870; CLIProxyAPI `Ex/antigravity_executor_request.go`
  L724-767): `{project, model, userAgent: "antigravity", requestId, request}`. CLIProxyAPI adds
  `requestType: "agent"` and `request.sessionId`. 9router-go leaves `requestType` out: "Google's Cloud Code endpoint
  enforces a tiny separate quota bucket whenever requestType="agent" is present, causing false 429
  RESOURCE_EXHAUSTED" (`tr/antigravity.go` L39-43).
- **Schemas.** Gemini refuses much of JSON Schema; both clean a tool's parameters: `$ref`, `$defs`, `format`,
  `default`, `const`, `additionalProperties`, `pattern…`, `allOf`, `not`, `if`, `x-*` and more are removed or moved
  into the description, unions are flattened, enums become strings, an object with no property gets a placeholder
  one. 9router-go's cleaner is 468 lines (`tr/schema.go`), CLIProxyAPI's 2712 (`internal/util/gemini_schema.go`), with
  a stricter pass for Claude models.
- **Thought signatures.** Gemini 3 refuses a `functionCall` in the history that has no `thoughtSignature`, and a
  chat client does not send one back.
  - CLIProxyAPI writes the sentinel `skip_thought_signature_validator` on every such call (`Req` L282) and, for
    Gemini models, replays the real signatures from a cache keyed by model and session
    (`Ex/antigravity_reasoning_replay.go`, 3084 lines).
  - 9router-go appends the signature to the tool call's id (`<id>__ts__<signature>`), which the client echoes, keeps
    a store as well, and falls back to a hardcoded default signature (`tr/gemini.go` L728-734, L337-345).
- **History repairs** (both): adjacent turns of one role merged, a `functionResponse` forced into a `user` turn
  with its call's id and name, call ids made unique, a first turn that is not the user's given one.
- **Disguise, 9router-go only** (`tr/antigravity.go` L116-204, L220-315): every tool name gets the suffix `_ide`, 21
  decoy tools named after the IDE's own are added, and phrases that identify other agents are rewritten in the
  system text, with comments saying these avoid false 429s. CLIProxyAPI does none of it; it has only an optional
  list of words to obscure. Neither injects a system prompt.

## 2. Headers, address, project

- **Headers**: `Authorization`, `Content-Type`, and a `User-Agent`: `antigravity/cli/1.5.1 (aidev_client;
  os_type=windows; arch=amd64; cl=…; auth_method=consumer)` in 9router-go (`providers/client_identity.go`),
  `antigravity/hub/<version> (aidev_client; os_type=darwin; arch=arm64; cl=…)` in CLIProxyAPI, which reads the
  version from the IDE's update manifest every 3 hours because a stale one is refused for newer models
  (`internal/misc/antigravity_version.go`). No `X-Goog-Api-Client` on a chat call in either.
- **Address**: `daily-cloudcode-pa.googleapis.com` for chat in both, with no fallback to another host.
- **Project.** Every request names a project. `loadCodeAssist` returns it (`cloudaicompanionProject`); when it is
  absent, `onboardUser` with the default tier creates it, polled until done. CLIProxyAPI does this at login and
  after a refresh, against the daily host (`internal/auth/antigravity/auth.go` L249-405). 9router-go does it on the
  first request, against `cloudcode-pa.googleapis.com`, and says the daily host refuses these calls
  (`chat/antigravity_project.go` L20-35).

## 3. The answer

Server-sent events of `response.candidates[0].content.parts` (9router-go `tr/gemini.go` L817-905; CLIProxyAPI `Resp`
L54-256): text becomes `content`, a part marked `thought` becomes `reasoning_content`, a `functionCall` becomes one
whole tool call (its id the backend's or a made one, its arguments complete). `finishReason` `MAX_TOKENS` is
`length` (CLIProxyAPI writes `max_tokens`, which is not OpenAI's word), the safety reasons are `content_filter`, a
tool call seen gives `tool_calls`. Usage: `promptTokenCount`, `candidatesTokenCount` plus `thoughtsTokenCount`,
`cachedContentTokenCount`; 9router-go's stream drops the thought tokens.

A client that does not stream: 9router-go calls `:generateContent`; CLIProxyAPI does so too, except for Claude and
Gemini 3 Pro models, which it always streams and puts together (`Ex/antigravity_executor_execute.go` L57-60).

## 4. Errors and limits

- **401**: neither sends the request again; both refresh ahead of time (CLIProxyAPI 5 minutes before the end).
- **429**: the delay is read from `RetryInfo.retryDelay`, then `ErrorInfo.metadata.quotaResetDelay`, then the
  message. Both cool the model of that account down, not the whole account (9router-go `chat/antigravity_quota.go`;
  CLIProxyAPI `Ex/antigravity_executor_credits.go` L232-288, L637-647), in memory. CLIProxyAPI tells
  `QUOTA_EXHAUSTED` (out until the reset) from `RATE_LIMIT_EXCEEDED` (a short wait).
- **A blocked account.** 9router-go parks an account for 24 hours on a 403 that says the service is "disabled in this
  account for violation" (`providers/errorclassify.go` L115) and can show the appeal link
  (`providers/antigravity_ban.go`). CLIProxyAPI has no such rule: a 403 is a 30-minute cooldown.
- **Load**: 9router-go allows 3 requests in flight per account and opens a breaker after 3 failures
  (`internal/connlimit/connlimit.go`). CLIProxyAPI has no cap by default.

## 5. Models

The two lists differ and both change often: Gemini 4 and 3.x ids whose effort is part of the id
(`gemini-3.8-flash-high`), `claude-sonnet-5-5-high`, `claude-opus-5-5-high`, `gpt-oss-120b-medium`. Both ask the
backend for the account's own list with `fetchAvailableModels` (`{project}`), which also gives each model's remaining
quota and reset time.

## 6. Read for Hypomnema

- More of this adapter is specific to its vendor than Codex's: the schema cleaner, thought signatures, the project,
  per-model limits.
- The two references agree on the translation and disagree on `requestType`, on signatures and on disguise. The
  common ground is CLIProxyAPI's plainer path: no renamed or decoy tools, the skip sentinel for signatures.
- An account can be blocked for this use, and 9router-go carries the code that proves it happens.
