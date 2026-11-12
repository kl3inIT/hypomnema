import type { Bot, BotSettings, Model } from "@/api/generated/types.gen"
import { modelKey, splitModel } from "@/features/agents/profiles-state"

// What the Bots pages say about a Bot, apart from how they draw it.

// firstBot is the Bot that is always there and that a chat talks with when none is named.
export const firstBot = "coordinator"

// A Bot's name follows the server's rule (orchestration/bots.go, PutBot): lowercase letters, digits and dashes,
// starting with a letter, at most 63.
export const namePattern = /^[a-z][a-z0-9-]{0,62}$/

// modelLine is a Bot's model as a card says it: the name, or that pi chooses.
export function modelLine(bot: Pick<Bot, "model">) {
  return bot.model || "pi's default model"
}

// memoryEntries is a Bot's memory as a list: one entry a line.
export function memoryEntries(memory: string) {
  return memory
    .split("\n")
    .map((entry) => entry.trim())
    .filter(Boolean)
}

// memoryUsed counts the memory's characters as the server does: Unicode code points, the newlines between
// entries included.
export function memoryUsed(memory: string) {
  return [...memory].length
}

// memoryLine says how full the memory is: "120 of 2,200 characters".
export function memoryLine(bot: Pick<Bot, "memory" | "memory_limit">) {
  return `${memoryUsed(bot.memory).toLocaleString("en")} of ${bot.memory_limit.toLocaleString("en")} characters`
}

// A Draft is a Bot as its form holds it. model is "provider/name", a bare name, or "" for pi's default.
export type Draft = { name: string; model: string; effort: string; persona: string }

export const emptyDraft: Draft = { name: "", model: "", effort: "", persona: "" }

export function draftOf(bot: Bot): Draft {
  return {
    name: bot.name,
    model: modelKey(bot.provider ?? "", bot.model ?? ""),
    effort: bot.effort ?? "",
    persona: bot.persona,
  }
}

export function sameDraft(a: Draft, b: Draft) {
  return (
    a.name === b.name && a.model === b.model && a.effort === b.effort && a.persona === b.persona
  )
}

// settingsOf is the draft as PUT /bots/{name} takes it. The server replaces all four, so what the draft leaves
// empty is left out and falls back to pi's default.
export function settingsOf(draft: Draft, models: Model[]): BotSettings {
  const model = draft.model ? splitModel(draft.model, models) : { provider: "", name: "" }

  return {
    ...(model.provider && { provider: model.provider }),
    ...(model.name && { model: model.name }),
    ...(draft.effort && { effort: draft.effort }),
    persona: draft.persona.trim(),
  }
}

// problem says what keeps a new Bot from being made, in the form's own words; the server checks the rest.
export function problem(draft: Draft, taken: string[]): string | null {
  const name = draft.name.trim()

  if (!name) return "Give the Bot a name."
  if (!namePattern.test(name)) {
    return "Use lowercase letters, digits and dashes, starting with a letter; at most 63."
  }
  if (taken.includes(name)) return "A Bot has this name already."

  return null
}
