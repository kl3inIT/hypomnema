# Remote MCP servers and the Integrations catalog (2026-11-16)

Asked by the owner after the BeyondPilot Bot could not reach BeyondPilot's operator MCP server
(`https://beyondpilot.ai/mcp/operator`, OAuth with an operator account): should a remote MCP server be a kind of the
shared Integrations catalog or set per Bot, how do references sign in to one, and does the Integrations page still fit
as integrations grow. Builds on [connectors](2026-11-10-connectors.md). Clones are in `.tmp/`. Two sub-agents read the
code; a line marked "(agent)" was not opened again. Three citations were opened to check: OpenMausBot
`wire.ts:385-386`, Coder `dump.sql` availability and allowlist columns, Omnara `modes.go:15-19`.

## Where a remote MCP server is declared, and how it is signed in to

| Project | Declared | OAuth | Grant and tool policy |
|---|---|---|---|
| Coder | Organization catalog; `availability` force on, default on or off (`coderd/database/dump.sql:2867-2927`) | Backend runs it: discovery from the server's metadata, dynamic registration or a client set by hand, PKCE; one token per user, encrypted; refreshed when needed (`coderd/mcp.go:940-962,1245-1288,2479-2558`) (agent) | Per chat; allow/deny tool list per server (agent) |
| Onyx | Admin catalog, public or for chosen users and groups | Backend runs it: discovery, client metadata document or dynamic registration, PKCE; the admin chooses one shared sign-in or one per user (`backend/onyx/server/features/mcp/oauth.py:213-400`) (agent) | Per tool allow/ask/deny, MCP tools ask by default (agent) |
| Omnigent | Operator catalog file; a session opts in (`omnigent/server/mcp_registry.py:76-248`) (agent) | Backend, sign-in URLs and client entered by hand, PKCE, refreshed 30 s before expiry (agent) | Catalog and agent allowlists; allow/ask/deny (agent) |
| OpenMausBot | App-wide `mcpServers`; a bot picks by name (`packages/phone-protocol/src/wire.ts:385-386`) | Its server runs it: discovery, dynamic registration, PKCE; one token set per installation (`server/mcp-oauth.ts:371-556`) (agent) | Per-bot `mcp:server:tool` allow/deny (agent) |
| OpenClaw | Gateway-wide `mcp.servers` (`src/config/zod-schema.mcp-server.ts:31-68`) (agent) | MCP SDK's OAuth; CLI or gateway callback (agent) | Include/exclude per server; approvals remembered per tool (agent) |
| Omnara | Per agent; the OAuth secret is shared and granted onward (`internal/agentconfig/source_schema.go:98-118`) (agent) | Backend, PKCE required (agent) | Allow/ask/deny per tool, MCP tools ask by default (`internal/toolpermission/modes.go:15-19`) |
| Hermes | Per profile (`hermes_cli/mcp_config.py:210-244`) (agent) | MCP SDK's OAuth, token files (agent) | Include/exclude per server (agent) |
| OpenHands | Each user's settings, filtered per profile (agent) | Backend not in the clone (agent) | Servers per profile (agent) |

They agree that the server runs the sign-in and keeps the tokens (the agent never holds the refresh token), that the
standard MCP sign-in is discovery, automatic registration and PKCE with a hand-entered client allowed, that a token
is refreshed only when needed, and that tools are always narrowed. Five of eight declare the server once and grant it
per agent; they differ on whose sign-in it is (one per user, one shared, or the admin's choice).

## How a catalog of integrations is laid out

| Project | Layout | Search, groups | Connected vs available | Custom MCP server |
|---|---|---|---|---|
| OpenMausBot | Tiles in a side panel (`src/components/PluginsPanel.tsx`) (agent) | Search, category pills (agent) | Marketplace and Connected tabs (agent) | "MCP servers" panel with "Add server" (agent) |
| OpenHands | Card grid (`src/routes/mcp.tsx:153-213`) (agent) | Search, Installed/Library filter (agent) | Installed above Library (agent) | "Add custom" in the header (agent) |
| Onyx plugins | One-column list; "Add" opens a catalog modal (`sections/plugins/PluginAddCatalogModal.tsx:74-98`) (agent) | Search, kind chips (agent) | Page lists what is installed (agent) | Last card in the catalog modal (agent) |
| Coder | Table; own page per server (agent) | Search (agent) | Added above available (agent) | Admin's "Add server" page (agent) |
| Omnara | Instance list; catalog on its own page (agent) | Search (agent) | Connected list, then the catalog (agent) | Per agent, from a URL or the MCP registry (agent) |
| Orca | Stacked cards, two groups, seven fixed providers (`IntegrationsPane.tsx:31-72`) (agent) | None | No split | None |

Hypomnema's page follows Orca: one card per provider, accounts as rows inside it, every provider always shown. That
fits a fixed list; every reference with an open-ended list keeps what is connected apart from what can be added and
has search.
