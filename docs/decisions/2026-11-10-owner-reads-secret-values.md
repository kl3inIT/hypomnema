# The owner can read a secret's value; an agent never can

Date: 2026-11-10. Status: accepted by the owner; implementation starts with
[issue #108](https://github.com/kl3inIT/hypomnema/issues/108) (reading a value, and the Vault page). Replaces the
rule, held in [conventions.md](../conventions.md#data-and-security) and the design of issue #66 since 2026-11-07,
that the vault lists secrets without their values and that no command or API ever returns one.

## Context

The vault was built as Omnara's secret manager is: a value is written and never read back ("Secret values are
encrypted and never returned by read endpoints"). That was chosen to keep values out of chat histories and away from
agents.

The owner uses the vault as the one place that keeps his secrets: API keys, git host tokens, and soon an OAuth
client's secret and the refresh token of each connected account (issue #103). He wants to look at what is there,
including the values, as in a secret manager. He is the only user.

## Decision

- **The owner's token may read a secret's value.** A value is fetched one secret at a time by its own request,
  never as part of a list; each read is recorded with the secret's name and the token, never the value.
- **An agent's token never may.** A coordinator's token, a worker and a node have no operation that returns a
  secret's value, and no tool or skill offers one. What an agent gets is still what the control plane derives for
  it: a short-lived token with its offer today, a placeholder behind the node's proxy later.
- **Listing does not change**: names and metadata without values, for the tokens that can list today.
- The vault stays behind the control plane's own wiring; no API package imports it.

## Alternatives considered

- **Keep write-only (Omnara, OpenClaw's protected secrets).** Nothing that reads a value exists, so nothing can
  leak one through a reader. The owner then needs a second secret manager beside Hypomnema to look a value up, and
  cannot check what was stored. Rejected by the owner.
- **Roles and per-secret permissions (Infisical: `describeSecret` and `readValue` per environment, path and
  tag).** Built for teams. With one owner, the only line that matters is owner or agent, and the token kinds
  already draw it.
- **Reveal only from a command run on the control plane host.** Keeps values off the network API. The owner works
  from the web and from other machines, and every operation is meant to be a client command.

## Consequences

- A flaw in the web client, which holds the owner's token in the browser, can now read values through the API.
  Before, no route returned one. Keeping that token in a server cookie instead narrows this and becomes more
  worth doing.
- A value read in a terminal can end in scrollback or a log; the command that prints one refuses to write to
  anything but a terminal unless told to.
- The policy table must keep the read owner-only, and a test must hold that a coordinator's token is refused.
- Chat is still not a place for a value: the coordinator cannot read one, so it cannot repeat one.
- Reading an earlier version of a secret becomes possible later, since the vault keeps versions.

## Sources

- Infisical (`.tmp/infisical`, `81e981d`): values fetched on reveal
  (`frontend/src/pages/secret-manager/OverviewPage/components/SecretTableRow/SecretEditTableRow.tsx`), the two
  permissions (`docs/internals/permissions/project-permissions.mdx`), the `get-secret` audit event
  (`docs/documentation/platform/audit-logs.mdx`).
- Omnara (`docs/organization/secrets.mdx`) and OpenClaw (`docs/gateway/secrets/secret-store-and-egress.md`: "there
  is no reveal RPC") for the rule this replaces.
- [Secrets for agents](../research/2026-11-07-secrets-for-agents.md), the research the first rule came from.
