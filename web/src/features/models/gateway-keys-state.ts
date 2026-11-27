import { AudioLines, FileText, Globe, ImageIcon, type LucideIcon } from "lucide-react"

import type {
  GatewayEndpoint,
  GatewayKey,
  GatewayModel,
  Provider,
  WebSearchProvider,
} from "@/api/generated/types.gen"
import { modelName } from "@/lib/model-name"

// A model as the key's picker shows it: its name to read, its id, and the provider that serves it first.
export type PickModel = { id: string; name: string; provider: string; kind: string }

export type PickGroup = { provider: string; kind: string; title: string; models: PickModel[] }

// providerTitle is how a group is headed: a subscription by its product, any other provider by its name.
function providerTitle(provider: string, kind: string) {
  if (kind === "claude_subscription") return "Claude subscription"
  if (kind === "codex_subscription") return "Codex subscription"
  if (kind === "antigravity_subscription") return "Antigravity"
  if (kind === "opencode_go") return "OpenCode Go"
  if (kind === "opencode_zen") return "OpenCode Zen"

  return provider
}

// pickGroups puts each model under the provider of its first target, the one the gateway tries first, in the
// order the providers come.
export function pickGroups(models: GatewayModel[], providers: Provider[]): PickGroup[] {
  const kindOf = new Map(providers.map((p) => [p.name, p.kind] as const))
  const groups = new Map<string, PickGroup>()

  for (const model of models) {
    const provider = model.targets[0]?.provider ?? "other"
    const kind = kindOf.get(provider) ?? "openai_compatible"
    const group = groups.get(provider) ?? {
      provider,
      kind,
      title: providerTitle(provider, kind),
      models: [],
    }

    group.models.push({ id: model.name, name: modelName(model.name), provider, kind })
    groups.set(provider, group)
  }

  return [...groups.values()]
}

// A key's own routing (issue #1277): the only accounts it may use, and its own web search order. Every key starts
// with every account and the order of Settings, as Bifrost's "*" and OmniRoute's empty allowed_connections do.
export type Routing = {
  accounts: "every" | "some"
  chosen: string[]
  order: "settings" | "own"
  providers: WebSearchProvider[]
}

// routingOf is what a key has set of its routing, for its form; a new key's is the default.
export function routingOf(key?: GatewayKey): Routing {
  return {
    accounts: key?.accounts ? "some" : "every",
    chosen: key?.accounts?.map((account) => account.id) ?? [],
    order: key?.search_order.length ? "own" : "settings",
    providers: key?.search_order ?? [],
  }
}

// routingSummary is the Routing section's header when it is closed: what is set, or that the key follows Settings,
// as new-api's and LiteLLM's collapsed settings say what they hold.
export function routingSummary(routing: Routing) {
  const parts: string[] = []
  if (routing.accounts === "some") {
    const n = routing.chosen.length
    parts.push(n === 1 ? "1 account" : `${n} accounts`)
  }
  if (routing.order === "own") parts.push("own search order")

  return parts.length ? parts.join(" · ") : "like Settings"
}

// The name of a subscription's accounts by their runtime, for the keys table.
const runtimeNames: Record<string, string> = {
  claude: "Claude",
  codex: "Codex",
  antigravity: "Antigravity",
  opencode_go: "OpenCode Go",
  opencode_zen: "OpenCode Zen",
}

// routingBadges mark a key with its own routing under its name in the keys table: how many accounts of each
// subscription it is limited to, and that it searches in its own order. Nothing for a key that follows Settings.
export function routingBadges(key: GatewayKey): string[] {
  const badges: string[] = []

  if (key.accounts) {
    if (key.accounts.length === 0) badges.push("No account")

    const counts = new Map<string, number>()
    for (const account of key.accounts) {
      counts.set(account.runtime, (counts.get(account.runtime) ?? 0) + 1)
    }
    for (const [runtime, n] of counts) {
      badges.push(`${n} ${runtimeNames[runtime] ?? runtime} account${n === 1 ? "" : "s"}`)
    }
  }

  if (key.search_order.length) badges.push("Own search order")

  return badges
}

// sameAccounts reports whether a form keeps the accounts a key was limited to, or keeps it on every account.
export function sameAccounts(routing: Routing, was: Routing) {
  if (routing.accounts !== was.accounts) return false
  if (routing.accounts === "every") return true

  return (
    routing.chosen.length === was.chosen.length &&
    routing.chosen.every((id) => was.chosen.includes(id))
  )
}

function sameOrder(routing: Routing, was: Routing) {
  if (routing.order !== was.order) return false

  return (
    routing.order === "settings" ||
    (routing.providers.length === was.providers.length &&
      routing.providers.every((p, i) => was.providers[i] === p))
  )
}

// routingBody is what a key's form sends of its routing. A new key leaves out what it does not set, so that it
// follows Settings; an edit sends only what changed, "*" being every account again and an empty order the order of
// Settings, so that a key whose accounts were all removed keeps its limit while it is renamed.
export function routingBody(
  routing: Routing,
  key?: GatewayKey,
): { accounts?: string[]; search_order?: WebSearchProvider[] } {
  const own = routing.accounts === "some"
  const ordered = routing.order === "own"

  if (!key) {
    return {
      ...(own && { accounts: routing.chosen }),
      ...(ordered && { search_order: routing.providers }),
    }
  }

  const was = routingOf(key)

  return {
    ...(!sameAccounts(routing, was) && { accounts: own ? routing.chosen : ["*"] }),
    ...(!sameOrder(routing, was) && { search_order: ordered ? routing.providers : [] }),
  }
}

// connectSnippet is what a tool needs to reach the gateway with a new key, as LiteLLM's created key gives its
// agent instructions (ui/litellm-dashboard/src/components/shared/CreatedKeyDisplay.tsx buildAgentInstructions, MIT):
// the base URL and the key for a tool of OpenAI's format and for Claude Code; Connect a tool writes the rest.
export function connectSnippet(origin: string, secret: string) {
  return [
    `# A tool of OpenAI's format`,
    `OPENAI_BASE_URL=${origin}/v1`,
    `OPENAI_API_KEY=${secret}`,
    ``,
    `# Claude Code`,
    `ANTHROPIC_BASE_URL=${origin}`,
    `ANTHROPIC_AUTH_TOKEN=${secret}`,
  ].join("\n")
}

// What a key may use besides chat (issue #664), in the order shown: one row of small chips (issue #1277), each with
// the icon of its page in the Settings menu (settings-page.tsx), and the endpoints in one hint line.
export const endpointKinds: {
  id: GatewayEndpoint
  title: string
  shortLabel: string
  icon: LucideIcon
}[] = [
  { id: "search", title: "Web search", shortLabel: "Search", icon: Globe },
  { id: "fetch", title: "Reading a web page", shortLabel: "Read pages", icon: FileText },
  { id: "images", title: "Making and editing images", shortLabel: "Images", icon: ImageIcon },
  {
    id: "audio",
    title: "Speech to text and text to speech",
    shortLabel: "Audio",
    icon: AudioLines,
  },
]
