import type { Bot, Profile, Skill, SkillAudit, SkillRevision } from "@/api/generated/types.gen"

// What the Skills pages say about a skill (issue #21), apart from how they draw it.

// Source is where a revision came from: a public GitHub repository, as owner/repo[/path] and the ref it named, or
// an archive the owner uploaded.
export type Source =
  | { kind: "github"; where: string; ref: string; commit?: string }
  | { kind: "upload" }

export function sourceOf(revision: Pick<SkillRevision, "source" | "commit">): Source {
  const github = revision.source.match(/^github:(.+)@([^@]+)$/)
  if (github) {
    return {
      kind: "github",
      where: github[1] ?? "",
      ref: github[2] ?? "",
      ...(revision.commit ? { commit: revision.commit } : {}),
    }
  }

  return { kind: "upload" }
}

// sourceLine is a source in a line: the repository and its ref, or "Uploaded".
export function sourceLine(revision: Pick<SkillRevision, "source" | "commit">) {
  const source = sourceOf(revision)
  if (source.kind === "upload") return "Uploaded"

  return source.ref === "HEAD" ? source.where : `${source.where}@${source.ref}`
}

// githubAgain is what the add form takes to fetch the newest of a GitHub source again; none for an upload.
export function githubAgain(revision: Pick<SkillRevision, "source" | "commit">) {
  const source = sourceOf(revision)
  if (source.kind === "upload") return undefined

  return source.ref === "HEAD" ? source.where : `${source.where}@${source.ref}`
}

// Users are who gets a skill: everyone (but the agents and bots that turn it off), or the agents and bots that add
// it. An agent is a profile, as the app names it.
export type Users = { everyone: boolean; adding: string[]; off: string[] }

export function usersOf(
  skill: Pick<Skill, "name" | "everyone">,
  profiles: Pick<Profile, "name" | "skills" | "skills_off">[],
  bots: Pick<Bot, "name" | "skills" | "skills_off">[],
): Users {
  const all = [...profiles, ...bots]

  return {
    everyone: skill.everyone,
    adding: all.filter((a) => a.skills.includes(skill.name)).map((a) => a.name),
    off: skill.everyone
      ? all.filter((a) => a.skills_off.includes(skill.name)).map((a) => a.name)
      : [],
  }
}

// usersLine is who gets a skill, in a few words.
export function usersLine(users: Users) {
  if (users.everyone) {
    return users.off.length === 0 ? "Everyone" : `Everyone but ${users.off.join(", ")}`
  }

  return users.adding.length === 0 ? "No one yet" : users.adding.join(", ")
}

// A built-in skill that teaches the tool of a service is shown with that service's mark (owner, 2026-11-10: a thing
// that has a logo is shown with it); the coordinator's is Hypomnema's own.
export type BuiltinMark = "google" | "lark" | "superset" | "hypomnema"

export function builtinMark(name: string): BuiltinMark | undefined {
  switch (name) {
    case "google-workspace":
      return "google"
    case "lark":
      return "lark"
    case "superset":
      return "superset"
    case "hypomnema-coordinator":
      return "hypomnema"
    default:
      return undefined
  }
}

// builtinUsers is who gets a built-in skill: each reaches an agent by its own rule, not by a switch.
export function builtinUsers(name: string) {
  switch (builtinMark(name)) {
    case "hypomnema":
      return "Every bot"
    case "google":
      return "Agents given a Google account"
    case "lark":
      return "Agents given a Lark app"
    case "superset":
      return "Agents given a Superset account"
    default:
      return "By its own rule"
  }
}

// size is a number of bytes as a person reads it.
export function size(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`

  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

// matches is whether a skill answers what the owner typed in the search box: its name or its description.
export function matches(query: string, name: string, description: string) {
  const q = query.trim().toLowerCase()

  return q === "" || name.toLowerCase().includes(q) || description.toLowerCase().includes(q)
}

export function errorText(error: unknown) {
  if (error && typeof error === "object" && "message" in error && error.message) {
    return String(error.message)
  }

  return "The server refused. Try again."
}

// What the skills.sh tab says, as the skills CLI says it (MIT, Copyright (c) 2026 Vercel, Inc.,
// https://github.com/vercel-labs/skills).

// searchDelay is the CLI's adaptive debounce (src/find.ts): a short query waits longer, the owner is still typing.
export function searchDelay(query: string) {
  return Math.max(150, 350 - query.trim().length * 50)
}

// installsLabel is the CLI's formatInstalls (src/find.ts).
export function installsLabel(count: number) {
  const short = (n: number) => n.toFixed(1).replace(/\.0$/, "")
  if (!count || count <= 0) return ""
  if (count >= 1_000_000) return `${short(count / 1_000_000)}M installs`
  if (count >= 1_000) return `${short(count / 1_000)}K installs`

  return `${count} install${count === 1 ? "" : "s"}`
}

// slugOf is the last part of a skills.sh id (owner/repo/slug), which its audits are kept under.
export function slugOf(id: string) {
  return id.split("/").at(-1) ?? id
}

// auditorName is an auditor as skills.sh's skill page names it.
export function auditorName(partner: string) {
  switch (partner) {
    case "ath":
      return "Gen Agent Trust Hub"
    case "socket":
      return "Socket"
    case "snyk":
      return "Snyk"
    default:
      return partner
  }
}

// auditTone is how worrying a result is: the CLI colours safe and low green, medium yellow, high and critical red
// (src/add.ts riskLabel), and a count of Socket's alerts red when there is one (socketLabel).
export type AuditTone = "good" | "warn" | "bad" | "none"

export function auditResult(audit: Pick<SkillAudit, "partner" | "risk" | "alerts">): {
  text: string
  tone: AuditTone
} {
  if (audit.partner === "socket" && audit.alerts !== undefined) {
    return audit.alerts > 0
      ? { text: `${audit.alerts} alert${audit.alerts === 1 ? "" : "s"}`, tone: "bad" }
      : { text: "0 alerts", tone: "good" }
  }

  switch (audit.risk) {
    case "critical":
      return { text: "Critical Risk", tone: "bad" }
    case "high":
      return { text: "High Risk", tone: "bad" }
    case "medium":
      return { text: "Med Risk", tone: "warn" }
    case "low":
      return { text: "Low Risk", tone: "good" }
    case "safe":
      return { text: "Safe", tone: "good" }
    default:
      return { text: "--", tone: "none" }
  }
}

// isAdded is whether a skills.sh result is a skill here: the same name, from the same repository. A skill of that
// name uploaded or taken from another repository is another skill that happens to share it.
export function isAdded(
  result: { name: string; source: string },
  skills: Pick<Skill, "name" | "latest">[],
) {
  return skills.some((s) => {
    const source = sourceOf(s.latest)

    if (s.name !== result.name || source.kind !== "github") return false

    const where = source.where.toLowerCase()
    const repo = result.source.toLowerCase()

    return where === repo || where.startsWith(`${repo}/`)
  })
}
