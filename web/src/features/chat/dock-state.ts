import type { ChatMessage, Question, RunDetail, TaskDetail } from "@/api/generated/types.gen"
import { allEnded } from "@/features/runs/use-run-actions"

// What the chat docks above its composer (issue #314): its open Runs, each a strip that opens to its Tasks, after
// Orca's task list docked above the composer (MIT, Copyright (c) 2026 Lovecast Inc., https://github.com/stablyai/orca;
// src/renderer/src/components/native-chat/tools/tasks/NativeChatDockedTaskList.tsx and NativeChatTaskList.tsx:
// a collapsed row with "n of m" done that opens to the list, a status icon a row and the status in words beside
// it). Which strips start open follows t3code's ThreadDetailsPanel.tsx with ThreadDetailsCard.tsx (MIT, Copyright
// (c) 2026 T3 Tools Inc., https://github.com/pingdotgg/t3code), whose card shows less when the room it has is
// short. The words a status is said in are the Team page's.

export type DockTone = "running" | "done" | "failed" | "needs-you" | "quiet"

export type DockIcon =
  | "queued"
  | "working"
  | "done"
  | "failed"
  | "blocked"
  | "canceled"
  | "question"

export type DockState = { word: string; tone: DockTone; icon: DockIcon }

// taskState says one Task's status in words: an open question to the owner first, as the Team page's Needs you does.
export function taskState(task: TaskDetail, asking = false): DockState {
  if (asking) return { word: "Waiting for you", tone: "needs-you", icon: "question" }

  switch (task.status) {
    case "pending":
    case "ready":
      return { word: "Queued", tone: "quiet", icon: "queued" }
    case "running":
      return { word: "Working", tone: "running", icon: "working" }
    case "blocked":
      return task.blocked_reason === "dependency_failed"
        ? { word: "Waits for a failed Task", tone: "quiet", icon: "blocked" }
        : { word: "Needs you", tone: "needs-you", icon: "blocked" }
    case "succeeded":
      return { word: "Done", tone: "done", icon: "done" }
    case "failed":
      return { word: "Failed", tone: "failed", icon: "failed" }
    case "canceled":
      return { word: "Canceled", tone: "quiet", icon: "canceled" }
  }
}

// runState says where a whole Run stands, from what most needs the owner down: a question to him or a Task that
// waits for him, a failure, work going on, work that waits, and every Task ended.
export function runState(run: RunDetail, asking: ReadonlySet<string>): DockState {
  // A Run that ended stays docked only when a wake-up's chip asked to see it.
  if (run.status === "finished") return { word: "Finished", tone: "done", icon: "done" }
  if (run.status === "canceled") return { word: "Canceled", tone: "quiet", icon: "canceled" }

  const states = run.tasks.map((t) => taskState(t, asking.has(t.id)))

  if (states.some((s) => s.tone === "needs-you"))
    return { word: "Needs you", tone: "needs-you", icon: "question" }
  if (states.some((s) => s.tone === "failed"))
    return { word: "Failed", tone: "failed", icon: "failed" }
  if (states.some((s) => s.icon === "working"))
    return { word: "Working", tone: "running", icon: "working" }
  if (run.tasks.length === 0) return { word: "No Tasks yet", tone: "quiet", icon: "queued" }
  if (!allEnded(run.tasks)) return { word: "Queued", tone: "quiet", icon: "queued" }

  return { word: "Ready to finish", tone: "done", icon: "done" }
}

// progress is "n of m Tasks done", counting the Tasks that succeeded.
export function progress(run: RunDetail): string {
  const done = run.tasks.filter((t) => t.status === "succeeded").length
  const total = run.tasks.length

  return `${done} of ${total} ${total === 1 ? "Task" : "Tasks"} done`
}

// The room a docked strip takes, in CSS pixels: its row, one row a Task up to the list's own scroll, and the row of
// its buttons. A card that waits for an answer (an Approval, a question) takes about a card's height.
const stripRow = 36
const taskRow = 28
const shownTasks = 5
const actionsRow = 40
const waitingCard = 180

// The share of the window the docked area may take before its strips start closed, so that the conversation keeps
// most of the screen, as t3code's card drops to a smaller density when it would not fit.
const share = 0.3

// startOpen decides which newly seen Runs start open: none when there are several, since every strip already shows
// its progress; the one Run when, opened, it still fits in the room above the composer with the cards that wait.
export function startOpen(
  runs: RunDetail[],
  known: Readonly<Record<string, boolean>>,
  room: { height: number; waiting: number },
): Record<string, boolean> {
  const fresh = runs.filter((r) => !(r.id in known))
  if (fresh.length === 0) return {}

  const lone = runs.length === 1 && fresh.length === 1 ? runs[0] : undefined
  const height = lone
    ? stripRow * runs.length +
      taskRow * Math.min(lone.tasks.length, shownTasks) +
      actionsRow +
      waitingCard * room.waiting
    : Infinity

  return Object.fromEntries(fresh.map((r) => [r.id, r === lone && height <= room.height * share]))
}

// wakeCount counts the wake-up rows a chat has kept: a new one means its Runs moved on.
export function wakeCount(messages: ChatMessage[]): number {
  return messages.filter((m) => m.role === "system" && m.kind === "wake").length
}

// asking lists the Tasks with an open question to the owner.
export function askingTasks(questions: Question[]): Set<string> {
  return new Set(
    questions
      .filter((q) => q.status === "open" && q.addressed_to === "owner")
      .map((q) => q.task_id),
  )
}

// toRead is every Run the dock reads in full: the chat's open Runs, then those a wake-up's chip asked to see.
export function toRead(open: string[], pinned: string[]): string[] {
  return [...new Set([...open, ...pinned])]
}

// docked picks the Runs to show from those read: this chat's, open, or ended but asked for by a chip. foreign are
// the Runs read that belong to another chat, whose chip links to the Run's own chat.
export function docked(
  read: RunDetail[],
  chatId: string,
  pinned: string[],
): { runs: RunDetail[]; foreign: Set<string> } {
  const foreign = new Set(read.filter((r) => r.chat_id !== chatId).map((r) => r.id))
  const runs = read.filter(
    (r) => !foreign.has(r.id) && (r.status === "open" || pinned.includes(r.id)),
  )

  return { runs, foreign }
}
