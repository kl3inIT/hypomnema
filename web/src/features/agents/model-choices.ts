import type { Model } from "@/api/generated/types.gen"

import { modelName } from "@/lib/model-name"

import { modelKey } from "./profiles-state"

// A Choice is one row: a model of a provider, the default (key ""), or a model no provider offers any more.
// name is what the row says (lib/model-name.ts) and id the model's id, "" for the default.
export type Choice = { key: string; id: string; name: string; provider: string; gone?: boolean }

export type Group = { value: string; kind: string; items: Choice[] }

// choices lists the default first, then a model that is chosen but no longer offered, then every provider's
// models under the provider's name.
export function choices(models: Model[], model: string, defaultLabel: string): Group[] {
  const top: Choice[] = [{ key: "", id: "", name: defaultLabel, provider: "" }]
  const known = model === "" || models.some((m) => modelKey(m.provider, m.name) === model)
  if (!known) top.push({ key: model, id: model, name: modelName(model), provider: "", gone: true })

  const providers = [...new Set(models.map((m) => m.provider))].sort()

  return [
    { value: "", kind: "", items: top },
    ...providers.map((provider) => ({
      value: provider,
      kind: models.find((m) => m.provider === provider)?.kind ?? "",
      items: models
        .filter((m) => m.provider === provider)
        .map((m) => ({
          key: modelKey(m.provider, m.name),
          id: m.name,
          name: modelName(m.name),
          provider,
        })),
    })),
  ]
}

// matches keeps a row when every word typed is found in its name, its id or its provider, as t3code's search asks each
// token to match a field (MIT License, https://github.com/pingdotgg/t3code;
// packages/client-runtime/src/modelSearch.ts, scoreModelPickerSearch). Its ranking by score is left out: the list
// keeps its order by provider.
export function matches(choice: Choice, query: string) {
  const text = `${choice.name} ${choice.id} ${choice.provider}`.toLowerCase()

  return query
    .toLowerCase()
    .split(/\s+/)
    .every((word) => text.includes(word))
}
