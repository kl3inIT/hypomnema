# Microsoft 365 (OneDrive and SharePoint) as an Integration (2026-11-17)

Asked by the owner on 2026-11-17: BeyondPilot's Bot must export the Nestlé challenge's applications (folders per use
case, stage and solution, with details and decks) every day into a SharePoint folder that Nestlé shared with
`team@beyondpilot.ai` (`https://nestle.sharepoint.com/teams/GenAI-VietnamCommunity-Taskforce/Shared%20Documents/...`;
the invitation came by email). The same export goes to Google Drive today through the Google Integration. Hypomnema
has no Microsoft Integration (`integrations list` on zm: github, gitlab, google, lark, superset, aws, whatsapp).
Builds on [connectors](2026-11-10-connectors.md), [service proxy and approvals](2026-11-11-service-proxy-and-approvals.md)
and [remote MCP and the catalog](2026-11-16-remote-mcp-and-integrations-catalog.md).

Read in `.tmp/`: rclone `b0ef5f6` (sparse: `backend/onedrive`, `docs/content`), softeria/ms-365-mcp-server `29ebda2`,
pnp/cli-microsoft365 `945ca0d`, Onyx `da837e6`, Hermes `dc965932a0`, OpenClaw `7b3c6edf5`. Microsoft's pages on
learn.microsoft.com were fetched on 2026-11-17 (dates in the list at the end). Two public, unauthenticated reads were
made: Nestlé's OpenID metadata and the realm of `team@beyondpilot.ai`. Nothing was signed in.

## How the references sign in to Microsoft 365

| Project | Who the token is | App registration | Scopes | Refresh |
|---|---|---|---|---|
| rclone (OneDrive backend, MIT) | A user (delegated), or the app (client credentials) | Its own published multi-tenant client (`backend/onedrive/onedrive.go:48`), or the user's own: "Accounts in any organizational directory … and personal Microsoft accounts", Web redirect `http://localhost:53682/`, a client secret (`docs/content/onedrive.md:150-170`). For a business tenant that refuses an unverified publisher, a single-tenant app with `auth_url`/`token_url` on the tenant (`onedrive.md:183-201`) | `Files.Read Files.ReadWrite Files.Read.All Files.ReadWrite.All Sites.Read.All offline_access` (`onedrive.go:85`), or without `Sites.Read.All` when the tenant did not grant it (`onedrive.go:86`, `onedrive.md:179-181`); app-only `.default` (`onedrive.go:90`) | OAuth refresh token kept in its config; a second token per SharePoint host from the same credentials (`onedrive.go:1201-1229`) |
| ms-365-mcp-server (MIT) | A user (device code by default, `README.md:477`), or the app (`--app-mode`), or on-behalf-of | Its own published client `084a3e9f-…` on authority `common` (`src/cloud-config.ts:57-59`, `src/auth.ts:67-68`); one's own client with `MS365_MCP_CLIENT_ID` and a tenant | Per tool from `src/endpoints.json` (`scopes`, `workScopes`, `appScopes`); upload needs `Files.ReadWrite` or `Sites.Selected` delegated, `Files.ReadWrite.All`/`Sites.ReadWrite.All`/`Sites.Selected` as the app (`endpoints.json:770-776`); `Sites.Selected` can replace the broad `Sites.*.All` for a granted site (`README.md:190-193`) | MSAL token cache |
| CLI for Microsoft 365 (`m365`, MIT) | A user (device code default) or the app (certificate, secret) | One's own Entra app: `appId` and `tenant` are required at login; `m365 setup` creates it (`docs/docs/user-guide/connecting-microsoft-365.mdx:27-39`). Its old shared app is gone | Whatever the app was granted | MSAL cache |
| Onyx (connectors, MIT outside `ee/`) | The app only | The customer registers it in their tenant; client secret or certificate (`backend/onyx/connectors/microsoft_credential.py:27-38`, `sharepoint/connector.py:277-282`) | `Sites.Read.All` application (`sharepoint/connector.py:2111`) | Client credentials, no refresh token |
| Hermes (`tools/microsoft_graph_auth.py`) | The app only | Tenant, client id and secret from the environment (`:18`, `:45-56`) | `https://graph.microsoft.com/.default` (`:14`) | Client credentials |
| OpenClaw (`extensions/msteams`) | The Teams bot's app | The bot's own registration | `Sites.Read.All` named for files (`src/channel.ts:89`) | Bot Framework |

