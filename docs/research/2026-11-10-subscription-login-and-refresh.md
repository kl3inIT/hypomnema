# Subscription accounts: login and token upkeep

Date: 2026-11-10. For issue #20 (the model gateway), step 2: the owner adds a Codex, Antigravity or Claude
subscription once; the control plane keeps the tokens in the vault and refreshes them.

Read by three Sonnet scouts in the clones under `.tmp/`: 9router-go and 9router, CLIProxyAPI, and the CLIs (codex,
omp, pi, opencode) with OpenHands, Orca and Omnigent. Paths are in those repositories. Upstream URLs and request
shaping are in [the gateway routing note](2026-11-10-model-gateway-routing.md) §3; this note covers only login and
the life of a token.

## 1. Logging in when the server has no browser

Every vendor's browser login returns to a loopback address, which is the laptop's, not the server's.

| | Codex | Claude | Antigravity |
|---|---|---|---|
| codex CLI | `codex login --device-auth`: a link and a one-time code, 15 minutes (`codex-rs/login/src/device_code_auth.rs` L68-206); falls back to the browser when the server answers 404 | | |
| pi | "Device code login (headless)" (`packages/ai/src/auth/oauth/openai-codex.ts` L419) | "Copy code login (headless)": redirect `https://platform.claude.com/oauth/code/callback`, the owner pastes `code#state` (`anthropic/anthropic.ts` L205-232) | none |
| omp | device code (`packages/ai/src/registry/oauth/openai-codex.ts` L208-275), or `auth-broker login --via` (an SSH forward of the port) | loopback 54545; "paste the final redirect URL or authorization code" (`anthropic.kdl` L6-34, `callback-server.ts` L665-693) | loopback 51121, SSH forward (`google-antigravity.kdl` L14) |
| opencode | device code (`plugin/provider/openai.ts` L177-227) | uses Claude Code's own login on that host | none |
| Orca | runs `codex login --device-auth` | runs `claude auth login`, the owner pastes the code (`src/main/claude-accounts/claude-pasted-code-login.ts`) | none |
| CLIProxyAPI | device code (`sdk/auth/codex_device.go`); or paste the callback URL | paste the callback URL | paste the callback URL |
| 9router-go | paste the callback URL (its listener on 1455 is on the server and never sees a remote browser) | redirect to the dashboard's own `/callback`, or paste | paste the callback URL |

- **Device code (Codex only).** `POST auth.openai.com/api/accounts/deviceauth/usercode` with the client id returns
  `device_auth_id`, `user_code`, `interval`; the owner opens `auth.openai.com/codex/device` and types the code; the
  caller polls `…/deviceauth/token` (403 and 404 mean "not yet") and exchanges the returned code with redirect
  `auth.openai.com/deviceauth/callback`. Five of the seven references that serve Codex offer it. The server can
  answer 404 ("not enabled"); what switches it on is not in the code.
- **Paste the callback URL.** CLIProxyAPI's management API is the fullest version
  (`internal/api/handlers/management/oauth_callback.go` L42-125, `oauth_sessions.go`): the server makes the PKCE
  verifier and the state, keeps the pending login in memory for 30 minutes, returns the URL; the browser ends on a
  page that cannot load; the owner copies the address and posts it; the server checks the state and the provider
  and exchanges the code. 9router-go keeps the verifier in the browser and does not check the state on the server.
- No reference imports `~/.codex/auth.json` or `~/.claude/.credentials.json` into a gateway. CLIProxyAPI accepts an
  upload of its own file format.

## 2. Parameters

The client ids are the vendors' own CLIs', public constants in both gateways.

| | Codex | Claude | Antigravity |
|---|---|---|---|
| Authorize | `auth.openai.com/oauth/authorize` | `claude.ai/oauth/authorize`, `code=true` | `accounts.google.com/o/oauth2/v2/auth`, `access_type=offline`, `prompt=consent` |
| Token | `auth.openai.com/oauth/token`, form | JSON body; 9router-go posts to `api.anthropic.com/v1/oauth/token`, CLIProxyAPI to `platform.claude.com/v1/oauth/token` | `oauth2.googleapis.com/token`, form, with the installed-app client secret |
| PKCE | S256 | S256 | none |
| Redirect | `http://localhost:1455/auth/callback`, the only one accepted | `http://localhost:54545/callback` (CLIProxyAPI, omp); pi's code page above | `http://localhost:51121/oauth-callback` |
| Scopes | `openid profile email offline_access` | `org:create_api_key user:profile user:inference`; CLIProxyAPI adds `user:sessions:claude_code user:mcp_servers user:file_upload` | `cloud-platform`, `userinfo.email`, `userinfo.profile`, `cclog`, `experimentsandconfigs`; CLIProxyAPI adds `aicode` |

