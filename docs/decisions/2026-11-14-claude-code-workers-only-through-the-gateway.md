# A Claude Code worker runs only through the model gateway; no subscription token reaches a node

Date: 2026-11-14. Status: accepted by the owner (step 4 of issue
[#381](https://github.com/kl3inIT/hypomnema/issues/381), plan approved 2026-11-14). Supersedes the part of
[2026-11-13-worker-credentials-in-the-environment.md](2026-11-13-worker-credentials-in-the-environment.md) that sends
a Claude Code worker its Claude subscription token (`CLAUDE_CODE_OAUTH_TOKEN`), and the part of issue
[#150](https://github.com/kl3inIT/hypomnema/issues/150) that sends the token of `claude setup-token` with each
Dispatch's offer. A git host's token still reaches the worker as that record says.

## Context

Until now a Claude Code worker ran under a Claude account: one whose token a node held in a file
(`hypomnema-node accounts set`), or one whose `claude setup-token` the control plane kept and sent in each offer
(`accounts.sent_to_node`, the offer's `account_token`). Since issue #381 step 1 a worker may instead call the
control plane's model gateway with its Dispatch's key, as Codex, omp and opencode workers do. The probes of #381
([research](../research/2026-11-14-claude-code-workers-through-the-gateway.md) and the issue's comments) showed:

- Through the gateway, `CLAUDE_CODE_PROMPT_CACHE_TTL=1h` gives the main conversation the one-hour cache a
  subscription gets by itself; a trial Task wrote only `ephemeral_1h` and read from its second request on.
- The gateway ranks every Claude account it holds for every client (issue #319): the account pasted with
  `--token` serves it too, a session resumed after a switch of account read its cache, and (read in the code, not
  provoked) a 429 or 5xx moves the request to the next account.
- Every Claude profile in use already named the gateway.

## Decision

- **A Claude Code worker gets only its Dispatch's gateway key.** Runtime `claude` accepts only a provider of kind
  `model_gateway`, as omp and opencode. A profile naming another is refused when applied; a Task that would run
  Claude Code without the gateway (a profile with no provider, a Task that sets its runtime to `claude`) is not
  placed and waits with the reason `needs_gateway`. The node's Claude runner refuses an offer that names an account.
- **The subscription token never leaves the control plane.** `accounts add claude <label> --token` and the web's
  "Paste a token" still keep the token in the vault, where the gateway serves the account; the column
  `sent_to_node`, the offer's `account_token`, `hypomnema-node accounts set` and the node's `accounts`
  configuration go.
- Account placement on a node (`pickAccount`) stays for the runtimes that log in on a node (Codex).

## Alternatives considered

- **Keep both paths** (as since #381 step 1). A worker under a sent token is tied to that one account, gets no
  ranking or failover, and the token sits on the node, which the 2026-11-13 record accepted as a risk only because
  nothing else was built. Not kept: the gateway path does all the old one did (probes above) with fewer moving
  parts: one credential path in the runner, no token in the offer, no file on a node.
- **Send the token, but through `apiKeyHelper` or a proxy on the node** (the earlier plan, superseded on
  2026-11-13). Still puts the token on the node. Not taken.

## Consequences

- A node holds no Claude credential: a worker that is tricked can send away only its Dispatch's key, which works for
  the Dispatch's model alone and is revoked when the Dispatch settles.
- The starting profiles `implementer`, `reviewer` and `explorer` name no provider; on a new install they wait with
  `needs_gateway` until the owner points them at a provider of kind `model_gateway`.
- A Conversation begun under a node's Claude account before this change does not go on; its Task waits with
  `needs_gateway`. After the release the owner checks no Task is left on the old path, removes the trial profile
  `implementer-gateway`, and deletes any token file left on a node (issue #381, step 4 plan, item 4).
- `ARCHITECTURE.md`, `README.md` and `docs/vision.md` describe the gateway as the only way a Claude Code worker runs.

## Sources

- [Claude Code workers through the model gateway](../research/2026-11-14-claude-code-workers-through-the-gateway.md):
  Claude Code's docs (prompt caching, LLM gateway), omp, CLIProxyAPI, 9router-go, and the probes.
- Issue [#381](https://github.com/kl3inIT/hypomnema/issues/381): the probes of an account switched mid-session and
  the trial on v0.9.27.
- CLIProxyAPI `claude_executor_auth.go` (`isClaudeSetupToken`): setup tokens pooled with OAuth logins as accounts of
  one provider, as the gateway now serves the pasted token.