They agree on two shapes. A server reading **its own organization's** data uses the app's own permissions,
consented by that tenant's admin (Onyx, Hermes, rclone and ms-365-mcp-server's app mode). A tool acting **as one
person** uses delegated scopes with `offline_access` and keeps the refresh token (rclone, ms-365-mcp-server,
`m365`). Only the delegated shape fits here: we are not Nestlé's admin. The mature ones that publish their own
multi-tenant client (rclone, ms-365-mcp-server) also tell the user to bring their own app when a tenant refuses an
unverified publisher (rclone `onedrive.md:183-188`); `m365` dropped its shared app and now requires one's own.

## How the agent calls it

| Project | Interface | Large files | A shared link | Folders |
|---|---|---|---|---|
| rclone | Its own Graph client behind `rclone copy/sync` | Upload session, chunks; the upload URL on `*.sharepoint.com` now needs a SharePoint token, as Microsoft retires pre-authenticated (`tempauth`) URLs (`onedrive.go:1096`, `:1151-1163`, `:1201-1240`, `:2834-3030`) | `root_folder_id`/`drive_id` set by hand (`onedrive.go:202-213`) | Made as needed |
| ms-365-mcp-server | MCP tools, one per Graph route (`src/endpoints.json`) | `create-upload-session` returns the URL and the caller PUTs the bytes (`endpoints.json:779-785`); simple PUT up to 250 MB, base64 in the argument (`:770-776`) | None found | `/items/{parent}:/{name}:/content` paths |
| `m365` | CLI commands | Graph `createUploadSession` then PUT to `uploadUrl` (`src/m365/file/commands/file-add.ts:82-96`) | Turned into a Graph URL from the web URL (`file-add.ts:123`) | `spo folder add` |
| OpenClaw | Its own code | — | `u!` + base64url, then `/shares/{id}/driveItem/content` (`extensions/msteams/src/attachments/shared.ts:207-221`) | — |

ms-365-mcp-server's README notes that `/content` answers 302 to a SharePoint download URL and that an egress proxy
must allow those hosts (`README.md:889`), as Hypomnema's GitHub catalog lets the log redirect through as plumbing.

Graph's `GET /shares/{u!base64url}/driveItem` opens what a sharing URL points at; `Prefer: redeemSharingLink` makes
the access durable. Least privilege is `Files.ReadWrite` delegated, `Files.ReadWrite.All` as the app. For OneDrive
for Business and SharePoint it "always requires authentication and can't be used to access anonymously shared
content without a user context" (shares-get).

## Can team@beyondpilot.ai get a token in Nestlé's tenant?

What Microsoft's pages say:

- **The guest account.** Since May–July 2026 SharePoint sharing in commercial tenants always goes through Entra B2B,
  and SharePoint's own one-time passcode retires in October 2026: a person invited from SharePoint gets a guest
  account in the directory (sharepoint-azureb2b-integration). So the invitation should have made
  `team@beyondpilot.ai` a guest of Nestlé's tenant (`12a3af23-a769-4654-847f-958f3d479f4a`, the same for
  `nestle.com` and `nestle.onmicrosoft.com` in their OpenID metadata, read 2026-11-17).
- **How it signs in.** Redemption tries a managed Entra account, SAML federation, Google federation (gmail.com
  only), an existing Microsoft account, then the email one-time passcode when enabled (on by default), else asks
  to create a Microsoft account for that address (redemption-experience, "Invitation redemption flow"). The realm of
  `team@beyondpilot.ai` is `NameSpaceType: Unknown` (no Entra home; read 2026-11-17), so it signs in with a
  one-time passcode or with a Microsoft account if one exists for that address. Either way the sign-in must name
  Nestlé's tenant, not `common` (redemption-experience, "direct link is tenant-specific").
- **Consent.** Before a multi-tenant app can act as the guest, someone consents in Nestlé's tenant. The default for
  new tenants, "Let Microsoft manage your consent settings", lets users consent to delegated permissions except a
  list that includes `Files.Read.All`, `Files.ReadWrite.All`, `Sites.Read.All` and `Sites.ReadWrite.All`
  (manage-app-consent-policies, "Microsoft recommended user consent policy"). `Files.ReadWrite` is not on that list.
  The stricter `microsoft-user-default-low` covers "member type users", verified publishers and low-impact
  permissions only (same page; configure-user-consent). A tenant may also turn user consent off; then only an
  admin consents.
- **What is not documented**: whether a guest may consent at all under Nestlé's policy, whether `Files.ReadWrite`
  without `.All` reaches an item shared from another person's site (shares-get lists it as least privileged;
  rclone asks for the `.All` scopes), and whether Nestlé's Conditional Access asks a guest for MFA or a sign-in
  frequency that ends an unattended refresh token.

