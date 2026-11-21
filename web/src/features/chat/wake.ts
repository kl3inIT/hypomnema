import type { ChatWake, TaskDetail } from "@/api/generated/types.gen"
import { pullRequest, stopping } from "@/features/work/work-state"
import { firstLine } from "@/lib/text"

// What a wake-up card says (issue #303), from the event the wake-up reports, which never changes, as t3code's
// TimelineSystemDivider and Orca's task rows show what happened at that moment. The status is always in words; the
// tone only repeats it. A Task's end carries the first line of its result and its pull request, read from the Task
// as the chat already loaded it; a Task that runs again since (a follow-up, Start again) shows how it stands now
// instead, as t3code's subagent notice does (V2LifecycleRow.tsx, subagentResumedSince; issue #687).

export type WakeTone = "running" | "done" | "failed" | "needs-you" | "quiet"

export type WakeIcon =
  | "working"
  | "queued"
  | "done"
  | "failed"
  | "blocked"
  | "canceled"
  | "question"
  | "escalation"

// A link of the card: a Task opens its panel over the chat, a Run its strip in its chat.
export type WakeLink = { kind: "task" | "run"; id: string; label: string }

export type WakeLine = {
  tone: WakeTone
  icon: WakeIcon
  // before and after are the words around the link, "Task" [title] "done"; text is the whole line in words.
  before: string
  link?: WakeLink
  after: string
  // detail follows after a colon: a failure's reason, or what a worker asks.
  detail?: string
  // action is a link at the end of the line, for a line whose words name no Task: a worker's question.
  action?: WakeLink
  // pr is the pull request the Task's worker reported.
  pr?: { url: string; label: string }
  // since is when the worker of a Task running again started, for a clock that ticks on the page.
  since?: string
  text: string
}

// Names are what the loaded work says of a wake-up's ids: a Task's title, agent and the Task itself as last read,
// a Run's objective and an open question's text. An id it does not know is shown as itself.
export type WakeNames = {
  task: (id: string) => { title: string; agent?: string; now?: TaskDetail } | undefined
  run: (id: string) => string | undefined
  question: (id: string) => string | undefined
}

const noNames: WakeNames = {
  task: () => undefined,
  run: () => undefined,
  question: () => undefined,
}

// The words for a blocked Task's reason (BlockedReason in internal/controlplane/orchestration/types.go).
const blocked: Record<string, string> = {
  dependency_failed: "a Task it needs failed",
  gate: "it waits for you",
  dispatch_unknown: "its worker's state is unknown",
  needs_auth: "its account needs signing in again",
}

// The words for an escalation's kind (EscalationKind in internal/controlplane/orchestration/dispatch_types.go).
const escalations: Record<string, string> = {
  stalled: "stalled",
  overrun: "ran over its time",
  node_unavailable: "lost its machine",
  question_reminder: "still waits for an answer",
  unplaceable: "has no machine that can take it",
  account_cooling: "waits for its account to cool down",
  fallback: "runs on a fallback agent",
  repeated_failure: "keeps failing for the same reason",
  pull_request: "has news of its pull request",
}

// The ends a wake-up reports that a Task may come back from, as t3code's notification outcomes.
const outcomes = new Set(["succeeded", "failed", "canceled"])

// resumedSince says whether a Task runs again since a wake-up reported its end at the moment at: it is queued
// again, or its worker started after the wake-up, as t3code's subagentResumedSince (V2LifecycleRow.logic.ts).
// Changed from t3code: a Task read as running counts only when its worker started after the wake-up, since the
// Runs are read every 10 s and may still show the run that ended.
export function resumedSince(task: TaskDetail, at: string): boolean {
  if (task.status === "pending" || task.status === "ready") return true

  const started = Date.parse(task.latest_dispatch?.started_at ?? "")

  return !Number.isNaN(started) && started > Date.parse(at)
}

// resultLine is the first line of what a Task's worker reported, whole: the card cuts it to its width.
function resultLine(task: TaskDetail): string | undefined {
  return firstLine(task.latest_dispatch?.result?.summary || task.result || "", 1000) || undefined
}

