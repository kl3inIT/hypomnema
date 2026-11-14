import type {
  WebSearchKey,
  WebSearchKeyedProvider,
  WebSearchLink,
  WebSearchRest,
  WebSearchUsage,
} from "@/api/generated/types.gen"

import { type Tone, until } from "./models-state"

// What the Web search page shows of each way a search is asked (issues #354, #393), worked out from the chain the
// server lists in order. The words are the references': a keyless provider is "Public, no key" (OpenClaw's "No
// separate API key required", OmniRoute's Free badge), one that can be asked is "Ready" (9router's badge for a
// keyless provider), one that is not "needs" what it lacks (OpenClaw's "Needs setup"), and a subscription's search
// is "from your account" (OpenClaw's "Saved account"). What is used is search-rotation's dashboard: the provider's
// balance where it tells one, else this control plane's calls, and "allowance unknown" where nothing is known
// (docs/provider-access.md).

// The name of each provider as its maker writes it.
const names: Record<WebSearchLink["provider"], string> = {
  exa: "Exa",
  firecrawl: "Firecrawl",
  parallel: "Parallel",
  tavily: "Tavily",
  tinyfish: "TinyFish",
  you: "You.com",
  codex: "Codex",
  claude: "Claude",
  antigravity: "Antigravity",
}

export function providerName(provider: WebSearchLink["provider"]) {
  return names[provider]
}

// Where each keyed provider gives its keys.
export const keyPages: Record<WebSearchKeyedProvider, string> = {
  exa: "https://dashboard.exa.ai/api-keys",
  firecrawl: "https://www.firecrawl.dev/app/api-keys",
  parallel: "https://platform.parallel.ai",
  tavily: "https://app.tavily.com",
  tinyfish: "https://agent.tinyfish.ai/api-keys",
}

export function isKeyed(provider: WebSearchLink["provider"]): provider is WebSearchKeyedProvider {
  return provider in keyPages
}

// sourceText says where a way's search comes from.
export function sourceText(link: WebSearchLink) {
  switch (link.source) {
    case "key":
      return "With your API keys"
    case "public":
      return "Public, no key"
    case "subscription":
      return `From your ${providerName(link.provider)} account`
  }
}

export type LinkState = { label: string; tone: Tone; why: string }

// What each rest is, in words, and why it rests.
const rests: Record<WebSearchRest, { label: string; why: string }> = {
  rate_limit: {
    label: "Rate limited",
    why: "It answered 429 (rate limited) and is skipped until then.",
  },
  no_credits: {
    label: "Out of credits",
    why: "It said its credits are spent. It is asked again after each rest, 5 minutes doubling up to an hour, so a refilled key comes back by itself.",
  },
  refused: {
    label: "Refused",
    why: "It refused the key (401 or 403). Check the key, or remove it.",
  },
  daily_limit: {
    label: "Daily limit",
    why: "It reached its daily limit and is asked again at 00:00 UTC.",
  },
}

function restState(rest: WebSearchRest | undefined, restingUntil: string | undefined, now: number) {
  const words = rests[rest ?? "rate_limit"]

  return {
    label: restingUntil ? `${words.label} until ${until(restingUntil, now)}` : words.label,
    tone: (rest === "refused" ? "destructive" : "warning") as Tone,
    why: words.why,
  }
}

// linkState is the badge of a way: what it is now, and why when pointed at.
export function linkState(link: WebSearchLink, now = Date.now()): LinkState {
  switch (link.state) {
    case "off":
      return {
        label: "Off",
        tone: "outline",
        why: "Switched off: a search neither tries it nor may name it.",
      }
    case "resting":
      return restState(link.rest, link.resting_until, now)
    case "not_set_up":
      return missing(link)
    case "ready":
      return { label: "Ready", tone: "success", why: "A search asks it in its turn." }
  }
}

// keyState is the badge of one of a provider's keys.
export function keyState(key: WebSearchKey, now = Date.now()): LinkState {
  switch (key.state) {
    case "resting":
      return restState(key.rest, key.resting_until, now)
    case "spent":
      return {
        label: "Spent",
        tone: "warning",
        why: "Its balance, as its provider tells it, is used up. It is skipped until the balance comes back.",
      }
    case "ready":
      return { label: "Ready", tone: "success", why: "A search asks it in its turn." }
  }
}

// missing says what a way that cannot be asked lacks.
function missing(link: WebSearchLink): LinkState {
  switch (link.source) {
    case "key":
      return {
        label: "No key",
        tone: "outline",
        why: `${providerName(link.provider)} is asked with your keys once one is in the vault.`,
      }
    case "subscription":
      return {
        label: "No account",
        tone: "outline",
        why: `No ${providerName(link.provider)} account is signed in here and in use. Add one on the Models page.`,
      }
    case "public":
      return {
        label: "Not available",
        tone: "outline",
        why: "It cannot be asked on this control plane.",
      }
  }
}

const count = new Intl.NumberFormat()
const dollars = new Intl.NumberFormat(undefined, {
  style: "currency",
  currency: "USD",
  maximumFractionDigits: 3,
})

// callsText is this control plane's calls today and this month (UTC), failures said only when there are some.
export function callsText(usage: WebSearchUsage) {
  const part = (label: string, t: WebSearchUsage["today"]) =>
    `${label} ${count.format(t.answered)}${t.failed > 0 ? ` (+${count.format(t.failed)} failed)` : ""}`

  return `${part("Today", usage.today)} · ${part("this month", usage.month)}`
}

// spendText is what the month's calls cost, as the provider said; nothing when it said nothing.
export function spendText(usage: WebSearchUsage) {
  return usage.month_dollars ? `${dollars.format(usage.month_dollars)} this month` : undefined
}

// balanceShare is how much of a key's balance is used, in percent; undefined when its provider tells none.
export function balanceShare(key: WebSearchKey) {
  const b = key.balance
  if (!b || b.limit <= 0) return undefined

  return Math.min(100, Math.max(0, (b.used / b.limit) * 100))
}

export function balanceText(key: WebSearchKey) {
  const b = key.balance
  if (!b) return undefined

  return `${count.format(b.remaining)} of ${count.format(b.limit)} credits left`
}

// moved is the keyed providers' order after one is dropped on another: it takes that one's place, as
// search-rotation's queueReorder splices it (static/app.js).
export function moved<T>(order: T[], from: T, to: T) {
  const i = order.indexOf(from)
  const j = order.indexOf(to)
  if (i < 0 || j < 0 || i === j) return order

  const out = [...order]
  out.splice(j, 0, ...out.splice(i, 1))

  return out
}
