# Serving chat from an Antigravity subscription

Date: 2026-11-10. For issue #20 (the model gateway), step 4: a client sends an OpenAI chat completions request; the
gateway calls Google's Cloud Code backend (`v1internal:streamGenerateContent`) with an Antigravity account's access
token and answers in the chat format. The backend serves Gemini, Claude and gpt-oss models in one Gemini-shaped format.

Read by Sonnet scouts in the clones under `.tmp/`: first 9router-go at commit 64c156d and CLIProxyAPI at c5251b5
(sections 1 to 5), then four more sources (section 6). Paths
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

## 6. Four more sources

Read the same day, after the owner asked whether two references were enough: 9router (JavaScript, the original of
9router-go) at 1fe586b, omp's built-in `google-antigravity` provider at 660a31d, the community plugin
opencode-antigravity-auth (MIT, 11k stars) at 05e600a, and Google's own open-source Gemini CLI at 1930f25, which calls
the same backend. LiteLLM, Bifrost, new-api and opencode itself have no Antigravity subscription path: their
mentions of the name are an agent type of Google's official API, a client's user agent in logs, and a link to the
plugin.

| | 9router-go | CLIProxyAPI | 9router | omp | opencode plugin | Gemini CLI (Google) |
|---|---|---|---|---|---|---|
| Tools renamed, decoys added | yes | no | off: "Antigravity cloaking disabled" (`open-sse/translator/index.js` L169-175) | no | no | |
| `requestType: "agent"` | omitted | sent | omitted (`open-sse/executors/antigravity.js` L335-341) | omitted: "draws false 429" (`docs/provider-quirks.md` L314) | sent | the field does not exist (`code_assist/converter.ts` L33-51) |
| An identity prompt injected | no | no (commented out) | no | no | yes: "You are Antigravity…" for every model (`src/constants.ts` L276-282) | |
| Other agents' names rewritten in the system text | yes | optional word list | yes (`appConstants.js` L187-195) | no | no | |
| A `functionCall` with no signature | default signature, or carried in the call id | sentinel `skip_thought_signature_validator`, and a replay cache | default signature, and a store | the sentinel, on the first call of a turn | the sentinel, on the first call of a turn | the same sentinel, on the first call of a turn (`utils/historyHardening.ts` L12, L227-240) |
| A client that does not stream | `:generateContent` | `:generateContent`, except Claude and Gemini 3 Pro | | always streams | `:generateContent` | both |
| Client version | constant | read from the IDE's update manifest | constant | read from the manifest before each request | read from the manifest at start | its own |
| Chat host | daily | daily | daily | daily, then its sandbox | daily only: "mixing hosts can grant incompatible entitlements" | `cloudcode-pa` |
| Project | first request, prod host | login, daily host | login, prod host | login, daily host; no fallback id | first use; falls back to a fixed or random id | `loadCodeAssist`, `onboardUser`, polled every 5 s (`code_assist/setup.ts`) |

- **The sentinel is Google's own.** The Gemini CLI writes `skip_thought_signature_validator` on the first unsigned
  `functionCall` of a model turn. Three of the others do the same.
- **`loadCodeAssist` and tiers, from Google's client** (`code_assist/setup.ts` L167-359): the request carries
  `metadata{ideType, platform, pluginType}`; the answer has `currentTier`, `allowedTiers` (one is `isDefault`),
  `ineligibleTiers` and `cloudaicompanionProject`. With no current tier the default tier is onboarded, with no project
  for the free tier ("Precondition Failed" otherwise), and the operation is polled until `done`. An ineligible tier
  with `VALIDATION_REQUIRED` carries a `validationUrl` the user must visit.
- **429, from Google's client** (`utils/googleQuotaErrors.ts` L229-483): `QUOTA_EXHAUSTED` is final for the
  day; `RATE_LIMIT_EXCEEDED` is retried after `RetryInfo.retryDelay` unless it is over 5 minutes;
  `MODEL_CAPACITY_EXHAUSTED` is final for that request; a daily quota id is final. omp and the plugin classify the
  same way and add backoffs (omp `ai/src/error/rate-limit.ts` L17-22: quota 30 min, rate 30 s, capacity 45 s).
