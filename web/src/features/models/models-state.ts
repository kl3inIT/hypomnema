import type { Account, GatewayModel, Provider } from "@/api/generated/types.gen"

// What the Models page shows, worked out from what the server lists (issue #199). The references that keep
// subscription accounts put an account under its provider (9router's providers/[id]/ConnectionRow.js, OpenClaw's
// pages/model-providers, Orca's settings/accounts); the server lists accounts and providers apart, so they are
// joined here by the runtime an account signs in for.

// The runtime whose accounts a subscription's provider runs under.
const runtimes: Record<string, string> = {
  claude_subscription: "claude",
  codex_subscription: "codex",
  antigravity_subscription: "antigravity",
}

// The name of a subscription as its vendor calls the product, and what kind of account it takes.
const subscriptions: Record<string, { name: string; account: string }> = {
  claude_subscription: { name: "Claude", account: "Claude subscription" },
  codex_subscription: { name: "Codex", account: "ChatGPT subscription" },
  antigravity_subscription: { name: "Antigravity", account: "Google subscription" },
}

export type Subscription = {
  provider: Provider
  name: string
  // account is what kind of account signs in, such as "ChatGPT subscription".
  account: string
  // runtime is what its accounts sign in for: claude, codex or antigravity.
  runtime: string
  accounts: Account[]
}

// subscriptionsOf joins each subscription's provider with its accounts, in the order the gateway tries them.
export function subscriptionsOf(providers: Provider[], accounts: Account[]): Subscription[] {
  return providers
    .filter((provider) => provider.kind in runtimes)
    .map((provider) => ({
      provider,
      name: subscriptions[provider.kind]?.name ?? provider.name,
      account: subscriptions[provider.kind]?.account ?? "Subscription",
      runtime: runtimes[provider.kind] ?? "",
      accounts: accounts
        .filter((account) => account.runtime === runtimes[provider.kind])
        .toSorted((a, b) => a.position - b.position),
    }))
}

// apiProvidersOf are the providers reached with a key: no account signs in to them.
export function apiProvidersOf(providers: Provider[]): Provider[] {
  return providers.filter(
    (provider) => provider.kind === "anthropic" || provider.kind === "openai_compatible",
  )
}

export type Tone = "success" | "warning" | "destructive" | "outline"

export type AccountState = { label: string; tone: Tone; why?: string } | null

const clock = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" })
const day = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" })

// until says when a rest ends: the hour today, the day and the hour after that.
export function until(iso: string, now = Date.now()) {
  const at = new Date(iso)
  const sameDay = new Date(now).toDateString() === at.toDateString()

  return sameDay ? clock.format(at) : `${day.format(at)}, ${clock.format(at)}`
}

// resting reports whether an account waits for a usage limit to end.
export function resting(account: Account, now = Date.now()) {
  return account.cooling_until !== undefined && new Date(account.cooling_until).getTime() > now
}

// accountState is the one thing wrong with an account, the worst first: off, needing a sign-in, resting. An
// account in use with nothing wrong has none (9router shows a status badge a connection; Orca "Needs re-auth").
export function accountState(account: Account, now = Date.now()): AccountState {
  if (account.status === "disabled") return { label: "Off", tone: "outline" }

  if (account.sign_in === "login_required") {
    return {
      label: "Needs sign-in",
      tone: "destructive",
      why: "Its sign-in was refused for good.",
    }
  }

  if (account.sign_in === "no_tokens") {
    return { label: "No sign-in", tone: "destructive", why: "It has no tokens here." }
  }

  if (resting(account, now)) {
    return {
      label: `Resting until ${until(account.cooling_until!, now)}`,
      tone: "warning",
      why: account.cooling_reason,
    }
  }

  return null
}

// serves reports whether the gateway can use an account now.
export function serves(account: Account, now = Date.now()) {
  return accountState(account, now) === null
}

// subscriptionState is a subscription's badge: whether any of its accounts can serve now, and if none why.
export function subscriptionState(
  accounts: Account[],
  now = Date.now(),
): { label: string; tone: Tone } {
  if (accounts.length === 0) return { label: "No account", tone: "warning" }
  if (accounts.some((account) => serves(account, now))) return { label: "Serving", tone: "success" }
  if (accounts.every((account) => account.status === "disabled"))
    return { label: "Off", tone: "outline" }
  if (accounts.some((account) => account.status === "active" && resting(account, now))) {
    return { label: "Resting", tone: "warning" }
  }

  return { label: "Needs sign-in", tone: "destructive" }
}

// inUse reports whether a subscription's switch is on: any of its accounts is in use (9router's card toggle is
// off when every connection of the provider is).
export function inUse(accounts: Account[]) {
  return accounts.some((account) => account.status === "active")
}

// offered are the models agents may name: the ones a provider that is the gateway lists.
export function offered(providers: Provider[]): Set<string> {
  return new Set(
    providers
      .filter((provider) => provider.kind === "model_gateway")
      .flatMap((provider) => provider.models),
  )
}

export type Route = {
  model: GatewayModel
  // offered: a profile or a Bot may name it.
  offered: boolean
  targets: { provider: string; kind: string; upstream: string }[]
}

// routesOf are the gateway's models with what each target's provider is.
export function routesOf(models: GatewayModel[], providers: Provider[]): Route[] {
  const kinds = new Map(providers.map((provider) => [provider.name, provider.kind as string]))
  const names = offered(providers)

  return models.map((model) => ({
    model,
    offered: names.has(model.name),
    targets: model.targets.map((target) => ({
      provider: target.provider,
      kind: kinds.get(target.provider) ?? "",
      upstream: target.upstream_model,
    })),
  }))
}
