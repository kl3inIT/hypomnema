# A worker's Claude and git host tokens stay in its environment; no proxy on the node

Superseded in part by [2026-11-14-claude-code-workers-only-through-the-gateway.md](2026-11-14-claude-code-workers-only-through-the-gateway.md):
a Claude Code worker no longer gets the Claude token; it runs through the model gateway with its Dispatch's key.

Date: 2026-11-13. Status: accepted by the owner ("chốt như hiện tại", 2026-11-13). It describes what is built (issues
#90 and #152), so no implementation follows. Supersedes the part of
[2026-11-07-two-deployables.md](2026-11-07-two-deployables.md) and of `docs/conventions.md` that planned a
credential session per Dispatch and a proxy on the node that attaches credentials, as Infisical's Agent Vault does.

## Context

The vault keeps the owner's secrets in the control plane. Integrations (Google, Lark, Superset) already reach their
services through the control plane's service proxy, so a worker holds a placeholder and never the token
([decision, 2026-11-11](2026-11-11-service-proxy-in-the-control-plane.md)). Two tokens still reach the worker
itself, for its own Dispatch only, as environment: the Claude subscription token of a Claude Code worker
(`CLAUDE_CODE_OAUTH_TOKEN`, `internal/node/claude/runner.go`) and a git host's token (`GH_TOKEN`, git's extra header,
`internal/shared/githost/githost.go`). The worker can read them.

The plan since 2026-11-07 was to close that with a proxy in `hypomnema-node`: a session per Dispatch, HTTPS decrypted
with the node's own CA, credentials attached on the way, the proxy running as another OS user than the worker.

## Decision

- **Keep what is built.** The Claude token and a git host's token go to the one Dispatch as environment, as
  OpenHands and Omnara deliver credentials. The node is the boundary: a VM used only by Hypomnema, with tokens scoped
  to the Dispatch's repositories and without merge rights (`docs/conventions.md`, Trusted machines).
- **No proxy on the node and no credential sessions** for these tokens. Integrations keep the control plane's service
  proxy.

## Alternatives considered

- **A proxy on the node (Infisical Agent Vault, Omnigent's credential proxy).** The worker never sees a token. It adds
  a process on every node whose failure stops every push, a CA every tool of every runtime must trust (tools that
  ignore `SSL_CERT_FILE` and its siblings fail), a separate OS user set up with sudo (untested), placeholders for tools
  that insist on a token, and failures that are harder to trace. Its gain shows only when a worker is tricked or a
  token leaks. Not taken: too much to run for one owner's trusted nodes.
- **The simpler half-way** (inference, not taken now): a Claude Code worker calls Claude through the model gateway
  with its Dispatch's key, as a Codex worker does (`internal/node/codex/runner_test.go`), and git asks the control
  plane for the token through `GIT_ASKPASS`, as Coder does (`.tmp/coder/cli/gitaskpass.go`). No proxy and no CA; the
  Claude token stays in the control plane. Left for later if the owner wants it.

## Consequences

- A worker that is tricked by what it reads can send these two tokens away; the token then works until the owner
  rotates it in the vault. The owner accepts this for his own nodes.
- A Dispatch's own key (its Gateway key and agent API key, revoked when it ends) is what "Session" means now
  (`docs/glossary.md`).
- `docs/vision.md`, `docs/conventions.md` and `ARCHITECTURE.md` no longer describe a proxy on the node.

## Sources

- [Secrets for agents](../research/2026-11-07-secrets-for-agents.md): Infisical Agent Vault, Omnara, Omnigent.
- [Claude credentials for workers](../research/2026-11-11-claude-credentials-for-workers.md): OpenClaw, OpenHands,
  Coder, Omnigent, Orca.
- Coder `cli/gitaskpass.go`; OpenHands `docs/ACP_AGENTS.md`.
