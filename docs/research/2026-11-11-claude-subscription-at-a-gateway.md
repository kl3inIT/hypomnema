# A Claude subscription behind a gateway: what others do, and Anthropic's rules

Date: 2026-11-11. For issue #20, step 5: the owner asked whether the model gateway should serve Claude models from
his Claude subscription, as the routers do, or whether omp could use the subscription by itself. Read by two Sonnet
scouts in the clones under `.tmp/` (9router-go at 64c156d, CLIProxyAPI at c5251b5, 9router at 1fe586b, OpenClaw at
d622a2b, hermes-agent at 022a144, omp at 660a31d, pi at 2a32a60, LiteLLM at 5103d8a, Bifrost at 03ead19, opencode at
145e948, Omnigent at cf1021e) and, for Anthropic's own pages, by a web fetch on the same day. Nothing was run.

**Outcome: the research recommended against building step 5 (section 3). The owner read this note and its terms, and
decided to build it anyway for his own subscription, his own agents, and no one else (owner, 2026-11-11). The adapter
is now built (section 4); this note keeps the rules and the risk on the record.**

## 1. How a program other than Claude Code uses the subscription

Every one that does calls `https://api.anthropic.com/v1/messages` with the subscription's OAuth token as a bearer
token and presents itself as Claude Code. A `claude setup-token` token (`sk-ant-oat01-…`, one year, no refresh) is
enough for it.

| | Token | What it sends to be taken for Claude Code |
|---|---|---|
| 9router-go, 9router | Its own OAuth login (`claude.ai/oauth/authorize`, client id of Claude Code, pasted `code#state`), refreshed 4 h early | `anthropic-beta: claude-code-20250219,oauth-2025-04-20,…`, `User-Agent: claude-cli/2.1.280`, `x-app: cli`; a first system block `x-anthropic-billing-header: cc_version=…; cc_entrypoint=sdk-cli; cch=…`; a made-up `metadata.user_id` (device, account, session); every tool renamed with `_ide` and twenty Claude Code tools added as decoys (`internal/handlers/chat/claude_cloaking.go`). Its registry marks the provider deprecated, with a risk notice |
| CLIProxyAPI | The same login, the account's real profile, or a setup-token | The same headers in Claude Code's own order and casing over a uTLS transport; the billing block signed as Claude Code signs it; the line "You are Claude Code, Anthropic's official CLI for Claude."; the caller's system prompt moved out of the top level, because there it "triggers" Anthropic's "third-party classifier" (`claude_executor_cloaking.go`) |
| omp | Its own OAuth login, or a `sk-ant-oat` token in the environment | `claude-cli` user agent, the billing block with a hash of the body, the Claude Code line, `_`-prefixed tools, a `metadata.user_id` (`packages/ai/src/providers/anthropic.ts`) |
| pi, hermes-agent, OpenClaw | A setup-token, or Claude Code's own credentials file | The same headers and the Claude Code line; hermes-agent notes that requests that look like a third-party app are moved to the metered "extra usage" lane and answered 400, and that a content filter applies to subscription tokens only (`agent/anthropic_adapter.py` L322, L550) |
| LiteLLM, Bifrost | The token a Claude Code client itself sends | Nothing of their own: they forward it |
| opencode 2, Omnigent | none | opencode takes an API key only; Omnigent only hands the token to the `claude` program |

So omp has no other road to the subscription than the gateway would have: the difference is only where the code runs.

## 2. What is done with the subscription inside Anthropic's rules

Running the Claude Code program itself, also headless on a server: `claude setup-token` exists "for CI pipelines and
scripts where browser login isn't available" (code.claude.com, authentication), and the GitHub Action takes the same
token. Hypomnema's Claude Code workers do this (issue #150). OpenClaw has a second backend that runs `claude -p`, and
its docs say Anthropic staff told them this use "is allowed again", while recommending an API key for long-lived hosts.

## 3. Anthropic's rules, as fetched on 2026-11-11

- Usage Policy (effective 2026-11-12), under "Do Not Abuse Our Platform": "Resell, proxy, or otherwise provide access
  to Claude through unauthorized means, including services that route requests through consumer subscriptions or
  misrepresent the product or client being used."
- Consumer Terms (2025-10-08), section 3: no access "through automated or non-human means" except "via an Anthropic
  API Key or where we otherwise explicitly permit it".
- Claude Code, legal and compliance: developers "should use API key authentication"; Anthropic "reserves the right to
  take measures to enforce these restrictions and may do so without prior notice".

A gateway adapter for the subscription routes requests through a consumer subscription and presents itself as Claude
Code: both halves of the Usage Policy's sentence. The account at stake is the one every Claude Code worker runs
under.

## 4. What Hypomnema builds (owner, 2026-11-11)

The owner accepts the risk of section 3 — a vendor may block the account used this way — and chose to build the
adapter, ported from 9router-go (64c156d), the same way the Codex and Antigravity subscriptions were built.

- A `claude` subscription vendor (`internal/controlplane/subscription/claude.go`): the owner logs in with Claude
  Code's own OAuth client (PKCE, pasted `code#state`), and the control plane refreshes the token.
- A `modelgateway/claude` package: chat completions ⇄ Anthropic Messages, and the disguise of section 1 — the
  `oauth-2025-04-20` beta on a Bearer token, the billing system block, a `metadata.user_id`, client tools renamed
  `_ide` with the twenty Claude Code decoy tools. Because the gateway's clients (omp, opencode, Codex) are not Claude
  Code, the top-level system is made exactly Claude Code's — the billing block and the identity line "You are Claude
  Code, Anthropic's official CLI for Claude." — and the client's own instructions are moved under the first user turn
  as a `<system-reminder>`, so Anthropic's classifier reads a Claude Code prefix (CLIProxyAPI
  `checkSystemInstructions`; 9router-go leaves them at the top because its clients already are Claude Code).
- A `claude_subscription` gateway adapter (`claude_adapter.go`): the call itself, under the account logged in on the
  control plane, with a refresh on a refused token and a cooldown on a usage limit, as the Codex adapter does.

Alternatives that remain and need no disguise: Claude models at the gateway from the Antigravity account
(`claude-sonnet`) or from an Anthropic API key (provider kind `anthropic`); and giving subscription work to a Claude
Code worker (issue #150).