export function wakeLine(wake: ChatWake, names: WakeNames = noNames, at?: string): WakeLine {
  const taskId = wake.task_id ?? ""
  const task = taskId ? names.task(taskId) : undefined
  const taskLink: WakeLink | undefined = taskId
    ? { kind: "task", id: taskId, label: task?.title || taskId }
    : undefined

  const line = (
    tone: WakeTone,
    icon: WakeIcon,
    before: string,
    link: WakeLink | undefined,
    after: string,
    detail?: string,
  ): WakeLine => {
    const words = [before, link?.label, after].filter(Boolean).join(" ")

    return {
      tone,
      icon,
      before,
      link,
      after,
      detail: detail || undefined,
      text: detail ? `${words}: ${detail}` : words,
    }
  }

  const now = task?.now

  // A Task that runs again since says how it stands now.
  if (wake.event === "task.status" && outcomes.has(wake.status ?? "") && now && at) {
    if (resumedSince(now, at)) return nowLine(now, taskLink, line)
  }

  switch (wake.event) {
    case "task.status":
      switch (wake.status) {
        case "succeeded":
          return withPr(line("done", "done", "Task", taskLink, "done", now && resultLine(now)), now)
        case "failed":
          return withPr(
            line(
              "failed",
              "failed",
              "Task",
              taskLink,
              "failed",
              wake.reason || (now && resultLine(now)),
            ),
            now,
          )
        case "blocked":
          return line(
            "needs-you",
            "blocked",
            "Task",
            taskLink,
            "blocked",
            blocked[wake.reason ?? ""] ?? wake.reason,
          )
        default:
          return line("quiet", "canceled", "Task", taskLink, wake.status ?? "changed")
      }
    case "question.opened": {
      const asked = wake.question_id ? names.question(wake.question_id) : undefined
      const who = task?.agent || "A worker"

      return {
        ...line(
          "quiet",
          "question",
          asked ? `${who} asks` : `${who} asks a question`,
          undefined,
          "",
          asked ? firstLine(asked) : undefined,
        ),
        action: taskLink,
      }
    }
    case "escalation":
      return line(
        "needs-you",
        "escalation",
        "Task",
        taskLink,
        escalations[wake.escalation ?? ""] ?? "needs a look",
        wake.reason,
      )
    case "run.finished":
    case "run.canceled": {
      const runId = wake.run_id ?? ""
      const link: WakeLink | undefined = runId
        ? { kind: "run", id: runId, label: firstLine(names.run(runId) ?? "", 80) || runId }
        : undefined

      return wake.event === "run.finished"
        ? line("done", "done", "Run", link, "finished")
        : line("quiet", "canceled", "Run", link, "canceled")
    }
  }
}

type Line = (
  tone: WakeTone,
  icon: WakeIcon,
  before: string,
  link: WakeLink | undefined,
  after: string,
  detail?: string,
) => WakeLine

// withPr adds the pull request a Task's worker reported to its line.
function withPr(line: WakeLine, task: TaskDetail | undefined): WakeLine {
  const pr = task ? pullRequest(task) : undefined

  return pr ? { ...line, pr } : line
}

// nowLine says how a Task that runs again stands now: running with its newest activity and a clock, stopping,
// queued, or ended again with its new result.
function nowLine(task: TaskDetail, link: WakeLink | undefined, line: Line): WakeLine {
  const activity = task.latest_dispatch?.activity?.replace(/\s+/gu, " ").trim()

  if (stopping(task)) return line("quiet", "canceled", "Task", link, "stopping")

  switch (task.status) {
    case "running":
      return {
        ...line("running", "working", "Task", link, "running", activity),
        since: task.latest_dispatch?.started_at,
      }
    case "pending":
    case "ready":
      return line("quiet", "queued", "Task", link, "queued again")
    case "succeeded":
      return withPr(line("done", "done", "Task", link, "done", resultLine(task)), task)
    case "failed":
      return withPr(line("failed", "failed", "Task", link, "failed", resultLine(task)), task)
    case "blocked":
      return line("needs-you", "blocked", "Task", link, "blocked")
    case "canceled":
      return line("quiet", "canceled", "Task", link, "canceled")
  }
}
