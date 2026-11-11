import type { Model, Profile } from "@/api/generated/types.gen"
import { since } from "@/lib/time"

// What the Agents pages say about a profile, apart from how they draw it.

// runtimeName is a runtime as people call it.
export function runtimeName(runtime: string) {
  if (runtime === "claude") return "Claude Code"
  if (runtime === "codex") return "Codex"
  if (runtime === "opencode") return "OpenCode"

  return runtime
}

// The runtimes a profile may name, as the server knows them (orchestration/profiles.go, runtimeProviders); pi is
// the coordinator's.
export const runtimes = ["claude", "codex", "omp", "opencode", "pi"] as const

// savedAgo says how long ago a version was saved, or nothing for one with no time: the starting profiles were
// inserted by migrations at time 0.
export function savedAgo(profile: Pick<Profile, "created_at">) {
  return new Date(profile.created_at).getTime() > 0 ? since(profile.created_at) : null
}

// The efforts a profile may name, as Omnara's model field words them (AgentConfigModelField.tsx: "xhigh" reads
// "Extra High", the others are capitalized).
export const efforts = [
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "xhigh", label: "Extra High" },
  { value: "max", label: "Max" },
] as const

export function effortLabel(effort: string) {
  return efforts.find((e) => e.value === effort)?.label ?? effort
}

// short is the state in a word or two, for a card's header; text says it in full.
// short is the state in a word or two, for a card's header; text says it in full.
export type Standing = { tone: "good" | "warn"; short: string; text: string }

const clock = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" })

// standing says in words whether a worker can start under a profile now (issue #139). A profile with no answer,
// the coordinator's, has no standing.
export function standing(profile: Profile): Standing | null {
  if (profile.available === undefined) return null
  if (profile.available) return { tone: "good", short: "Ready", text: "Can run now" }

  const runtime = runtimeName(profile.runtime)

  switch (profile.unavailable_reason) {
    case "no_node":
      return { tone: "warn", short: "No machine", text: `No machine online has ${runtime}` }
    case "no_account":
      return { tone: "warn", short: "No account", text: `No ${runtime} account` }
    case "accounts_cooling":
      return {
        tone: "warn",
        short: "Waiting",
        text: profile.available_at
          ? `${runtime} accounts wait until ${clock.format(new Date(profile.available_at))}`
          : `${runtime} accounts wait for their limit`,
      }
    default:
      return { tone: "warn", short: "Cannot run", text: "Cannot run now" }
  }
}

// summary counts what can run, for the list's header: "3 can run · 1 cannot".
export function summary(profiles: Profile[]) {
  const workers = profiles.filter((p) => p.available !== undefined)
  const can = workers.filter((p) => p.available).length
  const cannot = workers.length - can

  return [can > 0 && `${can} can run`, cannot > 0 && `${cannot} cannot`].filter(Boolean).join(" · ")
}

// modelLine is a profile's model as a card says it: the name, or that the runtime chooses.
export function modelLine(profile: Pick<Profile, "model">) {
  return profile.model || "Runtime default"
}

// A Draft is a profile as the builder holds it. model is "provider/name", a bare name when the profile names no
// provider, or "" for the runtime's default.
export type Draft = {
  name: string
  description: string
  runtime: string
  model: string
  effort: string
  instruction: string
  // fallbacks names, in order, the agents that take this one's Tasks while it is out of quota (issue #171).
  fallbacks: string[]
}

// As many backups as OpenMausBot keeps for one bot (src/components/bot-panel/BackupAis.tsx, MAX_BACKUPS).
export const maxFallbacks = 5

export const emptyDraft: Draft = {
  name: "",
  description: "",
  runtime: "claude",
  model: "",
  effort: "",
  instruction: "",
  fallbacks: [],
}

export function draftOf(profile: Profile): Draft {
  return {
    name: profile.name,
    description: profile.description,
    runtime: profile.runtime,
    model: modelKey(profile.provider ?? "", profile.model ?? ""),
    effort: profile.effort ?? "",
    instruction: (profile.instruction ?? "").replace(/\s+$/, ""),
    fallbacks: profile.fallbacks,
  }
}

