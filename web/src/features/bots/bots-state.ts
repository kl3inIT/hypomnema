import { modelName } from "@/lib/model-name"
import type { AllowList, Bot, BotSettings, Model } from "@/api/generated/types.gen"
import { modelKey, splitModel } from "@/features/agents/profiles-state"

// What the Bots pages say about a Bot, apart from how they draw it.

// mainBotOf is the id of the main Bot among those read, the one that is always there and that a chat talks with
// when none is named; "" before they are read. The server says which it is (Bot.main), so nothing here names it.
export function mainBotOf(bots: Pick<Bot, "id" | "main">[] | undefined) {
  return bots?.find((bot) => bot.main)?.id ?? ""
}

// botNameOf is what the Bot of an id is called (issue #466): its name, or its id when it is not among those read.
export function botNameOf(bots: Pick<Bot, "id" | "name">[] | undefined, id: string) {
  return bots?.find((bot) => bot.id === id)?.name ?? id
}

// maxName is the longest a Bot's name may be, as the server's rule (orchestration/bots.go, botName): trimmed, 1 to
// 64 characters, any letters.
const maxName = 64

// defaultBotModel is the model of a Bot that names none (owner, 2026-11-15): Claude Opus 5.5 under the Claude
// subscription, which pi is given as its default (clientcli pisettings.go, piDefaultModel).
export const defaultBotModel = "claude-opus-5-5"

// defaultModelLabel says which model a Bot with none runs on.
export const defaultModelLabel = `Default (${modelName(defaultBotModel)})`

// modelLine is a Bot's model as a card says it: the name, or the default.
export function modelLine(bot: Pick<Bot, "model">) {
  return bot.model ? modelName(bot.model) : defaultModelLabel
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
// skills and skillsOff are the skills the Bot adds and the ones for everyone it turns off (issue #21).
export type Draft = {
  name: string
  model: string
  effort: string
  persona: string
  skills: string[]
  skillsOff: string[]
}

export const emptyDraft: Draft = {
  name: "",
  model: "",
  effort: "",
  persona: "",
  skills: [],
  skillsOff: [],
}

export function draftOf(bot: Bot): Draft {
  return {
    name: bot.name,
    model: modelKey(bot.provider ?? "", bot.model ?? ""),
    effort: bot.effort ?? "",
    persona: bot.persona,
    skills: bot.skills,
    skillsOff: bot.skills_off,
  }
}

export function sameDraft(a: Draft, b: Draft) {
  return (
    a.name === b.name &&
    a.model === b.model &&
    a.effort === b.effort &&
    a.persona === b.persona &&
    a.skills.join() === b.skills.join() &&
    a.skillsOff.join() === b.skillsOff.join()
  )
}

// settingsOf is the draft as PUT /bots/{bot_id} and POST /bots take it. The server replaces what it is given, so what the draft
// leaves empty is left out and falls back to pi's default; the skills are always given, the form holds them.
export function settingsOf(draft: Draft, models: Model[]): BotSettings {
  const model = draft.model ? splitModel(draft.model, models) : { provider: "", name: "" }

  return {
    ...(model.provider && { provider: model.provider }),
    ...(model.name && { model: model.name }),
    ...(draft.effort && { effort: draft.effort }),
    persona: draft.persona.trim(),
    skills: draft.skills,
    skills_off: draft.skillsOff,
  }
}

// nameProblem says what keeps a name from being a Bot's, in the form's own words; the server checks the rest.
export function nameProblem(name: string, taken: string[]): string | null {
  const trimmed = name.trim()

  if (!trimmed) return "Give the Bot a name."
  if ([...trimmed].length > maxName) return `A name is at most ${maxName} characters.`
  if (taken.includes(trimmed)) return "A Bot has this name already."

  return null
}

// problem says what keeps a new Bot from being made.
export function problem(draft: Draft, taken: string[]): string | null {
  return nameProblem(draft.name, taken)
}

// keptSettings is a Bot's stored model and persona as PUT /bots/{bot_id} takes them, so that a change of one list
// replaces nothing else; the skills and the other list are left out and the server keeps them.
export function keptSettings(bot: Bot): BotSettings {
  return {
    ...(bot.provider && { provider: bot.provider }),
    ...(bot.model && { model: bot.model }),
    ...(bot.effort && { effort: bot.effort }),
    persona: bot.persona,
  }
}

// allowed is what a Bot's list names that is not among the items shown: kept on the list, though gone since, as
// OpenMausBot keeps the scope of an app no longer connected.
export function allowed(list: AllowList, items: { name: string }[]): string[] {
  if (!list.limited) return []

  return list.names.filter((name) => !items.some((item) => item.name === name))
}

// withName is a list's names with one switched on or off, in the order they were added.
export function withName(names: string[], name: string, on: boolean): string[] {
  const rest = names.filter((n) => n !== name)

  return on ? [...rest, name] : rest
}
