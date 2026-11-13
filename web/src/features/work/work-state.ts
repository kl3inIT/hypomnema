import type { Node, Profile, Question, Run, TaskBoard, TaskDetail } from "@/api/generated/types.gen"

// The work in flight, after Orca's agent dashboard (MIT, Copyright (c) 2026 Lovecast Inc.,
// https://github.com/stablyai/orca,
// src/renderer/src/components/dashboard/dashboard-card-bucket.ts): every Task falls in one of three columns, and
// what waits for the owner comes first. Columns are by status because workers live for one Task; a free canvas
// (Omnigent) or teams (OpenMausBot) would need places kept for cards that come and go.

export type Column = "you" | "work" | "done"

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
  const byVersion = new Map(profiles.map((p) => [p.version_id, p.runtime]))
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
      runtime: runtimeOf(task, runtimes, byVersion),
      node: nodeId ? (nodeNames.get(nodeId) ?? nodeId) : undefined,
      queued: task.status === "pending" || task.status === "ready",
    })
  }

  return cards
}

// runtimeOf is the CLI a Task's worker runs. A Task that ran under a fallback of its agent shows that agent's CLI
// (issue #171): the one whose current version the Dispatch ran with, the Task's own when that version has been
// replaced since. runtimes and byVersion map a profile's name and its version's id to its runtime.
export function runtimeOf(
  task: TaskDetail,
  runtimes: Map<string, string>,
  byVersion: Map<string, string> = new Map(),
): string {
  return (
    task.runtime ||
    (task.latest_dispatch?.fallback_for &&
      byVersion.get(task.latest_dispatch.profile_version_id ?? "")) ||
    runtimes.get(task.profile) ||
    ""
  )
}

export function needsYou(cards: Card[]) {
  return cards.filter((c) => c.column === "you").length
}

// pullRequest is the pull request a Task's latest Dispatch reported, as its number when the link has one. A worker
// writes the evidence, so only an http(s) link is kept: anything else (javascript:, data:) is never made a link.
export function pullRequest(task: TaskDetail): { url: string; label: string } | undefined {
  const ref = task.latest_dispatch?.result?.evidence.find((e) => e.kind === "pull_request")?.ref
  const url = evidenceLink("pull_request", ref)
  if (!url) return undefined

  // GitHub's /pull/N and GitLab's /-/merge_requests/N; both are a pull request here (docs/glossary.md).
  const n = /\/(?:pull|merge_requests)\/(\d+)/.exec(new URL(url).pathname)?.[1]

  return { url, label: n ? `PR #${n}` : "PR" }
}

// repoLabel is a Run's repository as it is shown: its path, with its host in front when that is not github.com.
export function repoLabel(run: { repo?: string; repo_host?: string }): string {
  if (!run.repo) return ""

  return run.repo_host && run.repo_host !== "github.com" ? `${run.repo_host}/${run.repo}` : run.repo
}

// A pull request as a worker reports it (its instructions ask for owner/repo#N): the repositories are GitHub's.
const shortPullRequest = /^([\w.-]+)\/([\w.-]+)#(\d+)$/

// evidenceLink is where a piece of evidence leads: a pull request given as owner/repo#N on GitHub, else the ref
// itself when it is an http(s) URL. Other text has no link.
export function evidenceLink(kind: string, ref: string | undefined): string | undefined {
  const short = kind === "pull_request" ? shortPullRequest.exec(ref ?? "") : null
  if (short) return `https://github.com/${short[1]}/${short[2]}/pull/${short[3]}`

  return safeLink(ref)
}

// safeLink returns ref as an http(s) URL, or undefined for any other scheme or text.
export function safeLink(ref: string | undefined): string | undefined {
  if (!ref) return undefined

  try {
    const url = new URL(ref)

    return url.protocol === "https:" || url.protocol === "http:" ? url.href : undefined
  } catch {
    return undefined
  }
}

export function firstLine(text: string, max = 120) {
  const line = text.trim().split("\n")[0] ?? ""

  return line.length > max ? `${line.slice(0, max - 1)}…` : line
}