Sources: 9router-go `internal/handlers/oauth/pkce.go` L49-82, `antigravity.go` L19-35; CLIProxyAPI
`internal/auth/codex/openai_auth.go` L25-95, `internal/auth/claude/anthropic_auth.go` L23-39 and L332-341,
`internal/auth/antigravity/constants.go`.

## 3. What is kept for an account

| | Codex | Claude | Antigravity |
|---|---|---|---|
| Tokens | access, refresh, id token | access, refresh | access, refresh |
| Identity | email, `chatgpt_account_id`, plan, read from the id token's claims without checking its signature (both) | email, account and organization ids from the token answer and `api/oauth/profile` (CLIProxyAPI); 9router-go keeps none | email from Google's userinfo (CLIProxyAPI, 9router); project id from `loadCodeAssist`, then `onboardUser` |
| Login fails when | no account id (9router-go), no email (CLIProxyAPI) | no email (CLIProxyAPI) | no email or no project (CLIProxyAPI's CLI); 9router-go finds the project on the first request |
| Same account again | CLIProxyAPI and 9router replace it, keyed on account id and email; 9router-go adds a row each time | CLIProxyAPI replaces, keyed on organization or account id and email | replaced, keyed on email (all) |

Neither gateway checks an account with a real request at login. CLIProxyAPI does so only on demand (`VerifyInference`).

## 4. Refresh

| | Codex | Claude | Antigravity |
|---|---|---|---|
| Request | form: `grant_type=refresh_token`, client id, refresh token (CLIProxyAPI adds `scope`) | JSON: the same three | form, with the client secret |
| Refresh token rotates | yes, single use; reusing an old one revokes the login | yes | no |
| Refreshed ahead by | 9router-go 10 min on a request, 30 min in the background; CLIProxyAPI 96 h (the access token lives about 10 days) | 4 h (both) | 5 min and 30 min (9router-go); 30 min (CLIProxyAPI) |

- **When.** Both refresh in the background and before a request: 9router-go every 5 minutes for what expires within
  30 (`proxy/oauth/background.go` L17-24); CLIProxyAPI with a timer per account (`sdk/cliproxy/auth/auto_refresh_loop.go`)
  and again after a 401 from the provider.
- **Login required.** 9router-go: 401, or 400 naming `invalid_grant`, `invalid_request`, `refresh_token_reused` or
  `unrecoverable_refresh_error` (`providers/oauth_status.go` L50-65); the account is marked `reauth_required` and
  left alone until the owner logs in again. CLIProxyAPI's list is longer (`refresh_token_expired`,
  `refresh_token_invalidated`, `token_revoked`, `invalid_client`); it keeps serving while the access token is still
  valid (`conductor_refresh.go` L1430-1565).
- **Anything else is transient**: the token is kept and the refresh tried again later, 5 minutes doubling to 24 hours
  in 9router-go (`db/accounts_oauth_lock.go`).
- **An answer that never arrived.** For a rotating token, CLIProxyAPI does not send the same refresh token twice: a
  journal on disk records the attempt before it is sent (`internal/auth/codex/refresh_journal*.go`), and Claude's
  refresh is not retried after a transport error. The vault's `Refresh` already holds this rule (issue #66).

## 5. Read for Hypomnema

- The server has no browser and the owner works over SSH or, later, the web client: device code for Codex and
  "paste the callback URL" for all three cover both, with the pending login held by the control plane as
  CLIProxyAPI does.
- One secret of kind `oauth_token_set` per account; identity and plan are not secret and belong on the account.
- A repeated login replaces the account's tokens, keyed on the vendor's own id, as CLIProxyAPI and 9router do.
