import type { Routine, RoutineRun } from "@/api/generated/types.gen"
import { since } from "@/lib/time"

// What the Routines pages say about a Routine and its runs, apart from how they draw it (issue #211). The status, its
// one fix, the grey status line and a run's one line are adapted from OpenMausBot (Apache License 2.0, Copyright
// 2026 Milind Soni and OpenMausBot contributors, https://github.com/milind-soni/OpenMausBot; src/lib/automations.ts
// L106-121, automationStatus L264-279, orderAutomations L290-298, statusLine L308-324, runLine L327-343, and its
// English words, src/locales/en.json). Changed: a Routine is on or paused and never done; its failures in a row
// are the server's count, not counted from the runs on the page; a problem run's reason is the run's error.

export type Status = {
  state: "working" | "paused" | "needs-you"
  reason?: string
  // fix is the one thing that mends what "Needs you" names.
  fix?: "answer" | "run"
}

export const statusWords: Record<Status["state"], string> = {
  working: "On",
  paused: "Paused",
  "needs-you": "Needs you",
}

export const fixWords: Record<NonNullable<Status["fix"]>, string> = {
  answer: "Answer",
  run: "Run now",
}

// routineStatus: a paused Routine is paused; one whose run waits for the owner's answer, or that failed twice or
// more in a row, needs him; any other is on.
export function routineStatus(routine: Pick<Routine, "state" | "failures" | "last_run">): Status {
  if (routine.state === "paused") return { state: "paused" }
  if (routine.last_run?.state === "waiting") {
    return { state: "needs-you", reason: "A run waits for your answer.", fix: "answer" }
  }
  if (routine.failures >= 2) {
    return {
      state: "needs-you",
      reason: `It didn't finish the last ${routine.failures} times.`,
      fix: "run",
    }
  }

  return { state: "working" }
}

const rank: Record<Status["state"], number> = { "needs-you": 0, working: 1, paused: 2 }

// orderRoutines puts the ones that need the owner first, then those on, then the paused; within each, the soonest
// next run, then by name.
export function orderRoutines<T extends Routine>(routines: readonly T[]): T[] {
  const next = (r: Routine) => (r.next_run_at ? new Date(r.next_run_at).getTime() : Infinity)

  return [...routines].sort(
    (a, b) =>
      rank[routineStatus(a).state] - rank[routineStatus(b).state] ||
      next(a) - next(b) ||
      a.name.localeCompare(b.name),
  )
}

const day = 24 * 60 * 60 * 1000
const startOfDay = (at: number) => new Date(at).setHours(0, 0, 0, 0)
const clock = (at: number) =>
  new Date(at).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" })

// nextWhen says when something comes: "today 9:00 AM", "tomorrow 9:00 AM", "Monday 9:00 AM", or "Nov 23".
export function nextWhen(iso: string, now = Date.now()) {
  const at = new Date(iso).getTime()
  const days = Math.round((startOfDay(at) - startOfDay(now)) / day)
  if (days <= 0) return `today ${clock(at)}`
  if (days === 1) return `tomorrow ${clock(at)}`
  if (days < 7) {
    return `${new Date(at).toLocaleDateString(undefined, { weekday: "long" })} ${clock(at)}`
  }

  return new Date(at).toLocaleDateString(undefined, { month: "short", day: "numeric" })
}

// lastWhen says when something was: "today 9:02 AM", "yesterday", "Mon 9:02 AM", or "Nov 3".
export function lastWhen(iso: string, now = Date.now()) {
  const at = new Date(iso).getTime()
  const days = Math.round((startOfDay(now) - startOfDay(at)) / day)
  if (days <= 0) return `today ${clock(at)}`
  if (days === 1) return "yesterday"
  if (days < 7) {
    return `${new Date(at).toLocaleDateString(undefined, { weekday: "short" })} ${clock(at)}`
  }

  return new Date(at).toLocaleDateString(undefined, { month: "short", day: "numeric" })
}

