# Pages for the model gateway in the web client

Date: 2026-11-12. Question from the owner: the model gateway has no page in the web client; how do the others show
and manage providers, subscription accounts, models, gateway keys and usage, and what should Hypomnema's pages be.

Read in `.tmp/`, in their front-end code: 9router and 9router-go, CLIProxyAPI (its management API only; its panel is
in another repository), new-api, LiteLLM, Bifrost, Helicone, Coder, T3 Code, Orca, OpenMausBot, OpenClaw and Omnara.
Three sub-agents read them first; the files named below were then opened again for this note. "Inferred" marks what
no reference states. "Not verified" marks what was not read to the end.

## 1. What Hypomnema has today

- No page. The web client reads models for the model field of Agents and Bots only.
- The client API lists accounts (`/accounts`), lists and stores providers as YAML (`/providers`), and lists models
  (`/models`). Gateway keys, usage, and an account's sign-in, refresh, removal and quota are commands only
  (`hypomnema accounts …`, `hypomnema gateway models|keys|usage`).
- An account has a label, a runtime, a status (active or disabled), a position and `cooling_until`. A sign-in is
  either a link whose final address is pasted back or a link and a code
  (`internal/controlplane/accounts.go`, `accountsLogin`).
- Quota is read for Antigravity accounts only (`internal/controlplane/subscription/antigravity.go`, `Models`).
- A usage row holds the key, the public model, the provider, the tokens and the outcome; not the account
  (`orchestration/migrations/00019_gateway_usage.sql`).
- A gateway key is kept as its SHA-256, so its value cannot be shown again.

## 2. Where the pages are, and how accounts are shown

| Product | Pages | Accounts | Where |
|---|---|---|---|
| 9router | Providers, a detail page a provider; Combos; Endpoint & Key; Usage; Quota Tracker | Rows in a card on the provider's page: name or e-mail, status, a countdown while it cools, the last error, `#priority`; up and down, an active switch, edit, delete | `9router/src/shared/components/Sidebar.js` L21-39, `src/app/(dashboard)/dashboard/providers/[id]/ConnectionRow.js` L162-231 |
| OpenClaw | Settings, group Agents: Model providers | A searchable list of providers with a status (signed in, expiring, expired, credentials rejected); accounts with kind, source, last used, priority; drag to order; plan limits | `openclaw/ui/src/pages/model-providers/`, `ui/src/i18n/locales/en-settings.ts` L1038-1200 |
| Orca | Settings: AI Provider Accounts | A section a provider, a row an account: e-mail, "Active", a red "Needs re-auth"; re-authenticate, remove | `orca/src/renderer/src/components/settings/accounts/codex/accounts-pane-codex-account-row.tsx` |
| OpenMausBot | Settings: AI | A card a provider group, a row an account: e-mail and organization; sign in, sign out, remove | `openmausbot/src/components/EngineAccounts.tsx` L321 |
| T3 Code | Settings: Agents, Providers; a Usage page | A card a provider instance; "Authenticated as"; a default a driver | `t3code/apps/web/src/components/settings/ProviderSettingsPanel.tsx` |
| Coder | Admin, AI: Spend, Gateway keys, Providers, Models | One credential a provider; a table a page | `coder/site/src/pages/AISettingsPage/` |
| Bifrost | Providers as a list and a detail pane; keys of a provider in a table with a weight and a switch | — | `bifrost/ui/app/workspace/providers/views/modelProviderKeysTableView.tsx` L265-370 |
| LiteLLM, new-api, Helicone | Admin consoles for many users: models and credentials in tabs, channels in a table, providers as cards | — | `litellm/ui/litellm-dashboard/src/`, `new-api/web/src/features/channels/`, `helicone/web/components/providers/` |

The four that manage subscription accounts (9router, OpenClaw, Orca, OpenMausBot) all put an account under its
provider, as a row. Ordering is by up and down (9router) or by dragging (OpenClaw).

## 3. Signing an account in from a page

- 9router opens a popup; on a remote host it asks for the callback address to be pasted, and some providers use a
  device code (`9router/src/shared/components/OAuthModal.js` L35-48, L263-306).
- CLIProxyAPI's management API starts a sign-in (`/anthropic-auth-url`, `/codex-auth-url`, …) and takes a pasted
  callback (`/oauth-callback`) (`CLIProxyAPI/internal/api/server_management.go`).
- Orca and OpenMausBot run the CLI's own login; not applicable to a server.

Hypomnema's two flows are the same two; only an API for the page is missing.

## 4. The limits of a subscription

