# The model gateway's prompt cache, measured per client (2026-11-15)

What was read and measured while fixing the gateway's Anthropic cache breakpoints (issues #504, #520, #524; PRs
#509, #521, #525). The measurements were taken on zm against the running control plane, through temporary gateway
keys revoked afterwards, and read from `gateway_usage` (prompt, cached, cache-write tokens per request).

## Sources read

| Reference | Revision | Files |
|---|---|---|
| CLIProxyAPI | e96d4c48 (2026-11-15) | `internal/translator/claude/openai/chat-completions/claude_openai_request.go` L172-207 (system and developer messages, their `cache_control`); `internal/runtime/executor/claude_executor_cloaking.go` (`enforceCacheControlLimitDoc`, `materializeTopLevelCacheControlDoc`, `shouldEnsureCacheControlDoc`, `buildForwardedSystemBlock`, `insertClaudeMidConversationSystemBlocksDoc`); `claude_executor_execute.go` L160-207 (order: place, limit, upgrade ttl, normalize) |
| omp | 57263720c8 (2026-11-14) | `packages/ai/src/providers/anthropic.ts` (`applyHeadCaching` L4590-4615, `applyPromptCaching` L4357-4520, `buildAnthropicSystemBlocks`); `providers/openai-chat-server.ts` L95-125; `providers/anthropic-messages-server.ts` L275-313; `auth-gateway/server.ts` |
| pi | 827383a15 (2026-11-13) | `packages/ai/src/api/openai-completions.ts` L1068-1100 (`getCompatCacheControl`, `applyAnthropicCacheControl`), L1226 (developer role), L1620-1650 (compat detection) |
| opencode | 6205e99c (2026-11-13) | `packages/ai/src/cache-policy.ts` (auto: last tool, first and last system parts, tail; cap 4); `packages/ai/src/protocols/openai-chat.ts` L636-656 (system parts joined unless the provider sets `cacheControl`) |
| Anthropic docs | read 2026-11-15 | Prompt caching: "up to 4 cache breakpoints"; a top-level `cache_control` takes one of them; prefixes are tools, system, messages; a 1h entry must come before a 5m one; at most 20 positions checked back per breakpoint |

## What the references do

| | System / developer on the chat route | Client breakpoints | Limit of four | Where the system breakpoint goes |
|---|---|---|---|---|
| CLIProxyAPI | both become system blocks, `cache_control` kept | kept, its own added when the client is not Claude Code | `enforceCacheControlLimitDoc`, a top-level one counted | on each forwarded client block, unless the client chose explicit mode; for newer models the client's system goes in as `role: system` messages |
| omp gateway | system joined into a string; developer kept as its own message | dropped; omp places its own | by construction: messages get what system and tools leave | moved off the identity block onto the last stable system block ("caches only tools + identity") |
| pi (client) | sends its system prompt as `developer` to a reasoning model | marks only with `cacheControlFormat: anthropic`, which Hypomnema's models.json does not set | three at most | — |
| opencode (client) | parts joined into one string on `openai-compatible`; separate parts with marks on `anthropic-compatible` | its own, first and last system parts | four | — |

## Measured on zm

Before the fixes (v0.9.43, 2026-11-15), a ~7k-token system no one marked, three new conversations, then a second
turn of the third:

| Route, provider | New conversations 2 and 3 read | Second turn read |
|---|---|---|
| chat, Claude subscription | 0 | 6,885 |
| chat, Antigravity | 4,408 | 4,408 |
| chat, Codex | 4,224 | 4,224 |
| Responses, Codex | 4,224 | 4,224 |
| Messages, Claude, a client that is not Claude Code | 0 | **0** |
| Messages, Antigravity | 4,408 | 4,408 |

Also before: a chat request whose client marked four messages got `400: A maximum of 4 blocks with cache_control may
be provided. Found 7.`; pi 1.5.2's real request carried its 6,932-character system prompt as `developer` with no mark.

After (v0.9.45 for the chat route, v0.9.48 for Messages): Claude on the chat route and on Messages reads 6,852 of
~6,885 from the second new conversation on, and 6,885 on a second turn; the seven-breakpoint request is answered.

Real clients through the gateway, configured as a node configures them (`internal/node/acp/agents.go`,
`internal/node/claude/runner.go`, `internal/node/codex/runner.go`), three new sessions each:

| Client → model | Main request | New sessions read | Why |
|---|---|---|---|
| Claude Code 2.1.322 → Claude | ~16,140 | 10,763 (67%) | its own three breakpoints; the rest is its per-session part (inferred: directory, date, git state) |
| Codex 0.167.0 → Codex | ~10,740 | 9,088 (85%) | sends its own `prompt_cache_key` |
| pi 1.5.2 → Claude, same directory | ~4,885 | 4,871 (99.7%) | a Bot's chats share the Bot's directory |
| pi 1.5.2 → Claude, a directory per session | ~4,890 | 2,297 (47%) | pi's system names the working directory |
| pi 1.5.2 → Codex, same directory | ~2,095 | 1,920 (92%) | |
| omp 18.18.6 → Claude | ~17,780 | 17,687 (99%) | its system is identical across directories |
| omp 18.18.6 → Codex | ~9,180 | 8,960, once 0 | |
| opencode v2.2.7 → Claude | ~12,880 | 5,909 (46%) | one system string of 19,853 characters with the directory at character 19,310 |
| opencode v2.2.7 → Codex | ~7,040 | 2,176 (31%) | not explained yet |

Codex alone, 40 requests: 35 read from the cache; the five misses were the first one or two after a new prefix, then
steady. A long conversation (~19.7k tokens) followed by a step adding 50 blocks (25 tool calls and results) still read
the whole history (19,737), so a fourth breakpoint was not added.

## What was decided and done

- System and developer messages become system blocks keeping their `cache_control`; four breakpoints at most on the
  chat route and the Messages passthrough, in CLIProxyAPI's order (#504).
- The gateway's system breakpoint is on the last block of the client's own system, not on Claude Code's identity
  line, as omp moves it (#520).
- A disguised Messages request (a client that is not Claude Code) gets the chat route's breakpoints, a top-level
  `cache_control` moved onto the last message first, as CLIProxyAPI places its own on a request it disguises (#524).

## Left open

- opencode: on Claude, its `anthropic-compatible` provider would send its system parts separately with its own marks;
  on Codex, the 31% is not explained. Deferred by the owner (2026-11-15).
- CLIProxyAPI's newer placement of a client's system as `role: system` messages changes the disguise, not the cache;
  not taken up.
