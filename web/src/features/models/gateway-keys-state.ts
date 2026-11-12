import type { GatewayModel, Provider } from "@/api/generated/types.gen"
import { modelName } from "@/lib/model-name"

// A model as the key's picker shows it: its name to read, its id, and the provider that serves it first.
export type PickModel = { id: string; name: string; provider: string; kind: string }

export type PickGroup = { provider: string; kind: string; title: string; models: PickModel[] }

// providerTitle is how a group is headed: a subscription by its product, any other provider by its name.
function providerTitle(provider: string, kind: string) {
  if (kind === "claude_subscription") return "Claude subscription"
  if (kind === "codex_subscription") return "Codex subscription"
  if (kind === "antigravity_subscription") return "Antigravity"

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
