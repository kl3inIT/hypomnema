import type { ImageProvider, ImageProviderLink } from "@/api/generated/types.gen"

import { restLabel, restTone, type Tone } from "./models-state"

// What the Images page shows of each image provider (issue #431), worked out from the list the server gives in
// order. The words follow the Web search page's: a provider that can be asked is "Ready", one with no account it
// can use has "No account", one that rests says until when, and what was made is this control plane's own count.

const names: Record<ImageProvider, string> = { codex: "Codex", antigravity: "Antigravity" }

export function imageProviderName(provider: ImageProvider) {
  return names[provider]
}

export type ImageState = { label: string; tone: Tone; why: string }

// imageState is a provider's badge: what it is now, and why when pointed at.
export function imageState(link: ImageProviderLink, now = Date.now()): ImageState {
  switch (link.state) {
    case "off":
      return {
        label: "Off",
        tone: "outline",
        why: "Switched off: a request neither asks it nor may name it.",
      }
    case "resting":
      // Why the account free first rests (issue #1305); a rate limit when the server does not say.
      return {
        label: link.resting_until
          ? restLabel(link.cooling_code ?? "rate_limit", link.resting_until, now)
          : "Rate limited",
        tone: restTone(link.cooling_code),
        // The words of the account free first, as the account's card shows them (issue #1305).
        why:
          link.cooling_reason ??
          "Each of its accounts reached its limit and rests until the time its vendor named.",
      }
    case "not_set_up":
      return {
        label: "No account",
        tone: "outline",
        why:
          link.provider === "codex"
            ? "No Codex account on a paid ChatGPT plan is signed in here and in use: the free plan makes no images. Add one on the Models page."
            : `No ${imageProviderName(link.provider)} account is signed in here and in use. Add one on the Models page.`,
      }
    case "ready":
      return { label: "Ready", tone: "success", why: "A request asks it in its turn." }
  }
}

const count = new Intl.NumberFormat()

// madeText is this control plane's requests today and this month (UTC), failures said only when there are some.
export function madeText(usage: ImageProviderLink["usage"]) {
  const part = (label: string, t: ImageProviderLink["usage"]["today"]) =>
    `${label} ${count.format(t.answered)}${t.failed > 0 ? ` (+${count.format(t.failed)} failed)` : ""}`

  return `${part("Today", usage.today)} · ${part("this month", usage.month)}`
}

// accountsText says how many of its accounts can make images.
export function accountsText(link: ImageProviderLink) {
  return link.accounts === 1 ? "1 account" : `${count.format(link.accounts)} accounts`
}
