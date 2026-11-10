# Lark: a bot that gives work, and Lark as an Integration

Date: 2026-11-10. Read from clones in `.tmp/`: OpenClaw (`d622a2ba4`), Hermes Agent (`022a1443`), OpenMausBot
(`1de1164`), Lark's CLI `larksuite/cli` (`ac22a60`, v1.0.100, MIT); and from the owner's MemoryOS (`D:\MemoryOS`,
`9c206cbb`). OpenClaw, Hermes and OpenMausBot were read by sub-agents; a line marked "(agent)" was not opened
again. Inferences are marked.

The questions: can the owner give Hypomnema work from Lark through a bot, who may talk to it, and how do agents
use Lark (documents, calendar, mail) as an Integration.

## 1. Who has a Lark channel

| Reference | Lark or Feishu channel | Where |
|---|---|---|
| OpenClaw | Yes, a channel plugin | `extensions/feishu/` |
| Hermes Agent | Yes, a platform adapter | `plugins/platforms/feishu/adapter.py` |
| MemoryOS | Yes: design accepted 2026-10-13, an adapter in code | `docs/increments/active/mem-246-lark-bot/design.md`, `memoryos-integration-lark/.../LarkChannelAdapter.java` |
| OpenMausBot | No outside chat channel at all (agent) | Outside systems use a per-bot HTTP API, webhooks, routines, an MCP server (`docs/bot-api.md`) |
| Omnara, Companion | None (agent) | |
| Omnigent | None for Lark; a Slack socket-mode bot as its own package (agent) | `omnigent/cli.py` |

## 2. How the three channels work

| Point | OpenClaw (agent) | Hermes (agent) | MemoryOS |
|---|---|---|---|
| Events arrive | `connectionMode` `websocket` (default) or `webhook`; `Lark.WSClient` of `@larksuiteoapi/node-sdk` (`src/config-schema.ts`, `src/monitor.transport.ts`) | `lark_oapi.ws.Client`, default; or a webhook | The SDK's persistent connection, tried on Lark international from the staging host |
| Who may send a direct message | `dmPolicy`: `pairing` (default), allowlist (`allowFrom`), `open` (requires `*` in `allowFrom`) | `FEISHU_ALLOWED_USERS`; empty means pairing | Every member of the Lark organisation; a sender of another `tenant_key` is refused; the person is matched by the email Lark reports |
| Groups | `groupPolicy` `allowlist` by default; the bot must be named unless the policy is `open` | `FEISHU_GROUP_POLICY` `allowlist` by default; mention required by default | Answers when named; a group Lark marks external is refused whole |
| A chat becomes | A session key from channel, account and peer (`resolveAgentRoute`); `groupSessionScope` for groups | `build_session_key`: platform, kind, chat, thread, user | A chat session; a reply continues its conversation, a new message continues while the last is under an hour old (four hours in a group) |
| Reply | A CardKit card in streaming mode; `post` otherwise | The bot's message edited as text arrives | A CardKit card updated by element, with sources |
| Approval or question | A card with buttons (`src/card-ux-approval.ts`) | A card with once, session, always, deny | Recorded for later: the approver is the requester, checked by the `open_id` of who pressed |
| A delivery repeated | A stored claim per message, 24 h (`src/dedup.ts`) | Message ids kept 24 h in a file | A row per message id; the event is acknowledged at once, since Lark pushes again after three seconds |
| A message while a turn runs | Queue modes `interrupt`, `steer`, `followup`, `collect`; the default was not confirmed | `interrupt` by default, `queue` as the other | The second waits for the first |
| Several bots | `channels.feishu.accounts.<id>`, each bound to an agent | One connection per profile | Several connections a Tenant, one provider app each |
| Process | The agent's | The agent's | Its own (`memoryos-channels`), because a hosted deployment holds one connection a Tenant and the SDK needs supervision (ADR 0028) |

Lark's limits as MemoryOS recorded them from Lark's documentation: fifty connections an app, each event to one
connection, three seconds to acknowledge, persistent connections for custom apps only.

OpenClaw names an allowed sender by Lark's ids (`open_id`, `user_id`; `src/policy.ts`), not by a name. MemoryOS
reads a sender's email with `contact/v3/users/{open_id}`.

## 3. What an outside sender may do (OpenMausBot, agent)

`docs/runtime-contract.md`, `server/host/outside-fence.ts`: work whose origin is `outside` (webhook, relay, guest,
peer, API) carries no person's authority. Its lines are labelled information only, it inherits no approval level,
its questions go to the person and no bot may answer them, and an API caller gets `needs_approval` that only a
person in the app can answer. A guest writes only in conversations it opened and can never answer a card.

## 4. Lark's CLI as the tool of an Integration

Read in `.tmp/lark-cli`:

- A ready token is read from the environment for both identities: `LARKSUITE_CLI_TENANT_ACCESS_TOKEN` and
  `LARKSUITE_CLI_USER_ACCESS_TOKEN`; also `LARKSUITE_CLI_BRAND`, `LARKSUITE_CLI_CONFIG_DIR`
  (`internal/envvars/envvars.go`).
- Releases are one archive per OS and architecture, `lark-cli-<version>-<os>-<arch>.tar.gz`, with
  `checksums.txt` (`.goreleaser.yml`), so a version can be pinned with its SHA-256 as `gws` is.
- It uses the standard proxy variables and warns when one is set; `LARK_CLI_NO_PROXY_WARN` silences the warning
  for an agent reading JSON (`internal/transport/warn.go`).
- It ships its own skills, one folder each under `skills/`: `lark-calendar`, `lark-doc`, `lark-drive`,
  `lark-mail`, `lark-sheets`, `lark-base`, `lark-wiki`, `lark-task`, `lark-im`, `lark-shared` and others.
- An auth sidecar mode: with `LARKSUITE_CLI_AUTH_PROXY` set, every request goes to a sidecar that attaches the
  real credential, signed with HMAC; the CLI holds only a placeholder (`extension/transport/sidecar/`,
  `sidecar/`). It is compiled in only under the build tag `authsidecar`; the plain build refuses to run when the
  variable is set (`main_noauthsidecar.go`). This is the shape of "workers never hold a raw secret", and needs a
  build of our own.
- DPoP is implemented (`internal/dpop/`). Not read: whether it applies to a token given through the environment.

For Lark itself OpenClaw and Hermes call the SDK from tool functions in a plugin, not a CLI
([connectors](2026-11-10-connectors.md)). Hypomnema's standard is a CLI with its skill (issue #103).

## 5. What follows for Hypomnema (inference)

- A Lark bot is a Channel: it carries messages into a Chat with the coordinator and answers there. It runs in
  the control plane, as OpenClaw's and Hermes' run with the agent; MemoryOS's reasons for a process of its own
  are a hosted deployment's.
- A persistent connection suits a control plane on a Tailscale address: nothing public is opened.
- Not checked: that Lark's Go SDK has the persistent connection, and the two things only a real app shows: a
  `lark-cli` call with a real token on a node, and whether Lark asks for DPoP there.
- Several bots, each a persona as OpenMausBot's bots are, have no counterpart yet: Hypomnema has one coordinator.
  OpenClaw binds each Lark app to an agent; the nearest thing here is a Profile.

## Sources

- OpenClaw: https://github.com/openclaw/openclaw · Hermes Agent: https://github.com/NousResearch/hermes-agent
- OpenMausBot: https://github.com/milind-soni/OpenMausBot
- Lark CLI: https://github.com/larksuite/cli
- MemoryOS: the owner's repository, `docs/increments/active/mem-246-lark-bot/`, ADR 0028
