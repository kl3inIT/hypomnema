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