- **A real project id matters.** 9router: real ids "significantly reduce the risk of being flagged by Google's
  anti-abuse systems" (`open-sse/services/projectId.js` L5-7).
- **Headers.** All send only the IDE's `User-Agent`. 9router and the plugin record that `X-Goog-Api-Client`,
  `Client-Metadata` or `x-goog-user-project` on these calls caused refusals (plugin `CHANGELOG.md` 1.5.0, #410).

## 7. What the vendor says, and what users report

- **Google's terms for the Gemini CLI login**, in the Gemini CLI repository:
  - `docs/resources/tos-privacy.md` L10-14: "Directly accessing the services powering Gemini CLI (for example, the
    Gemini Code Assist service) using third-party software, tools, or services (for example, using OpenClaw with
    Gemini CLI OAuth) is a violation of applicable terms and policies. Such actions may be grounds for suspension or
    termination of your account."
  - `docs/resources/faq.md` L11-18 says the same of Claude Code, OpenClaw and OpenCode, "may be grounds for immediate
    suspension or termination of your account", and names the supported way: a Vertex AI or Google AI Studio API
    key.
- **Antigravity's own terms**, fetched on 2026-11-10 (no date on the page):
  - `https://antigravity.google/terms`, clause 6: "You must not abuse, harm, interfere with, or disrupt the Service.
    This includes, but is not limited to, using the Service in connection with products not provided by us. Using
    third party software, tools, or services to access the Service (e.g. using OpenClaw with Antigravity OAuth) is a
    breach of this Agreement. Such actions may be grounds for suspension or termination of your Antigravity and/or
    Gemini CLI accounts."
  - `https://antigravity.google/docs/faq` repeats it for Claude Code, OpenClaw and OpenCode, recommends "a Gemini
    Enterprise or Google AI Studio API key" for a third-party agent, and says wrapping the Antigravity CLI inside
    another agent or rotating accounts "is not supported under consumer plans".
- **OpenClaw removed it.** Its documentation (`docs/providers/google.md` L87-92, at d622a2b): "OpenClaw does not offer
  new Gemini CLI OAuth or Antigravity OAuth setup. Google ended consumer Gemini CLI Login with Google access on June
  18, 2026, and the Antigravity terms prohibit third-party tools from accessing the service through Antigravity OAuth.
  Use an AI Studio API key or Vertex AI instead." Hermes has only a skill that drives the official Antigravity CLI.
- **Enforcement exists.** The Gemini CLI turns a 403 with reason `TOS_VIOLATION` into an account-suspended error
  with an appeal link (`utils/errors.ts` L212-229). 9router-go parks an account for 24 hours on that answer.
- **The plugin's own warnings** (`docs/TROUBLESHOOTING.md` L206-278): accounts "soft-ban[ned]" or "shadow ban[ned]"
  for long periods once flagged, new accounts limited at once, a tool's request pattern (many calls, retries,
  parallel agents) triggering limits sooner than the IDE. Its advice: low volume, no bursts, one request at a time,
  and an account that was first used in the IDE itself. Its changelog records jitter and a 90 % soft quota cap
  "to prevent Google penalties".
- 9router shows a risk notice before an Antigravity account is connected: "Account may be restricted or banned."
- omp's documents carry no such warning.

## 8. Read for Hypomnema

- More of this adapter is specific to its vendor than Codex's: the schema cleaner, thought signatures, the project,
  per-model limits, a client version that must stay current.
- Where the six differ, the majority and Google's own client point one way: no renamed or decoy tools, no injected
  identity, no `requestType`, the skip sentinel for signatures, always a stream, the daily host, the project found at
  login with no made-up fallback, the client version read from the update manifest.
- Unlike Codex, the vendor's terms name this use as a breach that may end the account, one of the references
  withdrew the feature for that reason, and suspensions are reported by users of the tools that kept it. The decision to build it is the owner's, made with
  that in view; a Google account kept only for this bounds what can be lost.