// modelKey names a model in a select: its provider and its name, which may itself hold a slash.
export function modelKey(provider: string, name: string) {
  if (!name) return ""

  return provider ? `${provider}/${name}` : name
}

// splitModel takes a key apart again, by the models the server lists; a key it does not find is a bare name.
function splitModel(key: string, models: Model[]) {
  const found = models.find((m) => modelKey(m.provider, m.name) === key)
  if (found) return { provider: found.provider, name: found.name }

  return { provider: "", name: key }
}

export function sameDraft(a: Draft, b: Draft) {
  return (
    a.name === b.name &&
    a.description === b.description &&
    a.runtime === b.runtime &&
    a.model === b.model &&
    a.effort === b.effort &&
    a.instruction === b.instruction &&
    a.fallbacks.join() === b.fallbacks.join()
  )
}

// The name's rule is Omnigent's (CreateAgentDialog.tsx: "Use only letters, numbers, hyphens, and underscores.").
export const namePattern = /^[A-Za-z0-9_-]+$/

// problem says what keeps a draft from being saved, in the builder's own words; the server checks the rest.
export function problem(draft: Draft): string | null {
  if (!draft.name.trim()) return "Give the agent a name."
  if (!namePattern.test(draft.name.trim())) {
    return "Use only letters, numbers, hyphens, and underscores."
  }
  if (!draft.description.trim()) return "Say what the agent is for: the coordinator chooses by it."

  return null
}

// plain is a string YAML reads as itself without quotes.
const plain = /^[A-Za-z0-9_][A-Za-z0-9_.-]*$/

function scalar(value: string) {
  return plain.test(value) &&
    !/^(true|false|null|yes|no|on|off|~)$/i.test(value) &&
    isNaN(Number(value))
    ? value
    : JSON.stringify(value)
}

// text is a longer string: a folded block for one line of words, a literal block for several lines, quoted when
// neither can hold it as it is.
function text(key: string, value: string, fold: boolean) {
  const lines = value.replace(/\r\n?/g, "\n").split("\n")
  const safe = lines.every((l) => l === l.trimEnd()) && !/^\s/.test(lines[0] ?? "")

  if (!safe || value === "") return `${key}: ${JSON.stringify(value)}\n`
  if (lines.length === 1 && !fold) return `${key}: ${JSON.stringify(value)}\n`
  if (lines.length === 1) return `${key}: >-\n  ${value}\n`

  return `${key}: |\n${lines.map((l) => (l ? `  ${l}` : "")).join("\n")}\n`
}

// toYaml writes a draft as the YAML document the server stores (name, description, runtime, an optional model,
// instruction and list of fallbacks).
export function toYaml(draft: Draft, models: Model[]) {
  let out = `name: ${scalar(draft.name.trim())}\n`
  out += text("description", draft.description.trim(), draft.description.trim().length > 80)
  out += `runtime: ${scalar(draft.runtime)}\n`

  const model = draft.model ? splitModel(draft.model, models) : { provider: "", name: "" }
  if (model.name || draft.effort) {
    out += "model:\n"
    if (model.provider) out += `  provider: ${scalar(model.provider)}\n`
    if (model.name) out += `  name: ${scalar(model.name)}\n`
    if (draft.effort) out += `  effort: ${scalar(draft.effort)}\n`
  }

  const instruction = draft.instruction.replace(/\s+$/, "")
  if (instruction) out += text("instruction", instruction, false)
  if (draft.fallbacks.length > 0) out += `fallbacks: [${draft.fallbacks.map(scalar).join(", ")}]\n`

  return out
}

// errorText is what the server said when it refused, or a plain line when it said nothing.
export function errorText(error: unknown) {
  if (error && typeof error === "object" && "message" in error && error.message) {
    return String(error.message)
  }

  return "The server refused. Try again."
}
