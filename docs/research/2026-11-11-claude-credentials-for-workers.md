# Giving a Claude Code worker its credential from a central place

Date: 2026-11-11. For issue #150: a Claude Code worker's subscription token sat in a file on its node; the owner wants
it kept by the control plane and handed to the node. Read by a Sonnet scout in the clones under `.tmp/`: OpenClaw at
d622a2b, OpenHands at 91c2e38 and its SDK at e4160d1, Coder at b798061, Omnigent at cf1021e, Orca at 4c13ae4, and in
passing hermes-agent, Bifrost and durable-harness. Omnara, companion, genie, buzz and arc have nothing on it.

## 1. What each does

| | The credential | Kept where | How it reaches the worker | Several accounts | Claude Code's own state | How the owner gives it |
|---|---|---|---|---|---|---|
| OpenClaw | The token of `claude setup-token` (`sk-ant-oat01-…`), or an API key (`docs/concepts/oauth.md`) | An auth profile in its SQLite store | Sent from the gateway to the node per session, and given to `claude` on a file descriptor, not in the environment; the node keeps no file (`src/node-host/invoke-agent-cli-claude-handler.ts`) | Profiles in order; a rate limit or an auth failure cools one down and the next is taken (`docs/concepts/model-failover.md`) | The node clears `CLAUDE_CONFIG_DIR` and the `ANTHROPIC_*` variables | Pasted (`models auth paste-token`); `models status` shows what expired |
| OpenHands | `CLAUDE_CODE_OAUTH_TOKEN`, "a Pro/Max OAuth token" (`docs/ACP_AGENTS.md`) | The agent server's secret store | Looked up as the CLI is spawned, per conversation, and exported in its environment | not found | `CLAUDE_CONFIG_DIR` of its own; `ANTHROPIC_API_KEY` and `ANTHROPIC_BASE_URL` are stripped when the token is set, since the first wins over it and the second sends it to a proxy that refuses it | A form |
| Coder | The same token, or `claude auth login` inside the workspace (`claude-code-subscription.md`) | A secret of the user, or a template variable | The environment of every workspace of the user | none | none | The dashboard; `claude auth status` checks it |
| Omnigent | The same token, an API key, or an `apiKeyHelper` that asks a broker every 15 minutes | The host's environment | Forwarded to its runners from a fixed list of variables | not found | removes a seeded helper | The environment |
| Orca | A full login per account | A `CLAUDE_CONFIG_DIR` per account, on the user's machine | That directory, per launch | chooses an account | strips the other credentials | `claude auth login` with a pasted code |

Claude Code, as the clones say it: `claude setup-token` "prints a long-lived token starting with `sk-ant-oat01-`"
(OpenClaw) and "does not save the token" (Coder); `ANTHROPIC_API_KEY` and `ANTHROPIC_AUTH_TOKEN` win over it (Coder).
How long it lives is in none of the clones.

## 2. Read for Hypomnema

- All four that run Claude Code elsewhere than the user's desk hand it the `setup-token` token; none hands out a
  login's refresh token, which could not be shared.
- OpenClaw is the same shape: a central store, an account taken in order for a session, the token sent to the node
  with the work. It alone avoids the environment.
- The token is pasted by the owner everywhere; none logs in for him.
