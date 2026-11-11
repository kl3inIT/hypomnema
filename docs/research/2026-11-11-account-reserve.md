# Keeping an account that is nearly used up in reserve

Date: 2026-11-11. For issue #172, a follow-on to [several accounts of one provider](2026-11-10-several-accounts.md):
its "an account near its quota, before it fails" row, read closely for the Claude and Codex accounts.

Read in the clones under `.tmp/`: omp (`packages/ai/src/auth/rank.ts`, `select.ts`, `types.ts`, `usage-report.ts`,
`sqlite-credential-store.ts`; `packages/ai/src/usage.ts`, `usage/claude.ts`, `usage/openai-codex.ts`) and
opencode-antigravity-auth (`src/plugin/accounts.ts`, `src/plugin/config/schema.ts`); and Hypomnema's own
`internal/controlplane/modelgateway/accounts.go` and `internal/controlplane/subscription/subscription.go`.

## 1. What each does

| | omp | opencode-antigravity-auth | Hypomnema before #172 |
|---|---|---|---|
| The threshold | 10 % left, one default for every provider, settable per account (`types.ts` `DEFAULT_USAGE_RESERVE_PCT`, `reservePct`) | 90 % used (`schema.ts` `soft_quota_threshold_percent`) | 10 % left, Antigravity only (`accounts.go` `softQuotaLeft`) |
| What an account in reserve gets | Ranked after every account outside the reserve (`rank.ts` L106) | Skipped (`accounts.ts` `isOverSoftQuotaThreshold`) | Tried after the others (`accounts.go` `usedLast`) |
| A session that is on it | Keeps its account unless that account is in reserve and a sibling is measured outside it (`select.ts` L743-760) | No session affinity | Antigravity has no session; Claude and Codex had no reserve |
| Where the number comes from | Claude: `/usage`, and the `anthropic-ratelimit-unified-{5h,7d}-utilization` headers of each answer (`usage/claude.ts` L154-167, L578-581). Codex: `wham/usage`, and the `x-codex-{primary,secondary}-used-percent` headers (`usage/openai-codex.ts` L561-580) | `fetchAvailableModels`, after a successful request | Antigravity: `fetchAvailableModels`. Claude and Codex: none |
| What is left, from several windows | One minus the most used window (`usage-report.ts` `remainingUsageFraction`) | Per model group | Per model |
| How fresh | A report is kept 5 minutes (`sqlite-credential-store.ts` `USAGE_REPORT_TTL_MS`) | Asked at most every 15 minutes, trusted for 30 (`schema.ts`, `computeSoftQuotaCacheTtlMs`) | The plugin's 15 and 30 |
| Not known | Not in reserve: used (`select.ts` L532-535) | Used | Used |

- omp trusts the weekly counter of one model family only once it is used up: short of the cap it "reports high
  utilization while the account can still serve requests" (`usage/claude.ts` L827-833).
- omp's `/usage` for Claude "stays rate-limited for minutes after a 429" (`usage/claude.ts` L26).
- omp also ranks the accounts outside the reserve by how fast each window must drain before it resets
  (`rank.ts` L124-135). Neither the plugin nor Hypomnema's Antigravity rule does.

## 2. Decided (owner, 2026-11-11)

1. An account in reserve comes last, a session's home too, when another account has more: omp. The accounts
   outside the reserve keep the order of issue #168; omp's ranking by drain is left out.
2. The number for Claude and Codex is read from the headers of the answers the gateway already gets, with no call to
   a usage endpoint: omp's header readers. It is trusted for 30 minutes and an account with none is used: the
   plugin's rule, which Antigravity already follows. An account that has not answered lately therefore has no number
   and takes a request before it can be set aside.
3. One threshold, a tenth, for every kind of account: omp's default and the plugin's.
