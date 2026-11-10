# The model gateway serves a declared model table; vendor CLIs go straight to their vendor

Date: 2026-11-10. Status: accepted by the owner; implementation started with issue #20 (the model table, keys and
`GET /v1/models`). Replaces the intent, held in [vision.md](../vision.md) and the first design of issue #20 until
that day, that every model request passes through the gateway, that a Claude subscription is used only by Claude
Code, and that the gateway never builds a request with a subscription.

## Context

The coordinator (pi) and the owner's own tools reach models through the owner's 9router today; workers are vendor
CLIs with a subscription. The owner wants one gateway of their own instead of 9router: an OpenAI-compatible API,
only the models they declare, accounts added in one place, subscriptions (Codex, Antigravity, Claude) usable from
any client, and room for images and speech later. The owner is the only user.

## Decision

- **One module, `internal/controlplane/modelgateway`, with one surface**: an OpenAI-compatible API (`/v1/…`) over a
  model table the owner declares. A public name has targets (a provider and the model's name there) in the order
  tried; a name that is not declared is refused.
- **Adapters in a registry, one small interface per capability.** Chat first; a later capability (speech, images)
  adds its own interface and endpoint and touches no existing adapter. An OpenAI-compatible provider is data. The
  request is read into a typed structure that keeps unknown fields, as Bifrost does, which is also the hub format
  for translating to a provider that speaks another format.
- **Vendor CLIs do not come through the gateway.** Claude Code and Codex talk to their vendor with a token the
  control plane hands them for the Dispatch; their usage comes from the CLI's own report and the account's quota
  from the vendor's usage endpoint.
- **One store of accounts.** An API key or a subscription is added once, kept in the vault, and refreshed only by
  the control plane; it is handed to a vendor CLI, served by the gateway through an adapter, or both.
- **The gateway serves subscriptions itself** through adapters that build the request as the vendor's client does,
  ported from 9router-go and CLIProxyAPI (both MIT). The owner accepts that a vendor may block an account used this
  way.
- **Keys are stored as hashes**, each with the models it may use.
- **Order**: the core with an OpenAI-compatible provider; subscription accounts; the Codex, Antigravity and Claude
  adapters. Gemini with an API key, speech, images, the web screens, and omp and opencode as workers come when
  needed.

## Alternatives considered

- **Keep running 9router or 9router-go.** It has everything listed and a dashboard the owner likes. It is a
  separate product with its own database, login and updater, and does not share the vault or the accounts. Its
  routing, provider table and dashboard are the reference instead.
- **Vendor CLIs through a native passthrough surface** (Coder's AI gateway): the gateway would see every request.
  Orca, Omnigent and OpenHands run the CLIs without a proxy and read usage from the CLI, the node already does the
  same, and a gateway that is down would stop every worker.
- **Pairwise translation** (CLIProxyAPI: 7 formats, 21 pairs) instead of a hub: single pairs reach thousands of
  lines.
- **One wide provider interface** with every capability (Bifrost, new-api): each provider answers "unsupported" for
  most methods, and a new capability changes every provider.
- **Subscriptions only through the vendor's own CLI** (the earlier rule): no account blocked for it, and no
  subscription model for pi or the owner's tools without 9router.

## Consequences

- A subscription adapter carries the vendor client's version strings and request shape, which change; each records
  the reference commit it was ported from.
- An account shared by workers and the gateway can be exhausted or blocked from either side.
- A worker's individual requests are not seen; a Dispatch's total is.
- Until the Codex adapter exists, the gateway can put 9router behind it as an OpenAI-compatible provider, so pi
  moves to the gateway first and off 9router later.

## Sources

[Model gateway: routing, providers, translation and subscriptions](../research/2026-11-10-model-gateway-routing.md);
issue #20, with the comparisons of forwarding details and of how the references run vendor CLIs (§12).
