import { useState } from "react"

import type { Bot, GatewayModel, GatewayModelEffort, Model } from "@/api/generated/types.gen"
import { effortLabel, modelKey } from "@/features/agents/profiles-state"
import { effortLevels } from "@/features/models/gateway-models-state"

// What a chat runs with (issue #369): its own model and effort, each empty when it is its Bot's. The chat's
// composer and the New chat page choose them; the server keeps them on the chat.
export type ChatChoice = { provider: string; model: string; effort: string }

export const botsChoice: ChatChoice = { provider: "", model: "", effort: "" }

// The kinds of provider pi reaches its model through, as the server checks a chat's model
// (orchestration/profiles.go, runtimeProviders["pi"]).
const piKinds = ["model_gateway", "openai_compatible", "anthropic"]

// piModels are the models a chat may choose: those of a provider pi can use.
export function piModels(models: Model[]) {
  return models.filter((m) => piKinds.includes(m.kind))
}

// running is what the chat runs with: its own model and effort, and its Bot's for what it did not choose.
export function running(
  choice: ChatChoice,
  bot: Pick<Bot, "provider" | "model" | "effort"> | undefined,
) {
  const own = choice.model !== ""

  return {
    provider: own ? choice.provider : (bot?.provider ?? ""),
    model: own ? choice.model : (bot?.model ?? ""),
    effort: choice.effort || bot?.effort || "",
  }
}

// levelsOf are the effort levels a model offers, in their order, as the gateway gives its attributes (issue #367);
// none for a model that does not reason, or one the gateway does not serve. t3code's TraitsPicker shows no control
// for a trait a model lacks.
export function levelsOf(model: string, gateway: GatewayModel[]): GatewayModelEffort[] {
  const attributes = gateway.find((m) => m.name === model)?.attributes
  if (!attributes?.reasoning.value) return []

  const offered = attributes.efforts.value ?? []

  return effortLevels.filter((level) => offered.includes(level))
}

// levelLabel is a level as the composer says it: the Agents pages' words, and the two levels a profile cannot name.
export function levelLabel(level: string) {
  if (level === "none") return "None"
  if (level === "minimal") return "Minimal"

  return effortLabel(level)
}

// withModel is the choice after another model is picked: the chat's own effort stays when the new model offers it,
// else the chat goes back to its Bot's, since the effort button lists only the new model's levels.
export function withModel(
  choice: ChatChoice,
  next: { provider: string; model: string },
  gateway: GatewayModel[],
): ChatChoice {
  const levels = levelsOf(next.model, gateway)
  const effort = levels.includes(choice.effort as GatewayModelEffort) ? choice.effort : ""

  return { provider: next.provider, model: next.model, effort }
}

// choiceOf is a chat's choice as the server gives it.
export function choiceOf(chat: { provider?: string; model?: string; effort?: string }): ChatChoice {
  return { provider: chat.provider ?? "", model: chat.model ?? "", effort: chat.effort ?? "" }
}

// keyOf is the picker's key of a model, "" for the Bot's.
export function keyOf(choice: Pick<ChatChoice, "provider" | "model">) {
  return modelKey(choice.provider, choice.model)
}

// useDraftChoice is the choice of a chat not made yet: the New chat page's and a pane's new chat tab's. Another Bot
// picked starts again from that Bot's own: a chat starts with its Bot's model and effort (issue #369).
export function useDraftChoice(bot: string) {
  const [picked, setPicked] = useState<{ bot: string; choice: ChatChoice } | null>(null)
  const choice = picked?.bot === bot ? picked.choice : botsChoice

  return [choice, (next: ChatChoice) => setPicked({ bot, choice: next })] as const
}
