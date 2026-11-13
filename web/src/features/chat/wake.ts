import type { ChatWake } from "@/api/generated/types.gen"
import { firstLine } from "@/features/work/work-state"

// What a wake-up card says (issue #303), from the event the wake-up reports, which never changes: no live status
// is read again, as t3code's TimelineSystemDivider and Orca's task rows show what happened at that moment. The
// status is always in words; the tone only repeats it.

export type WakeTone = "done" | "failed" | "needs-you" | "quiet"

export type WakeIcon = "done" | "failed" | "blocked" | "canceled" | "question" | "escalation"

// A link of the card: a Task opens its panel over the chat, a Run its page.
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
  text: string
}

// Names are what the loaded work says of a wake-up's ids: a Task's title and agent, a Run's objective and an open
// question's text. An id it does not know is shown as itself.
export type WakeNames = {
  task: (id: string) => { title: string; agent?: string } | undefined
  run: (id: string) => string | undefined
  question: (id: string) => string | undefined
}

export const noNames: WakeNames = {
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
}

export function wakeLine(wake: ChatWake, names: WakeNames = noNames): WakeLine {
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

  switch (wake.event) {
    case "task.status":
      switch (wake.status) {
        case "succeeded":
          return line("done", "done", "Task", taskLink, "done")
        case "failed":
          return line("failed", "failed", "Task", taskLink, "failed", wake.reason)
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
