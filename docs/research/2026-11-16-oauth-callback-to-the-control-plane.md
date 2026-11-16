# The browser comes back to the control plane after an OAuth sign-in

Date: 2026-11-16. Question from the owner: now that the control plane has a public HTTPS address, can a sign-in come
back to it by itself instead of the owner pasting the address the browser ends on, with pasting kept as a fallback?
And can the subscription logins (Claude, Codex, Antigravity) do the same?

Read in `.tmp/`: openmausbot, onyx, coder, hermes-agent, openclaw, CLIProxyAPI, 9router, 9router-go, pi, omp, codex,
gemini-cli, omniroute. "Inferred" marks what no reference states.

## 1. Integrations with the owner's own OAuth client

| Reference | Redirect and its base | State, PKCE, owner | What the browser sees | Tab | Paste kept |
|---|---|---|---|---|---|
| openmausbot (MCP) | `{origin}/mcp-oauth/callback`, origin from `OMB_PUBLIC_URL`, else a `127.0.0.1` listener (`server/mcp-oauth.ts` L55, L114-120, L505-528) | in memory 5 min, used once, compared in constant time, `iss` checked, PKCE S256, bound to the user who began (L50, L548, L563, L608-626) | "Signed in. You can close this tab" (L597-605) | new tab (`src/lib/connector-oauth.ts` L16-24) | yes, the whole redirect address (L326-340) |
| onyx (apps) | `{WEB_DOMAIN}/craft/v1/apps/oauth/callback` (`server/features/build/external_apps/oauth.py` L50) | cache key of owner and state, read and deleted, 600 s; another user cannot finish it (`onyx/oauth/authorization_attempt.py` L85-129) | the page tells the opener (BroadcastChannel or `postMessage`) and closes (`web/src/lib/oauth/launch.ts` L111-167) | new tab (L243) | no |
| coder (external auth) | `{AccessURL}/external-auth/{id}/callback` (`coderd/externalauth/externalauth.go` L1070-1099) | state and PKCE in HttpOnly cookies, needs the session (`coderd/httpmw/oauth2.go` L188-252) | redirect to `/external-auth/{id}?redirected=true` | popup, the page polls | no |
| hermes-agent (MCP) | `{HERMES_DASHBOARD_PUBLIC_URL}/api/mcp/oauth/callback/{server}` (`hermes_cli/web_routers/mcp.py` L67-79) | in memory 15 min, at most 8 | "Close this tab", or expired with 404 (L356-368) | system browser | no |

The common shape: one public base URL setting, a callback path under it, a random single-use state checked on the
server and bound to the user who began, PKCE, a small "done" page or a redirect back to the UI, the sign-in opened in
a new tab. openmausbot also keeps pasting the redirect address.

## 2. Subscription logins

| Vendor client | Redirects it accepts | Back to a remote server? | Without pasting |
|---|---|---|---|
| Claude | loopback ports, or the page that shows `code#state` (`pi/packages/ai/src/auth/oauth/anthropic.ts` L18-23) | no reference does it | none |
| Codex | `localhost:1455/auth/callback` only (`codex/codex-rs/login/src/server.rs` L79-81; others refused, `9router-go/internal/handlers/oauth/pkce.go` L75-79) | no | device code (`codex-rs/login/src/device_code_auth.rs` L165-206), used by pi, omp, opencode, omniroute, CLIProxyAPI (`sdk/auth/codex_device.go` L27) |
| Google (Gemini CLI, Antigravity) | loopback ports, or `codeassist.google.com/authcode` (`gemini-cli/packages/core/src/code_assist/oauth2.ts` L441, L536-543) | no | none |

9router's "own /callback" is `http://localhost:{port}/callback`, so it works only with the dashboard on the same machine,
and it falls back to a paste box (`src/shared/components/OAuthModal.js` L74-78, L363-371, L450-469). CLIProxyAPI
forwards to `127.0.0.1` (`auth_files_oauth_callback.go` L67-77); omniroute documents `ssh -L` or pasting for remote
use. Inferred: the vendors' public clients allow nothing else, so these logins keep pasting.

## 3. Hypomnema on 2026-11-16

- Integrations: `redirect = "http://localhost:1"` (`internal/controlplane/integration/integration.go` L45); a pending
  sign-in is kept in memory for 30 minutes and is not bound to the user who began it (L83, L330-463).
- Subscriptions: Claude pastes `code#state` (`subscription/claude.go` L33, L119-135), Codex has the device code from
  the CLI but the web uses the browser flow only (`subscription/codex.go` L105-147, L188-226;
  `web/src/features/models/account-sign-in.tsx`), Antigravity pastes (`subscription/antigravity.go` L48).
- The web session cookie is `SameSite=Lax` (`clientapi/session.go` L21-24), so a top-level redirect from Google carries
  it.
