import type { Model, Profile, ToolPreset } from "@/api/generated/types.gen"
import { toolEntries } from "@/features/tools/tools-state"
import { modelName } from "@/lib/model-name"
import { since } from "@/lib/time"

// What the Agents pages say about a profile, apart from how they draw it.

// runtimeName is a runtime as people call it.
export function runtimeName(runtime: string) {
  if (!runtime) return "By Task kind"
  if (runtime === "claude") return "Claude Code"
  if (runtime === "codex") return "Codex"
  if (runtime === "opencode") return "OpenCode"

  return runtime
}

// The runtimes a profile may name, as the server knows them (orchestration/profiles.go, runtimeProviders): the
// workers' CLIs. pi runs a Bot on the server and is not a profile's (issue #169).
export const runtimes = ["claude", "codex", "omp", "opencode"] as const
export const routingRoles = ["implementer", "planner", "explorer", "reviewer"]

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
// one read from a list of versions, has no standing.
export function standing(profile: Profile): Standing | null {
  if (profile.available === undefined) return null
  if (profile.available) return { tone: "good", short: "Ready", text: "Can run now" }

  const runtime = runtimeName(profile.runtime)

  switch (profile.unavailable_reason) {
    case "no_node":
      return { tone: "warn", short: "No machine", text: `No machine online has ${runtime}` }
    case "no_account":
      return { tone: "warn", short: "No account", text: `No ${runtime} account` }
    // A Claude Code profile that names no provider of kind model_gateway (issue #381).
    case "needs_gateway":
      return {
        tone: "warn",
        short: "No gateway",
        text: `${runtime} runs only through the model gateway`,
      }
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

// The View menu's Status group, as Beszel's (systems-table.tsx): whether a worker can start under a profile now. A
// profile with no answer shows under All agents only.
export const filterStates = ["can", "cannot"] as const

export type FilterState = (typeof filterStates)[number]

// statusChoices are the View menu's Status group with each one's count: "Can run (3)".
export function statusChoices(profiles: Profile[]) {
  const can = profiles.filter((p) => p.available === true).length
  const cannot = profiles.filter((p) => p.available === false).length

  return [
    { value: "all", label: "All agents" },
    { value: "can", label: `Can run (${can})` },
    { value: "cannot", label: `Cannot run (${cannot})` },
  ]
}

// withStatus is the profiles the View menu's choice lets through.
export function withStatus(profiles: Profile[], status: FilterState | "all") {
  if (status === "all") return profiles

  return profiles.filter((p) => p.available === (status === "can"))
}

// modelLine is a profile's model as a card says it: the name, or that the runtime chooses.
export function modelLine(profile: Pick<Profile, "model" | "runtime">) {
  if (!profile.runtime) return "Role routing"
  return profile.model ? modelName(profile.model) : "Runtime default"
}

// A Draft is a profile as the builder holds it. model is "provider/name", a bare name when the profile names no
// provider, or "" for the runtime's default.
export type Draft = {
  name: string
  description: string
  runtime: string
  model: string
  effort: string
  instructions: string
  // fallbacks are models, "provider/name" in order, a worker of the agent runs while its own model cannot be served
  // (issue #1336; they named other agents until 2026-11-28, issue #171).
  fallbacks: string[]
  // skills and skillsOff are the skills the agent adds and the ones for everyone it turns off (issue #21).
  skills: string[]
  skillsOff: string[]
  preset: ToolPreset
  tools: string[]
  toolsOff: string[]
  // cpu and memory are what a worker of the agent asks of a machine, as the YAML's requests write them ("500m",
  // "512Mi"); empty is the default for building work (issue #731).
  cpu: string
  memory: string
  // memoryLimit is the most memory each worker may use, as the YAML's limits.memory writes it ("4Gi"); empty is none
  // of its own (issue #1313). The builder does not edit it yet; saving keeps it.
  memoryLimit: string
}

// cpuText writes thousandths of a core as a request: whole cores as a number, the rest in millicores.
export function cpuText(millis: number | undefined) {
  if (!millis) return ""

  return millis % 1000 === 0 ? String(millis / 1000) : `${millis}m`
}

// memoryText writes bytes as a request: in Gi or Mi when they divide it, else in bytes.
export function memoryText(bytes: number | undefined) {
  if (!bytes) return ""
  if (bytes % 2 ** 30 === 0) return `${bytes / 2 ** 30}Gi`
  if (bytes % 2 ** 20 === 0) return `${bytes / 2 ** 20}Mi`

  return String(bytes)
}

// As many backups as OpenMausBot keeps for one bot (src/components/bot-panel/BackupAis.tsx, MAX_BACKUPS), for a Bot's
// fallbacks and an Agent's.
export const maxFallbacks = 5

export const emptyDraft: Draft = {
  name: "",
  description: "",
  runtime: "claude",
  model: "",
  effort: "",
  instructions: "",
  fallbacks: [],
  skills: [],
  skillsOff: [],
  preset: "full",
  tools: [],
  toolsOff: [],
  cpu: "",
  memory: "",
  memoryLimit: "",
}

export function draftOf(profile: Profile): Draft {
  return {
    name: profile.name,
    description: profile.description,
    runtime: profile.runtime,
    model: modelKey(profile.provider ?? "", profile.model ?? ""),
    effort: profile.effort ?? "",
    instructions: profile.instructions.replace(/\s+$/, ""),
    fallbacks: profile.fallbacks,
    skills: profile.skills,
    skillsOff: profile.skills_off,
    preset: profile.preset ?? "full",
    tools: profile.tools,
    toolsOff: profile.tools_off,
    cpu: cpuText(profile.cpu_request_millis),
    memory: memoryText(profile.memory_request_bytes),
    memoryLimit: memoryText(profile.memory_limit_bytes),
  }
}

// modelKey names a model in a select: its provider and its name, which may itself hold a slash.
export function modelKey(provider: string, name: string) {
  if (!name) return ""

  return provider ? `${provider}/${name}` : name
}

// splitModel takes a key apart again, by the models the server lists; a key it does not find is a bare name.
export function splitModel(key: string, models: Model[]) {
  const found = models.find((m) => modelKey(m.provider, m.name) === key)
  if (found) return { provider: found.provider, name: found.name }

  return { provider: "", name: key }
}

// copyName names a copy of an agent: "<name>-copy", as OpenMausBot's "<name> copy" (src/lib/bot-duplicate.ts)
// with a hyphen, since a profile's name has no spaces. A profile is stored under its name, so a name already
// taken would save over that agent: then a number follows (inferred; OpenMausBot's bots have ids).
export function copyName(name: string, taken: string[]) {
  const base = `${name}-copy`
  if (!taken.includes(base)) return base

  let n = 2
  while (taken.includes(`${base}-${n}`)) n++

  return `${base}-${n}`
}

export function sameDraft(a: Draft, b: Draft) {
  return (
    a.name === b.name &&
    a.description === b.description &&
    a.runtime === b.runtime &&
    a.model === b.model &&
    a.effort === b.effort &&
    a.instructions === b.instructions &&
    a.fallbacks.join() === b.fallbacks.join() &&
    a.skills.join() === b.skills.join() &&
    a.skillsOff.join() === b.skillsOff.join() &&
    a.tools.join() === b.tools.join() &&
    a.toolsOff.join() === b.toolsOff.join() &&
    a.preset === b.preset &&
    a.cpu.trim() === b.cpu.trim() &&
    a.memory.trim() === b.memory.trim() &&
    a.memoryLimit.trim() === b.memoryLimit.trim()
  )
}

// The name's rule is Omnigent's (CreateAgentDialog.tsx: "Use only letters, numbers, hyphens, and underscores.").
const namePattern = /^[A-Za-z0-9_-]+$/

// problem says what keeps a draft from being saved, in the builder's own words; the server checks the rest.
export function problem(draft: Draft): string | null {
  if (!draft.name.trim()) return "Give the agent a name."
  if (!namePattern.test(draft.name.trim())) {
    return "Use only letters, numbers, hyphens, and underscores."
  }
  if (!draft.description.trim()) return "Say what the agent is for: the coordinator chooses by it."
  if (!draft.runtime && !routingRoles.includes(draft.name.trim())) {
    return "Only implementer, planner, explorer and reviewer can use role routing."
  }

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
// instructions, list of fallbacks and skills, and the optional requests and limits).
export function toYaml(draft: Draft, models: Model[]) {
  let out = `name: ${scalar(draft.name.trim())}\n`
  out += text("description", draft.description.trim(), draft.description.trim().length > 80)
  if (draft.runtime) out += `runtime: ${scalar(draft.runtime)}\n`

  const model = draft.model ? splitModel(draft.model, models) : { provider: "", name: "" }
  if (draft.runtime && (model.name || draft.effort)) {
    out += "model:\n"
    if (model.provider) out += `  provider: ${scalar(model.provider)}\n`
    if (model.name) out += `  name: ${scalar(model.name)}\n`
    if (draft.effort) out += `  effort: ${scalar(draft.effort)}\n`
  }

  const instructions = draft.instructions.replace(/\s+$/, "")
  if (instructions) out += text("instructions", instructions, false)
  if (draft.runtime && draft.fallbacks.length > 0) {
    out += `fallbacks: [${draft.fallbacks.map(scalar).join(", ")}]\n`
  }
  if (draft.skills.length > 0) out += `skills: [${draft.skills.map(scalar).join(", ")}]\n`
  if (draft.skillsOff.length > 0) out += `skills_off: [${draft.skillsOff.map(scalar).join(", ")}]\n`
  const tools = toolEntries(draft.tools)
  const toolsOff = toolEntries(draft.toolsOff)
  if (draft.preset !== "full") out += `preset: ${draft.preset}\n`
  if (tools.length > 0) out += `tools: [${tools.map(scalar).join(", ")}]\n`
  if (toolsOff.length > 0) out += `tools_off: [${toolsOff.map(scalar).join(", ")}]\n`

  const cpu = draft.cpu.trim()
  const memory = draft.memory.trim()
  if (cpu || memory) {
    out += "requests:\n"
    if (cpu) out += `  cpu: ${JSON.stringify(cpu)}\n`
    if (memory) out += `  memory: ${JSON.stringify(memory)}\n`
  }

  const memoryLimit = draft.memoryLimit.trim()
  if (memoryLimit) out += `limits:\n  memory: ${JSON.stringify(memoryLimit)}\n`

  return out
}
