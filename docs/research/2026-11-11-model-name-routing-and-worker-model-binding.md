# How a model name routes to a provider and an account, and how a worker picks a model

Date: 2026-11-11. The owner asked, after the Claude subscription landed, how a requested model resolves to a backend
account, whether a `provider/model` prefix is the whole answer, and how the worker side binds a model. This note is
the layering and the naming; account rotation itself is [several accounts](2026-11-10-several-accounts.md).

Read by three scouts in the clones under `.tmp/`: CLIProxyAPI at c5251b5, 9router-go at 64c156d, pi and omp at
660a31d. Hypomnema's own handling read at `origin/main`. Paths are in those repositories.

## 1. The layers each has

Every gateway has three distinct concepts: the **client model name**, the **provider** (an upstream backend type),
and the **account/credential**. A worker-side framework keeps the first two and leaves the account to one credential
or to a gateway.

| | Name → provider | Provider → account | Prefix form | One name → many backends |
|---|---|---|---|---|
| CLIProxyAPI | `GetModelProviders(name)`, a reverse index each account builds by declaring the models it serves (`internal/registry/model_registry.go`) | priority tier, then round-robin / weighted / fill-first among that provider's accounts (`sdk/cliproxy/auth/selector.go`, `scheduler.go`) | no prefix; the index resolves it | "mixed" models round-robin across providers too |
| 9router-go | `resolveModel` (`internal/handlers/chat/resolution.go`): a `provider/model` **prefix**, else an alias, else a "combo" | the provider's `ProviderConnection` rows, sticky round-robin / random / priority (`connections.go`) | **yes**, `provider/model`, and `x-connection-id` pins an account | a "combo" is one name → an ordered list of `provider/model` leaves |
| pi | a catalog `Model` carries `api` + `provider` + `baseUrl` (`catalog/define.ts`, `src/types.ts`); the agent holds the object | **one credential per provider** (`src/auth/credential-store.ts`), no rotation; multi-account is left to a gateway | provider is on the model, not a user-typed prefix | model-substitution fallback only (`allowedFallbackModels`) |
| omp | same catalog as pi | a **pool** of many credentials, chosen by session-hash affinity or round-robin, ranked by remaining quota (`src/auth/select.ts`, `affinity.ts`) | same | account-level credit fallback (`rotation.ts`) |
| Hypomnema | a gateway model's `targets`, an ordered list of `{provider, model}` (`modelgateway`, `serveChat`) | `underAccounts`/`Token`: the first usable account of the runtime, failover + cooldown | the profile already names `model-gateway/<name>`; a model may be named per source (`claude-sonnet` vs `claude-sonnet-5-5`) | `targets` is the ordered list |

## 2. Read for Hypomnema

- **A model name is not an account.** All five separate the name the client asks for from the account that serves it;
  the prefix (9router-go, OpenRouter, the pi/omp catalog) answers *which provider*, not *which account*.
- **Prefix solves disambiguation, not failover.** A `provider/model` name pins one backend; a single name that falls
  back to another needs a list regardless — 9router-go's "combo", CLIProxyAPI's "mixed", Hypomnema's `targets`. Every
  one that fails over keeps such a list. Hypomnema's `targets` is this, and matches the two gateway references.
- **Worker-side (pi/omp) does not route accounts** beyond omp's own pool; pi expects an external gateway (it ships
  gateway providers: openrouter, vercel, cloudflare). This is why Hypomnema puts account routing in the gateway, not
  in the node worker — the worker names a gateway model and the gateway owns the fan-out.
- **Where Hypomnema departs:** it takes accounts in a fixed order (first usable), while CLIProxyAPI, 9router-go and
  omp **round-robin** across a provider's accounts and keep a **session stuck to one account** so the per-account
  prompt cache keeps hitting (9router-go measured cache hits falling "from over 90% to under 35%" without it). This
  only matters with two or more accounts of one kind; it is the subject of a separate issue.
