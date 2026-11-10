# Connectors in the references (2026-11-10)

Read to decide how Hypomnema gives its agents access to outside services (mail, Lark, Google Drive, a tracker):
the owner wants a connector set up once, on the control plane, and used by the coordinator and by workers on any
node. Clones are in `.tmp/`. A line marked "(agent)" was read by a sub-agent and not opened again; the rest was
opened in the code or docs.

## What each does

| Project | It is called | How the agent calls it | Where the credential is | The agent holds the credential | Granted |
|---|---|---|---|---|---|
| Omnigent | MCP registry service (`examples/mcp-registry/README.md`) | An MCP entry with `transport: registry`: the runner sends the call to the server (`POST /mcp/{id}`), the server calls the service. "The server holds and refreshes upstream credentials"; it works for "sessions on local machines, remote hosts or sandboxes" | The server's database, a user's own, encrypted (agent) | No: "generic MCP credentials stay on the server". Its GitHub token is the exception, handed to the sandbox (agent) | A service's tool allowlist and allowed users; chosen per session; policies such as `gmail_policy` with sending off by default (agent) |
| Omnara | MCP server in an agent's config; integrations | The control plane is the MCP client (agent) | Secrets store, named by id in the config (agent) | No (agent) | Per server and per tool: always allow, always ask, always deny; MCP asks by default (agent) |
| Coder | MCP servers, registered per organization | The chat daemon on the control plane calls the server (agent) | The server's database (agent) | No for registered servers; yes for a workspace's own `.mcp.json` and for git (agent) | Forced on, on by default, off by default; an ACL and a tool allowlist per server (agent) |
| OpenMausBot | Connected apps (Composio) | A stdio MCP shim beside the agent that relays to the harness, which checks the call and relays to Composio (agent) | Composio; the harness keeps only its project key (agent) | No: the shim has a loopback token (agent) | Per bot and per tool, read or write; sending mail asks each time (agent) |
| Hermes | Connectors: a hosted account, or a local MCP server | Two built-in tools (`tool_describe`, `tool_call`) over HTTP to a hosted gateway; skills that wrap a CLI for Google (agent) | The hosted gateway; or token files on the machine, copied into a remote sandbox (agent) | No for hosted; yes for the files (agent) | A member policy over connectors and tools (agent) |
| Companion | Apps | Two built-in tools that speak MCP over HTTP from the remote box to the vendor's server | The control plane's database, encrypted; the refresh token never leaves it | Yes, a short-lived access token pushed to the box (`apps/server/src/plugins.ts`: `Authorization: Bearer ${credential.accessToken}`) | Per companion; Gmail limited to six read and draft tools (agent) |
| OpenClaw | Plugins, skills, saved MCP servers | Built-in tool functions for Feishu; skills that wrap a CLI on the host for Google and mail (`gog`, `himalaya`); MCP run by the gateway (agent) | The gateway's store and the host's keyring (agent) | It depends: with the egress proxy on, the agent holds a placeholder that the proxy swaps for the real value, per destination host (agent) | Per agent; a hook can ask before a call (agent) |
| Orca | Integrations | The `orca` CLI with a skill: `orca linear ...`, `orca jira ...`; GitHub through the local `gh` (agent) | Orca's main process, an encrypted file (agent) | No: the CLI asks the process that holds it (agent, inferred from absence) | Not found |
| OpenHands | Integrations: an MCP marketplace (`INTEGRATION_CATALOG`, the `/mcp` page) | MCP servers in the settings | Settings and a secret store | Secrets reach the shell as environment for a command that names them; output is masked (agent) | Not found |
| Infisical Agent Vault | Services in an access bundle | The real HTTP or MCP URL, through a proxy that runs apart from the agent | Infisical | No: "The agent receives a session token instead" and "the proxy attaches the real credential to each request that the session allows" (`why-agent-vault.mdx`) | A session: one bundle, a time limit, methods, paths and tools |
| t3code | Its own MCP server only | A scoped bearer per provider session, given to each CLI in that CLI's own way (agent) | In memory (agent) | Only its own short-lived token (agent) | The session |
| Buzz | Nothing | | | | |

How the agent CLIs take an MCP server: pi over stdio or streamable HTTP, `pi mcp add <name> --url <url>
--bearer-token-env-var <NAME>` (`packages/coding-agent/docs/mcp.md`, `cli.md`); Codex `[mcp_servers.<name>]` with
`url` and `bearer_token_env_var`, and refuses an inline token (agent); Claude Code `claude mcp add --transport http`
(agent). One remote MCP server addressed by URL, with its bearer in the environment, reaches all three.