export const capital = (text: string) => text.charAt(0).toUpperCase() + text.slice(1)

const problem = (run: Pick<RoutineRun, "state">) =>
  run.state === "failed" || run.state === "canceled"

// statusLine is the grey line under a Routine's sentence: "On · last ran today 9:02 AM · next: today 10:00 AM",
// or "First run today 10:00 AM" before it has run.
export function statusLine(routine: Routine, now = Date.now()) {
  const status = routineStatus(routine)
  const latest = routine.last_run

  if (!latest && status.state === "working" && routine.next_run_at) {
    return `First run ${nextWhen(routine.next_run_at, now)}`
  }

  const parts = [statusWords[status.state]]
  if (status.state === "paused" && routine.paused_reason) parts.push(routine.paused_reason)
  if (latest?.state === "running") parts.push("running now")
  else if (latest)
    parts.push(`last ran ${lastWhen(latest.started_at, now)}${problem(latest) ? " ✗" : ""}`)
  else parts.push("hasn't run yet")
  if (routine.state === "on" && routine.next_run_at) {
    parts.push(`next: ${nextWhen(routine.next_run_at, now)}`)
  }

  return parts.join(" · ")
}

export type RunLine = { when: string; mark: "✓" | "✗" | "…" | "!"; text: string }

const firstLine = (text: string | undefined) =>
  (text ?? "")
    .split("\n")
    .map((line) => line.trim())
    .find(Boolean) ?? ""
const short = (text: string) => (text.length > 110 ? `${text.slice(0, 109).trimEnd()}…` : text)

// runLine says one run in plain words: when, a mark, and its one-line result or reason.
export function runLine(run: RoutineRun, now = Date.now()): RunLine {
  const when = capital(lastWhen(run.started_at, now))

  switch (run.state) {
    case "succeeded":
      return { when, mark: "✓", text: short(firstLine(run.output)) || "Done" }
    case "waiting":
      return { when, mark: "!", text: "It's waiting for your answer." }
    case "running":
      return { when, mark: "…", text: "running now" }
    case "canceled": {
      const reason = short(firstLine(run.error))
      return { when, mark: "✗", text: reason ? `Stopped: ${reason}` : "Stopped" }
    }
    case "failed":
      return { when, mark: "✗", text: short(firstLine(run.error)) || "It didn't finish" }
  }
}

export const startedByWords: Record<RoutineRun["started_by"], string> = {
  schedule: "by the schedule",
  manual: "by hand",
}

// kindLine says what a Routine runs: a prompt, or its script's file.
export function kindLine(routine: Pick<Routine, "kind" | "script">) {
  return routine.kind === "script" ? `Script ${routine.script ?? ""}`.trim() : "Prompt"
}

// lasted says how long a run took: "4s", "2m 5s", "1h 3m"; nothing for a run that has not ended.
export function lasted(run: Pick<RoutineRun, "started_at" | "ended_at">) {
  if (!run.ended_at) return ""

  const seconds = Math.max(
    0,
    Math.round((new Date(run.ended_at).getTime() - new Date(run.started_at).getTime()) / 1000),
  )
  if (seconds < 60) return `${seconds}s`

  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return seconds % 60 ? `${minutes}m ${seconds % 60}s` : `${minutes}m`

  const hours = Math.floor(minutes / 60)

  return minutes % 60 ? `${hours}h ${minutes % 60}m` : `${hours}h`
}

// limitLine says how long a run may take: "10 minutes".
export function limitLine(seconds: number) {
  if (seconds % 3600 === 0) return seconds === 3600 ? "1 hour" : `${seconds / 3600} hours`
  if (seconds % 60 === 0) return seconds === 60 ? "1 minute" : `${seconds / 60} minutes`

  return `${seconds} seconds`
}

// ago is since() for a run's line in a card: "5m ago".
export const ago = since

export function errorText(error: unknown) {
  if (error && typeof error === "object" && "message" in error && error.message) {
    return String(error.message)
  }

  return "The server refused. Try again."
}
