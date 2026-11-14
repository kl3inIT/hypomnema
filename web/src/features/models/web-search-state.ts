import type { WebSearchLink } from "@/api/generated/types.gen"

import { type Tone, until } from "./models-state"

// What the Web search page shows of each way a search is asked (issue #354), worked out from the chain the server
// lists in order. The words are the references': a keyless provider is "Public, no key" (OpenClaw's "No separate
// API key required", OmniRoute's Free badge), one that can be asked is "Ready" (9router's badge for a keyless
// provider), one that is not "needs" what it lacks (OpenClaw's "Needs setup"), and a subscription's search is "from
// your account" (OpenClaw's "Saved account").

// The name of each provider as its maker writes it.
const names: Record<WebSearchLink["provider"], string> = {
  exa: "Exa",
  searxng: "SearXNG",
  parallel: "Parallel",
  codex: "Codex",
  claude: "Claude",
  antigravity: "Antigravity",
}

export function providerName(provider: WebSearchLink["provider"]) {
  return names[provider]
}

// sourceText says where a way's search comes from.
export function sourceText(link: WebSearchLink) {
  switch (link.source) {
    case "key":
      return "With your API key"
    case "self_hosted":
      return "Self-hosted"
    case "public":
      return "Public, no key"
    case "subscription":
      return `From your ${providerName(link.provider)} account`
  }
}

export type LinkState = { label: string; tone: Tone; why: string }

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
      return {
        label: link.resting_until ? `Resting until ${until(link.resting_until, now)}` : "Resting",
        tone: "warning",
        why: "It answered 429 (rate limited) and is skipped until then.",
      }
    case "not_set_up":
      return missing(link)
    case "ready":
      return { label: "Ready", tone: "success", why: "A search asks it in its turn." }
  }
}

// missing says what a way that cannot be asked lacks.
function missing(link: WebSearchLink): LinkState {
  switch (link.source) {
    case "key":
      return {
        label: "Needs a key",
        tone: "outline",
        why: "Exa is asked through its public MCP until its API key is in the vault.",
      }
    case "self_hosted":
      return {
        label: "No address",
        tone: "outline",
        why: "SearXNG is asked once its address is set.",
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

// shareSwitch says a way shares its provider's switch with another way in the chain: Exa with and without its key.
export function shareSwitch(chain: WebSearchLink[], link: WebSearchLink) {
  return chain.filter((other) => other.provider === link.provider).length > 1
}
