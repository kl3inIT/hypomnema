# Several accounts of one provider in a gateway

Date: 2026-11-10. For issue #20 (the model gateway), §6: the owner wants what the tools that share subscriptions
have for several accounts of one kind: choosing among them, moving on when one is limited, sparing an account near
its quota, and spacing requests.

Read by two Sonnet scouts in the clones under `.tmp/`: 9router-go at 64c156d and 9router at 1fe586b;
opencode-antigravity-auth at 05e600a, CLIProxyAPI at c5251b5 and omp at 660a31d. Paths are in those repositories.

## 1. What each does

| | 9router-go | 9router | CLIProxyAPI | opencode-antigravity-auth | omp |
|---|---|---|---|---|---|
| Order by default | Priority, first one until it fails ("fill-first"); round-robin and random can be set per provider (`handlers/chat/connections.go` L189, L1292) | fill-first; round-robin with a sticky limit of 3 (`src/sse/services/auth.js` L146) | round-robin within the highest priority (`sdk/cliproxy/auth/selector.go` L623) | `hybrid`: a score of health, a token bucket and time since last use (`src/plugin/rotation.ts` L231-319) | by a hash of the session, else round-robin; ranked by usage where it is known (`packages/ai/src/auth/select.ts`) |
| A session stays on its account | Only with round-robin: by the client's session header, 2 h, dropped on 429, 401, 403 (`handlers/chat/session_affinity.go`) | no | optional, off by default, 1 h | per model family, not per session | yes, kept 30 days |
| Next account in the same request | On 401, 402, 403, 429, 502, 503, 504, each account once, nothing after bytes were sent (`handlers/chat/fallback.go` L200-355) | the same | each credential once per round, more rounds on request | after a wait of 1 s then 5 s | a sibling credential, for quota errors only |
| Cooldown after a failure | 429 and overload: 2 s doubling to 5 min; quota used up: 1 h; 401 to 404: 2 min; other 5xx: 30 s; a success clears it; a reset the provider names wins, capped at 2 h (`providers/errorclassify.go`) | the same numbers; provider resets capped at 30 min | 429: 1 s doubling to 30 min, at least 10 s with `Retry-After`; auth: 30 min; 5xx: 60 s (`conductor_cooldown.go`) | quota: 60 s, 5 min, 30 min, 2 h; rate limit 30 s; capacity 45 s; server 20 s (`src/plugin/accounts.ts` L29-36) | quota 30 min; rate limit 30 s; capacity 45 s; server 20 s (`error/rate-limit.ts`, "ported from opencode-antigravity-auth") |
| An account near its quota, before it fails | No: only one already at zero is skipped, read after a failure | no | no | Yes: skipped at 90 % used, from `fetchAvailableModels`, per model group, refreshed after a success at most every 15 min, trusted for 30 min, allowed when unknown (`accounts.ts` L254-295) | Yes: ranked behind the others within a reserve of 10 % (`auth/types.ts` L25) |
| Spacing between requests | None; a token refresh waits 1.5 s between accounts, 12 s for Antigravity (`proxy/oauth/background.go`) | the same refresh spacing | none; jitter only on a cooldown's wait | `request_jitter_max_ms`, a random wait before every request, **off by default** (`config/schema.ts` L371) | none |
| Requests in flight | 3 per Antigravity account, a breaker after 3 failures for 30 s (`connlimit/connlimit.go`) | none | none by default | none | none |
| Every account unavailable | The last error, or 502 | 503 with `retry-after` to the earliest reset (`src/sse/handlers/chat.js` L283-289) | 429 with `Retry-After` (`selector.go` L84-130) | waits up to 300 s, else fails | the caller backs off |

- 9router-go's reason for keeping a session on an account: the vendor's prompt cache is per account, and changing
  account each turn "dropped cache hits from over 90% to under 35%"
  (`docs/superpowers/specs/2026-10-28-session-affinity-sticky-roundrobin-design.md`).
- The plugin's reason for the 90 % rule: "to prevent Google penalties" (`CHANGELOG.md` L197); for jitter: "to reduce
  detection patterns" (L250).

## 2. Read for Hypomnema

- Three of five take accounts in a fixed order until one fails, then the next: what the gateway does today with one
  account, extended to the others.
- Moving on inside a request and a cooldown per kind of failure are in all five; 9router-go's numbers are the closest
  fit (Go, the same shape) and its original has the same.
- Sparing an account near its quota is in two (the plugin's 90 % used is omp's 10 % reserve); spacing requests is in
  one, off by default.
