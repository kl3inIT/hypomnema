# Antigravity's quota groups: reading and showing them

2026-11-16, for [issue #576](https://github.com/kl3inIT/hypomnema/issues/576). Follows
[the adapter research](2026-11-10-antigravity-subscription-adapter.md), whose section 4 covers how a 429 is read.

## What happened

On zm, 2026-11-15 (v0.9.53), the only Antigravity account (`g1-pro-tier`) answered 429 `RESOURCE_EXHAUSTED` for
`gemini-3-flash` at its first call after eight hours without a request, and for `claude-sonnet-4-6` after 18
answers. At 02:19 the next day `gemini-3-flash` still answered 429. The account card said its limits were "not
given", and an omp worker retried for about 15 minutes, since a bare `RESOURCE_EXHAUSTED` made the model wait 30
seconds at a time.

## What the references do

| | Reads | Shows | On a 429 |
|---|---|---|---|
| 9router-go | `v1internal:fetchAvailableModels` (each model's `remainingFraction`, `resetTime`) and `v1internal:retrieveUserQuotaSummary` (`{project}`): groups Gemini, Claude & GPT and Gemini 4 Argon, each with a five-hour and a weekly bucket; cached 3 minutes | the usage page | a used-up window of the group blocks every model in it; three 429s within 60 s while quota reads above zero block the model 15 minutes; a bare `RESOURCE_EXHAUSTED` alone is not taken as a quota error |
| 9router | the same summary | one row per group and window, with a countdown to its reset | the same |
| opencode-antigravity-auth | `fetchAvailableModels`, grouped as claude, gemini-pro and gemini-flash | a quota view | a used-up quota 30 minutes, a rate limit 30 seconds |
| CLIProxyAPI | a quota API common to its providers | its management API | `QUOTA_EXHAUSTED` until the reset, `RATE_LIMIT_EXCEEDED` a short wait |

Read: 9router-go `internal/handlers/chat/antigravity_quota.go` (`IsAntigravityModelBlocked` L175-245,
`HandleAntigravityQuotaError` L448-519, `ParseWeeklyQuotaSummary` L536-670, `FetchAntigravityWeeklyQuota`
L673-745); 9router `src/app/(dashboard)/dashboard/usage/components/ProviderLimits/utils.js` L386-416;
opencode-antigravity-auth `src/plugin/quota.ts` L18, L129-171; CLIProxyAPI
`internal/api/handlers/management/plugin_quota.go`.

How 9router-go reads a bucket: a group is told by its name (argon, claude or gpt, gemini); a bucket is weekly or
a five-hour session by its `window` or its words; a disabled session bucket counts as used up (Google disables it
once the week is), a disabled weekly bucket is skipped, and a bucket with no fraction stays unknown.

## Taken

- The summary is read as the account's limits: `5 hours (Gemini)`, `week (Gemini)`, `5 hours (Claude & GPT)`,
  `week (Claude & GPT)`, and Argon's when given, each with its reset, on the page that shows Claude's and Codex's.
- On a 429, when a window of the model's group is used up, the model waits until that window resets.

Left out: the strike-breaker. `retrieveUserQuotaSummary` is not a published API; when it changes, the limits are
not shown again, and the per-model quota of `fetchAvailableModels` still ranks accounts.