**Verdict: must test.** Inferred: a large enterprise tenant is unlikely to let a guest consent to `*.All` file
scopes for an unverified app; `Files.ReadWrite` alone may pass. The test is cheap and decides the design:

1. In a browser, open the shared link as `team@beyondpilot.ai`, accept, and sign in (passcode by email). This
   confirms the guest account.
2. Register Hypomnema's Entra app (multi-tenant, Web redirect `http://localhost:1`, a secret) and open
   `https://login.microsoftonline.com/12a3af23-…/oauth2/v2.0/authorize?client_id=…&scope=openid email offline_access User.Read Files.ReadWrite&response_type=code&code_challenge=…`.
   A consent page means user consent works; "Need admin approval" (AADSTS65001/90094) means it is blocked. Try
   `Files.ReadWrite.All` the same way.
3. With the token: `GET /v1.0/shares/u!<base64url of the folder link>/driveItem` with
   `Prefer: redeemSharingLink`, then `PUT /v1.0/drives/{driveId}/items/{folderId}:/spike.txt:/content`, then a
   refresh after 24 hours.

## Read and write, and their defaults

Hypomnema's Google catalog (`internal/controlplane/serviceproxy/catalog.go:365-392`) splits Drive into read (allow),
add a new file or folder (allow), change a file, delete, share (ask). Graph's routes map onto the same five; Onyx's
Drive catalog and ms-365-mcp-server's `--read-only` draw the same line. One difference: Graph's
`PUT …/{parent}:/{name}:/content` both creates and replaces. It is a new file only with
`@microsoft.graph.conflictBehavior=fail` (or `rename`); without it, it is a change (inference from Graph's
conflictBehavior; no reference makes this split). A daily export that replaces its files would ask once and be
approved always for its Routine, as Routines already allow (issue #211).

## Options

| Option | Moving parts | Failure modes | State | Operations |
|---|---|---|---|---|
| A. `microsoft` Integration of kind `oauth` (as Google): delegated sign-in as the guest in Nestlé's tenant, tokens in the vault, Graph and SharePoint through the service proxy, Graph REST through `hypomnema tool run microsoft -- curl` with a skill | One Entra app; one Connection per tenant; the proxy's catalog; a second token audience for `*.sharepoint.com` | Consent refused; Conditional Access ends refresh; the guest removed | Refresh token per Connection | Owner keeps the app's secret current (≤24 months) |
| B. A as above, with rclone pinned as the tool | A's, plus rclone's config with a placeholder token and its own SharePoint token source | A's, plus rclone refreshing or scoping a token it cannot have (its `sharePointClient` wants a token source; inference that a placeholder breaks it) | A's | A's, plus a second pinned tool |
| C. The ms-365-mcp-server as a remote MCP Connection | Waits on issue #659; a Node server; its own token cache | A's, plus a second token holder | Tokens outside Hypomnema's vault | A server to run |
| D. Nestlé's admin consents, or grants our app `Sites.Selected` on that one site (app-only) | Nestlé's IT; our app's certificate or secret | Nestlé says no or takes weeks | No user token | Nestlé's admin; we keep the secret |
| E. No Microsoft: Nestlé reads (or syncs) the Google Drive folder the export already fills | None | Nestlé's policy may block Google Drive | None | None |

An "Anyone with the link can edit" folder does not help the API: `/shares` needs a signed-in user on SharePoint
(shares-get, Remarks), and such links are often disabled. rclone's "non-admin" mode copies a token out of the
browser's network tab (`onedrive.md:243-290`); it expires and is not something to run unattended.

## Microsoft pages read (fetched 2026-11-17)

- https://learn.microsoft.com/en-us/entra/external-id/redemption-experience (updated 2026-04-22)
- https://learn.microsoft.com/en-us/sharepoint/sharepoint-azureb2b-integration (updated 2026-10-07)
- https://learn.microsoft.com/en-us/entra/identity/enterprise-apps/configure-user-consent (updated 2026-08-04)
- https://learn.microsoft.com/en-us/entra/identity/enterprise-apps/manage-app-consent-policies (updated 2026-08-28)
- https://learn.microsoft.com/en-us/entra/fundamentals/users-default-permissions (updated 2026-06-23)
- https://learn.microsoft.com/en-us/graph/api/shares-get?view=graph-rest-1.0 (updated 2025-07-23)

The design proposed from this note is in [issue #710](https://github.com/kl3inIT/hypomnema/issues/710).
