import { z } from "zod"

import type {
  GatewayModel,
  GatewayModelEffort,
  GatewayModelInputKind,
  GatewayModelSettings,
} from "@/api/generated/types.gen"

// What the edit form of a gateway model holds (issue #367): each attribute the model's YAML sets, as text; an
// empty one comes from models.dev. Input and reasoning are a choice, "auto" being models.dev's.
export type ModelForm = {
  context_window: string
  max_output: string
  input: "auto" | "text" | "text,image"
  reasoning: "auto" | "yes" | "no"
  efforts: string
}

// The effort levels in their order, as the server names them.
export const effortLevels: GatewayModelEffort[] = [
  "none",
  "minimal",
  "low",
  "medium",
  "high",
  "xhigh",
  "max",
]

const tokens = z
  .string()
  .trim()
  .refine(
    (v) => v === "" || /^[1-9]\d*$/.test(v.replaceAll(",", "")),
    "A number of tokens above 0.",
  )

export const modelFormSchema = z.object({
  context_window: tokens,
  max_output: tokens,
  input: z.enum(["auto", "text", "text,image"]),
  reasoning: z.enum(["auto", "yes", "no"]),
  efforts: z
    .string()
    .refine(
      (v) => splitWords(v).every((w) => effortLevels.includes(w as GatewayModelEffort)),
      `Levels among ${effortLevels.join(", ")}.`,
    ),
})

function splitWords(v: string) {
  return v
    .split(/[\s,]+/)
    .map((w) => w.trim().toLowerCase())
    .filter(Boolean)
}

// formOf is the form for a model: what its YAML sets, nothing else.
export function formOf(model: GatewayModel): ModelForm {
  const a = model.attributes
  const set = <T>(v: { source: string; value?: T }) => (v.source === "set" ? v.value : undefined)

  const input = set(a.input)
  const reasoning = set(a.reasoning)

  return {
    context_window: set(a.context_window)?.toString() ?? "",
    max_output: set(a.max_output)?.toString() ?? "",
    input: input ? (input.includes("image") ? "text,image" : "text") : "auto",
    reasoning: reasoning === undefined ? "auto" : reasoning ? "yes" : "no",
    efforts: set(a.efforts)?.join(", ") ?? "",
  }
}

// settingsOf is what the form sends: each value it sets, none for one left to models.dev.
export function settingsOf(form: ModelForm): GatewayModelSettings {
  const out: GatewayModelSettings = {}
  const number = (v: string) => (v.trim() === "" ? undefined : Number(v.replaceAll(",", "").trim()))

  const window = number(form.context_window)
  if (window !== undefined) out.context_window = window

  const output = number(form.max_output)
  if (output !== undefined) out.max_output = output

  if (form.input !== "auto") out.input = form.input.split(",") as GatewayModelInputKind[]
  if (form.reasoning !== "auto") out.reasoning = form.reasoning === "yes"

  const efforts = splitWords(form.efforts) as GatewayModelEffort[]
  if (efforts.length > 0) {
    out.efforts = effortLevels.filter((level) => efforts.includes(level))
  }

  return out
}

const grouped = new Intl.NumberFormat("en-US")

// tokenCount is a number of tokens as the form shows it: 1,050,000.
export function tokenCount(n: number) {
  return grouped.format(n)
}

// shortTokens is a number of tokens on a row: 1.05M, 272K.
export function shortTokens(n: number) {
  if (n >= 1_000_000) return `${+(n / 1_000_000).toFixed(2)}M`
  if (n >= 1_000) return `${+(n / 1_000).toFixed(1)}K`

  return `${n}`
}

// inputName says what a model takes.
export function inputName(input: GatewayModelInputKind[] | undefined) {
  if (!input) return undefined

  return input.includes("image") ? "Text and image" : "Text only"
}

// fromModelsDev is a hint of what an empty field takes: models.dev's value, or that none is known.
export function fromModelsDev(value: string | undefined) {
  return value === undefined ? "Not known: the client's own default" : `From models.dev: ${value}`
}