## What they agree on

- **The owner connects a service once, on the server.** The server runs the OAuth flow, keeps the refresh token,
  and refreshes (Omnigent, Omnara, Coder, Companion; OpenMausBot and Hermes leave it to a hosted service).
- **MCP is the common protocol** between agent and connector: nine of eleven. The two that are not are Orca (its
  own CLI with skills) and OpenClaw's skills that wrap a CLI.
- **The agent does not hold the service's credential** where the product is built for more than one machine:
  the call goes through the server (Omnigent, Omnara, Coder, OpenMausBot, Hermes hosted) or through a proxy
  (Infisical, OpenClaw). Companion is the exception and hands over a short-lived access token.
- **Access is granted narrowly**: per agent, bot, session or profile, with an allowlist of tools, and writing
  (sending mail) asks first or is off by default (OpenMausBot, Omnigent, Omnara, Companion).
- **A catalog** of known services to pick from (OpenHands, Companion, Hermes, OpenMausBot through Composio, Omnara
  from the public MCP registry); a custom MCP server is always allowed beside it.

## Where they differ

- Who writes the connector: the vendor's own hosted MCP server (Companion, OpenHands, Coder), a broker with
  hundreds of services (Composio in OpenMausBot), or the product's own code on the vendor's SDK (OpenClaw and
  Hermes for Lark).
- No reference states that a CLI is preferred over MCP for agents. opencode warns that an MCP server's tools fill
  the context (agent); Coder and pi answer that by loading tool definitions only when searched for (agent). Orca
  prefers "a programmatic path (shell, filesystem, git, HTTP, existing CLIs)" for its own features (agent).
- Lark: no reference uses `lark-cli` or a Lark MCP server; OpenClaw and Hermes call the official SDK (agent).

## Onyx's Craft Apps: a service with no MCP server

Read after the owner pointed at it (clone `.tmp/onyx`, 2026-11-10; `documentation/admins/plugins/craft_apps.mdx`,
`documentation/security/architecture/craft.mdx`, `backend/onyx/external_apps/`, `backend/onyx/sandbox_proxy/`).

- "A Craft App is a catalog of API endpoints for one application, such as Slack or Gmail. Each user connects their
  own account." Built in: Slack, Gmail, Google Calendar, Google Drive, Notion, Linear, GitHub, HubSpot, Zendesk. No
  Lark.
- **The agent calls the service's own HTTP API** from its sandbox, taught by a skill; no MCP server is involved.
  "The sandbox receives placeholder values for credentials that Onyx manages"; the sandbox's only way out is the
  **sandbox proxy**, which "matches the URL and action, applies its policy, then injects organization or user
  credentials". Raw credentials are in Onyx's storage "and briefly in proxy memory".
- **A built-in App is data, not a client**: a list of actions, each a name and the HTTP routes that are that action
  (`providers/gmail.py`: `gmail.messages.read` is `GET /gmail/v1/users/{userId}/messages` and one message;
  `gmail.messages.send`, `gmail.drafts.create`, ...), with a default policy.
- **Policy per action**: auto-approve, ask, disabled. Ask parks the request at the proxy, before the credential is
  added, until the user answers on an approval card (once, or for the session). The strictest matching policy
  wins; a request to a built-in App that matches no action asks.
- **A custom App** covers "a public HTTP API that no built-in App covers": URL patterns and a credential, no
  action catalog, and every request asks.
- A scheduled task has nobody to ask: its owner chooses the Apps and which actions it may do without asking; a run
  that reaches an action that asks stops and waits.
- Onyx keeps both doors and says when to use which: an App when one exists; an MCP server, which "Onyx calls ... for
  the agent", when none does or when chat needs the tools too. In both the sandbox never holds the credential.
- The same idea is Infisical's Agent Vault and OpenClaw's egress proxy above.

## For Hypomnema

Inference, not read in any reference:

- The closest shape is Omnigent's: the control plane is an MCP gateway. A connector is declared on the control
  plane; the coordinator and each worker get one MCP server URL on the control plane and a bearer for their own
  Run or chat; the control plane checks the grant, attaches the service's credential from the vault and calls the
  service. A worker never holds the service's credential, and a node needs no route to the service.
- This is the vault's credential sessions and proxy ([issue #66](https://github.com/kl3inIT/hypomnema/issues/66))
  for services that speak MCP. A service used through a CLI (`gh`, `glab`, a Lark CLI) needs the other half, an
  egress proxy, or keeps today's handing of a token as environment.
