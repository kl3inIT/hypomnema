import type { Node, Profile, Question, Run, TaskBoard, TaskDetail } from "@/api/generated/types.gen"

// The board of the work in flight, after Orca's agent dashboard (MIT, Copyright (c) 2026 Lovecast Inc.,
// https://github.com/stablyai/orca,
// src/renderer/src/components/dashboard/dashboard-card-bucket.ts): every Task falls in one of three columns, and
// what waits for the owner comes first. Columns are by status because workers live for one Task; a free canvas
// (Omnigent) or teams (OpenMausBot) would need places kept for cards that come and go.

export type Column = "you" | "work" | "done"

export const columns: { key: Column; name: string; hint?: string }[] = [
  { key: "you", name: "Needs you" },
  { key: "work", name: "Working" },
  { key: "done", name: "Done", hint: "last 24h" },
]

export type Card = {
  task: TaskDetail
  run: Run
  column: Column
  // A question from this Task's worker to the owner, open.
  question?: Question
  // A question from this Task's worker to the coordinator, open: shown, answered by pi.
  piQuestion?: Question
  runtime: string
  node?: string
  // Pending or ready: waits for its dependencies or for a free slot on a machine.
  queued: boolean
}

// columnOf places a Task. A question to the owner, a failure in an open Run and a block only the owner can lift
// wait for the owner; a block on a failed dependency waits on that Task instead.
export function columnOf(task: TaskDetail, run: Run, question?: Question): Column {
  if (question) return "you"

  switch (task.status) {
    case "failed":
      return run.status === "open" ? "you" : "done"
    case "blocked":
      return task.blocked_reason === "dependency_failed" ? "work" : "you"
    case "succeeded":
    case "canceled":
      return "done"
    default:
      return "work"
  }
}

export function boardCards(
  board: TaskBoard,
  questions: Question[] = [],
  nodes: Node[] = [],
  profiles: Profile[] = [],
): Card[] {
  const runs = new Map(board.runs.map((r) => [r.id, r]))
  const nodeNames = new Map(nodes.map((n) => [n.id, n.name]))
  const runtimes = new Map(profiles.map((p) => [p.name, p.runtime]))
  const asked = new Map<string, Question>()
  const askedPi = new Map<string, Question>()

  for (const q of questions) {
    if (q.status !== "open") continue
    ;(q.addressed_to === "owner" ? asked : askedPi).set(q.task_id, q)
  }

  const cards: Card[] = []

  for (const task of board.data) {
    const run = runs.get(task.run_id)
    if (!run) continue

    const question = asked.get(task.id)
    const nodeId = task.latest_dispatch?.node_id

    cards.push({
      task,
      run,
      column: columnOf(task, run, question),
      question,
      piQuestion: askedPi.get(task.id),
      runtime: task.runtime || runtimes.get(task.profile) || "",
      node: nodeId ? (nodeNames.get(nodeId) ?? nodeId) : undefined,
      queued: task.status === "pending" || task.status === "ready",
    })
  }

  return cards
}

// sortColumn orders one column: the newest change first (Orca), and in Working what runs before what waits.
export function sortColumn(cards: Card[]): Card[] {
  return cards.toSorted((a, b) => {
    if (a.queued !== b.queued) return a.queued ? 1 : -1

    return b.task.updated_at.localeCompare(a.task.updated_at)
  })
}

export function byColumn(cards: Card[]): Record<Column, Card[]> {
  const out: Record<Column, Card[]> = { you: [], work: [], done: [] }
  for (const c of cards) out[c.column].push(c)

  return { you: sortColumn(out.you), work: sortColumn(out.work), done: sortColumn(out.done) }
}

export type Group = "status" | "run" | "repo"

export type Lane = {
  key: string
  title: string
  subtitle?: string
  runId?: string
  cards: Card[]
  done: number
}

// lanes splits the cards into rows: one for the status view, one per Run or per repository otherwise. Rows with
// work for the owner come first, then the most recently changed.
export function lanes(cards: Card[], group: Group, repo = ""): Lane[] {
  const shown = repo ? cards.filter((c) => c.run.repo === repo) : cards
  if (group === "status") return [{ key: "all", title: "", cards: shown, done: 0 }]

  const rows = new Map<string, Lane>()

  for (const c of shown) {
    const key = group === "run" ? c.run.id : c.run.repo || "No repository"
    let lane = rows.get(key)

    if (!lane) {
      lane =
        group === "run"
          ? {
              key,
              title: firstLine(c.run.objective),
              subtitle: c.run.repo,
              runId: c.run.id,
              cards: [],
              done: 0,
            }
          : { key, title: key, cards: [], done: 0 }
      rows.set(key, lane)
    }

    lane.cards.push(c)
    if (c.task.status === "succeeded") lane.done++
  }

  const newest = (l: Lane) =>
    l.cards.reduce((m, c) => (c.task.updated_at > m ? c.task.updated_at : m), "")
  const needs = (l: Lane) => l.cards.filter((c) => c.column === "you").length

  return [...rows.values()].toSorted(
    (a, b) => needs(b) - needs(a) || newest(b).localeCompare(newest(a)),
  )
}

export function needsYou(cards: Card[]) {
  return cards.filter((c) => c.column === "you").length
}

export function repos(cards: Card[]) {
  return [...new Set(cards.map((c) => c.run.repo).filter((r): r is string => !!r))].toSorted()
}

// pullRequest is the pull request a Task's latest Dispatch reported, as its number when the link has one.
export function pullRequest(task: TaskDetail): { url: string; label: string } | undefined {
  const ref = task.latest_dispatch?.result?.evidence.find((e) => e.kind === "pull_request")?.ref
  if (!ref) return undefined

  const n = /\/pull\/(\d+)/.exec(ref)?.[1]

  return { url: ref, label: n ? `PR #${n}` : "PR" }
}

export function firstLine(text: string, max = 120) {
  const line = text.trim().split("\n")[0] ?? ""

  return line.length > max ? `${line.slice(0, max - 1)}…` : line
}
