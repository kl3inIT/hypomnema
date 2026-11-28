import type { Account, AccountLimits, CoolingCode, Provider } from "@/api/generated/types.gen"

// What the Models page shows, worked out from what the server lists (issue #199). The references that keep
// subscription accounts put an account under its provider (9router's providers/[id]/ConnectionRow.js, OpenClaw's
// pages/model-providers, Orca's settings/accounts); the server lists accounts and providers apart, so they are
// joined here by the runtime an account signs in for.

// The runtime whose accounts a subscription's provider runs under.
const runtimes: Record<string, string> = {
  claude_subscription: "claude",
  codex_subscription: "codex",
  antigravity_subscription: "antigravity",
  opencode_go: "opencode_go",
  opencode_zen: "opencode_zen",
}

// The name of a subscription as its vendor calls the product, and what kind of account it takes.
const subscriptions: Record<string, { name: string; account: string }> = {
  claude_subscription: { name: "Claude", account: "Claude subscription" },
  codex_subscription: { name: "Codex", account: "ChatGPT subscription" },
  antigravity_subscription: { name: "Antigravity", account: "Google subscription" },
  opencode_go: { name: "OpenCode Go", account: "OpenCode Go key" },
  opencode_zen: { name: "OpenCode Zen", account: "OpenCode Zen key" },
}

export type Subscription = {
  provider: Provider
  name: string
  // account is what kind of account signs in, such as "ChatGPT subscription".
  account: string
  // runtime is what its accounts sign in for: claude, codex, antigravity, opencode_go or opencode_zen.
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

// action is what the owner does to end a rest that does not end by itself (issue #1335, row 4).
export type AccountState = { label: string; tone: Tone; why?: string; action?: string } | null

const clock = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" })
const day = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" })

// until says when a rest ends: the hour today, the day and the hour after that.
export function until(iso: string, now = Date.now()) {
  const at = new Date(iso)
  const sameDay = new Date(now).toDateString() === at.toDateString()

  return sameDay ? clock.format(at) : `${day.format(at)}, ${clock.format(at)}`
}

// Why an account rests, in words (issue #1305): the code the server gives, the provider's own words staying for the
// badge's title. OmniRoute and 9router-go show such a status on the connection (rate_limited, quota_exhausted,
// 9router-go internal/providers/connection_status.go); the words are the owner's (issue #1305, row 6).
const restWords: Record<CoolingCode, string> = {
  usage_limit: "Usage limit",
  rate_limit: "Rate limited",
  billing: "Credits spent",
  auth: "Key refused",
  overloaded: "Provider overloaded",
  unknown: "Resting",
}

// restLabel says why an account rests and when that ends: "Usage limit, resets 12:42 AM", "Rate limited until
// 12:42 AM", "Resting until 12:42 AM" when nothing says why.
export function restLabel(code: CoolingCode | undefined, iso: string, now = Date.now()) {
  const at = until(iso, now)

  if (code === "usage_limit") return `${restWords.usage_limit}, resets ${at}`

  return `${restWords[code ?? "unknown"]} until ${at}`
}

// restTone is how loud a rest is: a refused key is the owner's to fix, the rest pass by themselves.
export function restTone(code: CoolingCode | undefined): Tone {
  return code === "auth" ? "destructive" : "warning"
}

// restAction is what the owner does when only the owner can end a rest (issue #1335, row 4): a refused sign-in is
// signed in again, spent credits are added; the gateway rests such an account for the usage wait, since trying it
// again changes nothing. OpenClaw keeps a billing failure off until the owner acts
// (docs/research/2026-11-13-gateway-features.md:70-72). An OpenCode key is not signed in again: a new key is a new
// account (issues #854, #965).
export function restAction(code: CoolingCode | undefined, runtime: string) {
  if (code === "auth") {
    return runtime === "opencode_go" || runtime === "opencode_zen"
      ? "Add a new key"
      : "Sign in again"
  }
  if (code === "billing") return "Add credits"

  return undefined
}

// resting reports whether an account waits for a usage limit to end.
function resting(account: Account, now = Date.now()) {
  return account.cooling_until !== undefined && new Date(account.cooling_until).getTime() > now
}

// accountState is the one thing wrong with an account, the worst first: off, needing a sign-in, needing Google's
// verification, resting. An
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

  // Google wants it verified (issue #822): the gateway passes it over until its next try. The row says when, with
  // Google's link (opencode-antigravity-auth's "[needs verification]", src/tui.ts:101).
  if (account.verification) {
    return {
      label: "Needs verification",
      tone: "destructive",
      why: account.verification.message ?? "Google asks for the account to be verified.",
    }
  }

  if (resting(account, now)) {
    return {
      label: restLabel(account.cooling_code, account.cooling_until!, now),
      tone: restTone(account.cooling_code),
      why: account.cooling_reason,
      action: restAction(account.cooling_code, account.runtime),
    }
  }

  return null
}

// tryDue reports whether the next try of an account Google wants verified has come: the gateway may use it again.
export function tryDue(verification: { next_try_at: string }, now = Date.now()) {
  return new Date(verification.next_try_at).getTime() <= now
}

// serves reports whether the gateway can use an account now.
function serves(account: Account, now = Date.now()) {
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
  if (accounts.some((account) => account.status === "active" && account.verification)) {
    return { label: "Needs verification", tone: "destructive" }
  }

  return { label: "Needs sign-in", tone: "destructive" }
}

// inUse reports whether a subscription's switch is on: any of its accounts is in use (9router's card toggle is
// off when every connection of the provider is).
export function inUse(accounts: Account[]) {
  return accounts.some((account) => account.status === "active")
}

// limitsWhy says why an account's limits have no windows, or why the windows shown are an earlier reading (issue
// #1198): the vendor asked to wait, until when; it refused the token, which a new sign-in replaces, as CLIProxyAPI
// says "unauthorized" with the account and 9router-go stops dialling it; or the call failed. The server's words
// are its detail, shown on hover. Nothing when the reading is fresh.
export function limitsWhy(limits: AccountLimits, now = Date.now()): string | null {
  const earlier = limits.windows.length > 0
  switch (limits.unavailable) {
    case undefined:
    case "not_given":
    case "sign_in":
      return null
    case "resting":
      return limits.retry_at
        ? `The vendor asked to wait: read again at ${until(limits.retry_at, now)}.`
        : "The vendor asked to wait: read again in a few minutes."
    case "refused":
      return "The vendor refused its token: sign it in again. The account may be disabled."
    case "verification":
      return "Google asks for the account to be verified."
    default:
      return earlier ? "Reading them again failed." : "Its limits could not be read."
  }
}