- "Everything the owner can do is a CLI command" is about operating Hypomnema (`hypomnema connectors add`), not
  about how an agent reaches a service.

## Second pass: how an agent calls a service, and what the server must do (2026-11-10)

Asked by the owner after he pointed out that the credential path (a proxy on the node, a vault Session per
Dispatch) was decided on 2026-11-07 ([conventions](../conventions.md#data-and-security)); the open question is
CLI or MCP and how they combine. Four sub-agents read; a line marked "(agent)" was not opened again.

### A service usually has a CLI, and the CLI covers more than its MCP server

| Service | CLI | MCP server | Takes a ready token from the environment |
|---|---|---|---|
| GitHub, GitLab | `gh`, `glab` | GitHub's hosted server | `GH_TOKEN`, `GITLAB_TOKEN` (in use, PR #90) |
| Lark / Feishu | `larksuite/cli` (`lark-cli`): official, MIT, "200+ curated commands, 26 AI Agent Skills" (its README) | `larksuite/lark-openapi-mcp`: official, "currently in Beta stage"; no file upload or download, no direct editing of documents (agent) | `LARKSUITE_CLI_TENANT_ACCESS_TOKEN`, `LARKSUITE_CLI_USER_ACCESS_TOKEN` (agent) |
| Gmail, Drive, Calendar, Docs, Sheets | `googleworkspace/cli` (`gws`): Apache-2.0, "not an officially supported Google product", commands built at run time from Google's Discovery Service, "100+ Agent Skills" (its README). `openclaw/gogcli` (`gog`): MIT, OpenClaw's own | Google-hosted `gmailmcp`, `drivemcp`, `calendarmcp.googleapis.com`, Developer Preview (agent) | `GOOGLE_WORKSPACE_CLI_TOKEN`, "highest priority" (its README) |

- **OpenClaw's bundled skills are almost all a CLI each** (`skills/*/SKILL.md`, `requires.bins`): `gh` (GitHub),
  `gog` (Google Workspace), `himalaya` (IMAP and SMTP mail), `ntn` (Notion), `obsidian`, `xurl` (X), `op`
  (1Password), `curl` with `jq` (Trello's REST API), `mcporter` (calls MCP servers from the shell), and
  `claude`, `codex`, `opencode` as background coding workers. Lark is the exception: tool functions in a plugin.
- **Hermes' skills** name `gws` (Google Workspace, with its own Python client as the fallback), `himalaya`, `gh`,
  `xurl`, `obsidian`, and `claude` and `codex` as workers (`skills/**/SKILL.md`).
- Companion, which uses Google's Gmail MCP server, is limited to six read and draft tools (agent).
- Lark's own guide for embedding its CLI in a platform compiles a credential provider into a wrapper binary, so
  the app secret is in the CLI's process on the agent's machine (agent). That is against "workers never hold a raw
  secret"; the environment token with a placeholder is the form that fits the proxy.
- Not known until tried: whether `lark-cli` and `gws` honour `HTTPS_PROXY` and a CA bundle; whether Lark's DPoP
  (on by default "for new local credentials") applies to a token from the environment.

### An MCP server goes through the same proxy

Infisical: "You add the MCP server to your agent with its real URL and no credentials. When the agent connects to
the server, the proxy attaches the service's credential" (`mcp-servers.mdx`); the proxy also refuses a `tools/call`
that is not allowed and removes it from `tools/list` (agent). Onyx's proxy parses the JSON-RPC body, makes each
tool an action with a policy, forwards plumbing methods, and denies what it cannot classify
(`sandbox_proxy/mcp_jsonrpc.py`, agent). No second gateway is needed for MCP.

### What the server must do for each service (official docs, read by a sub-agent)

| Service | Kept on the server | Minted, and how often | On the wire | Fits a proxy that sets a header |
|---|---|---|---|---|
| Lark, as the app | app id and secret | tenant access token, at most 2 h | `Authorization: Bearer` to `open.larksuite.com` / `open.feishu.cn` | Yes |
| Lark, as a user (mail, own drive, own calendar) | plus the user's refresh token, **single use**: each refresh returns a new one that must be stored | user access token, about 2 h | the same | Yes |
| Gmail, Drive | OAuth client and refresh token; a project left in "Testing" loses its refresh token after 7 days; `gmail.readonly`, `gmail.modify` and full `drive` are restricted scopes | access token, about 1 h | `Authorization: Bearer`; a send carries the whole message in the body (base64url MIME), so an approval can show it | Yes; uploads are a path on the same host |
| Microsoft Graph | client and refresh token (90 days, replaced on each use) | access token, 60 to 90 min | `Authorization: Bearer` | Yes, except large uploads: another host, and the header must not be sent |
| IMAP, SMTP | | | SASL inside a TCP protocol, not HTTP | **No** |
| GitHub | a fine-grained token, or an App's key for 1 h installation tokens | | Bearer for the API; the token as the password for git over HTTPS | Yes; git needs Basic built from the token (done in PR #90) |
| GitLab | a project or group token (`api` for merge requests; `write_repository` is git only) | | `PRIVATE-TOKEN` or Bearer; git as above | Yes |

Mail fits the proxy through a provider's HTTP API (Gmail, Graph, Lark Mail), not through IMAP: `himalaya`, which
OpenClaw and Hermes use, cannot go through it.

### Onyx's proxy, in detail (agent, `backend/onyx/sandbox_proxy/`; MIT, not under an `ee` directory)

- mitmproxy as a library with one addon (`addons/gate.py`, 1418 lines); its own service.
- Refresh is lazy: at injection, when the token has under 120 s left, under a lock; `invalid_grant` disconnects the
  App. Their recorded decision: "A scheduled refresh job is not needed".
- An App is rows of data: URL patterns, a header template (`Authorization: Bearer {access_token}`), and a catalog
  of actions (`RestRoute(method, path)` with `{name}` segments, or a GraphQL operation). Only headers are set;
  what the agent sent in that header is overwritten.
- Ask: the request is held open up to 180 s; an approval row keeps the actions and the request body; the card
  shows a decoded body (a Gmail send as To, Subject, Body, attachment names); the answers are approve once,
  approve for the session, reject; after approval the request is evaluated again before the credential is added;
  every error fails closed. A request to a built-in App that matches no action always asks.
- A skill a service (`skills/builtin/gmail/`: a 167-line `SKILL.md` and a helper script that sends no
  `Authorization` header), pushed to the sandbox only for Apps the user has connected.
- A known issue they list: responses are not scrubbed, so an API that echoes request headers returns the
  credential.
- Why not only MCP, in their words: "The proxy sees every request regardless of the tool or language the agent
  uses, and secrets never enter the sandbox. The cost: the proxy sees all sandbox HTTPS in plaintext."

### Other proxies (agent)

- Infisical: Bearer, Basic or a custom header; placeholder substitution in path, query, headers or body; methods
  and path prefixes per service; HTTP/1.1 only; no approval of a single request; OAuth refresh only for MCP
  services.
- OpenClaw: a placeholder is the secret itself, encrypted (`oc-sent-v2...end`) and bound to exact hosts; the proxy
  covers only processes the gateway runs: "remote `node` exec receive neither proxy variables nor sentinels".
- Codex has its own loopback proxy with hooks that inject headers, and a credential masking feature still under
  development.
- OpenMausBot holds a send for up to 9 minutes behind an "Ask every time" card; Omnigent parks a tool call on a
  policy that answers ask, and evaluates again with the reviewed arguments on the retry.
- Approval of a single call exists in Onyx, OpenMausBot, Omnigent and Codex; not in Infisical or OpenClaw's proxy.

### Agent CLIs (official docs, agent)

- Claude Code honours `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` and `NODE_EXTRA_CA_CERTS`; no SOCKS. A PreToolUse
  hook that times out does not block the call, so it is not a gate.
- An HTTP MCP server is given per run: Claude Code `--mcp-config` with `--strict-mcp-config`; Codex
  `[mcp_servers.<name>] url`, `-c key=value`; pi `mcp.json` (`packages/coding-agent/docs/mcp.md`).
- Skills: `~/.claude/skills/<name>/SKILL.md` or the project's `.claude/skills`.

## What follows for Hypomnema (inference)

- An integration is one thing with one credential path (the proxy and a Session) and one or more faces the agent
  calls it through: a skill with the service's CLI, a skill with the service's HTTP API, or an MCP server. A git
  host is an integration of this kind.
- A CLI with its skill is the default face: it exists for the services asked for (git hosts, Lark, Google), covers
  more than the MCP server does, and its skill is loaded only when used. An MCP server is declared for a service
  that has nothing better.
- The control plane does what no agent may: the OAuth flow, storing a rotated refresh token, minting Lark's and
  GitHub App tokens. The vault's refresh lease (issue #66) is what a single-use refresh token needs.
- The proxy needs, beyond what the conventions record: an action catalog per integration, an answer of ask that
  holds the request and shows its decoded body, and the handling of MCP calls by tool.