| Account | How it is read | What comes back | Where |
|---|---|---|---|
| Codex | `GET https://chatgpt.com/backend-api/wham/usage` with the account's bearer token | A primary (5 hours) and a secondary (week) window: percent used and reset time | `9router-go/internal/codexquota/fetch.go`, `wire.go`; `new-api/service/codex_wham_usage.go` L38; `t3code/apps/server/src/usage/cliproxyApi.ts` L343 |
| Codex, without a call | The `x-codex-*` headers of every answer, and a `codex.rate_limits` event | The same windows | `CLIProxyAPI/internal/runtime/executor/helps/codex_quota.go` |
| Claude | `GET https://api.anthropic.com/api/oauth/usage` with the bearer token and `anthropic-beta: oauth-2025-04-20` | `five_hour`, `seven_day` (`utilization` is percent used, `resets_at`), weekly windows a model (`seven_day_opus`, `seven_day_sonnet`, and `limits[]` of kind `weekly_scoped`) | `9router/open-sse/services/usage/claude.js` L62-130; `orca/src/main/rate-limits/providers/claude/claude-oauth-usage-request.ts` L19, L86; `t3code/apps/server/src/usage/cliproxyApi.ts` L287 |

- Neither address is a published API. All three readers drop the reading on an error and go on.
- The Claude address answers 429 when asked often: 9router-go keeps a reading for 60 seconds and does not ask again
  for 3 minutes after a 429 (`9router-go/internal/handlers/dashboard/usage_claude.go` L37-40); 9router skips the call
  while a token cools (`claude.js` L64-68).
- Shown as a bar a window with the percent left and the reset time, a card an account, with a refresh button
  (`9router/src/app/(dashboard)/dashboard/usage/components/ProviderLimits/`); T3 Code says "N% left" and "Resets …"
  (`t3code/apps/web/src/components/usage/UsageLimits.tsx` L118-224); Orca shows them in its status bar.

[2026-11-11-account-reserve.md](2026-11-11-account-reserve.md) asked the same question for the account reserve.

## 5. Models and their routes

- 9router edits a combo in a dialog: the models in a list that is dragged into order, and a strategy, "Fallback, try
  in order" or "Round Robin" (`9router/src/app/(dashboard)/dashboard/combos/page.js` L50-52, L1203, L1313).
- LiteLLM has a form for aliases (`litellm/ui/litellm-dashboard/src/components/model_group_alias_settings.tsx`) and
  sets fallbacks in its router settings. Coder's models are a table with a full-page form.
- new-api maps models a channel in a row editor with a JSON editor beside it
  (`new-api/web/src/features/channels/components/model-mapping-editor.tsx`).
- Hypomnema's Agents page already has a builder and a YAML field for one document
  (`web/src/features/agents/profile-editor.tsx`).

## 6. Gateway keys

| Product | The value | Limits | Where |
|---|---|---|---|
| Coder | Shown once: "Copy this key now … cannot be shown again" | None | `coder/site/src/pages/AISettingsPage/GatewayKeysPage/CreateGatewayKeyDialog.tsx` L93 |
| LiteLLM | Shown after creation in a dialog | Models, budgets | `litellm/ui/litellm-dashboard/src/components/organisms/create_key_button.tsx` |
| 9router, new-api, Bifrost | Can be shown again | Models or combos | `9router/src/app/(dashboard)/dashboard/endpoint/EndpointPageClient.js` L1025-1163; `bifrost/ui/app/workspace/virtual-keys/views/virtualKeysTable.tsx` |

Hypomnema keeps a hash, so only Coder's way fits.

## 7. Usage

- T3 Code: a metric (cost, tokens, limits) and a period (24 hours to 90 days), a chart, totals, a table by model or
  by day (`t3code/apps/web/src/components/usage/UsagePage.tsx`).
- Omnara: a date range, a chart, and a table by model with calls, uncached input, cache read, output, reasoning and
  cost (`omnara/frontend/apps/web/src/components/usage/UsageReport.tsx`).
- 9router: tables by model, by model and account, by key (`9router/src/shared/components/UsageStats.js` L160-191).
- The account that served a request is recorded with it: 9router-go's tracker takes the connection's id
  (`9router-go/internal/usagetracker/tracker.go` L158-167), new-api's log row has `channel_id`
  (`new-api/model/log.go` L325).
- Cost is shown by T3 Code, Omnara, LiteLLM and Bifrost. Hypomnema records no price; not proposed here.

## 8. For Hypomnema

Decided by the owner on 2026-11-12: the pages are in Settings, under a group named "Models". The design is in
issue #199; a prototype built from the app's own stylesheet is in `.tmp/prototypes/gateway/` (git-ignored).

- Three pages: Models (providers, their accounts, the routes), Gateway keys, Usage.
- An account is a row under its provider, with its status, its limits and its order, as 9router and OpenClaw.
- Limits are read with the two calls of section 4, kept for a minute, dropped on an error.
- A route is edited in a form with an ordered list and a YAML field, as 9router's combo and the Agents page.
- A key's value is shown once, as Coder.
- A usage row records its account, as 9router-go and new-api.

Inferred, with no reference: the order the pages are built in; the group's name, which the owner chose because the
app's sidebar already has "Agents".
