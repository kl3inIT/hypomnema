// What waits for the owner: the web's "you" column (web/src/features/work/work-state.ts, columnOf), ported because
// Orca loads only a plugin's own files ("cannot import ...: it is outside the plugin"). The web's
// src/features/work/orca-waiting.test.ts runs this and the web's boardCards on the same fixtures, so the two rules
// cannot drift apart. The types are the fields of the client API's TaskBoard and Question this reads
// (api/openapi.yaml).

export type Option = { label: string; allows_text?: boolean }

export type Question = {
  id: string
  task_id: string
  run_id: string
  mode: string
  addressed_to: string
  text: string
  options: Option[]
  status: string
}

export type Run = {
  id: string
  objective: string
  status: string
  review_stall?: {
    task_id: string
    pull_request: string
    verdict: string
    waiting_for_owner: boolean
  }
}

export type Task = {
  id: string
  run_id: string
  spec: string
  status: string
  blocked_reason?: string
}

export type Board = { after: number; data: Task[]; runs: Run[] }

export type Kind = 'question' | 'review' | 'failed' | 'blocked'

export type Item = {
  // Notified once: the question's id, else the Task's id and status (issue #1061).
  key: string
  kind: Kind
  runId: string
  taskId: string
  run: string
  task: string
  text: string
  // The pull request a stalled review is about, an http(s) link only.
  link?: string
  question?: { id: string; mode: string; options: Option[] }
}

// waitingFor lists what waits for the owner, in the board's order: a Task with an open question to the owner, the
// review a Run stalled at, a failure in an open Run and a block only the owner can lift. A block on a failed
// dependency waits on that Task instead, and a question to the coordinator is the coordinator's.
export function waitingFor(board: Board, questions: Question[] = []): Item[] {
  const runs = new Map(board.runs.map((r) => [r.id, r]))
  const asked = new Map<string, Question>()

  for (const q of questions) {
    if (q.status === 'open' && q.addressed_to === 'owner') asked.set(q.task_id, q)
  }

  const items: Item[] = []

  for (const task of board.data) {
    const run = runs.get(task.run_id)
    if (!run) continue

    const item = itemOf(task, run, asked.get(task.id))
    if (item) items.push(item)
  }

  return items
}

function itemOf(task: Task, run: Run, question?: Question): Item | undefined {
  const base = { runId: run.id, taskId: task.id, run: run.objective, task: firstLine(task.spec) }

  if (question) {
    return {
      ...base,
      key: question.id,
      kind: 'question',
      text: question.text,
      question: { id: question.id, mode: question.mode, options: question.options }
    }
  }

  const stall = run.review_stall
  if (stall?.waiting_for_owner && stall.task_id === task.id) {
    const link = safeLink(stall.pull_request)

    // An item crosses to the panel as JSON, so a missing link is left out rather than undefined.
    return {
      ...base,
      key: `${task.id}:${task.status}`,
      kind: 'review',
      text: `The review stopped at its last round: ${stall.verdict}`,
      ...(link ? { link } : {})
    }
  }

  if (task.status === 'failed' && run.status === 'open') {
    return { ...base, key: `${task.id}:${task.status}`, kind: 'failed', text: 'The Task failed' }
  }

  if (task.status === 'blocked' && task.blocked_reason !== 'dependency_failed') {
    return {
      ...base,
      key: `${task.id}:${task.status}`,
      kind: 'blocked',
      text: `The Task is blocked: ${blockedText(task.blocked_reason)}`
    }
  }

  return undefined
}

// The words for a blocked Task's reason, the web's own (web/src/features/chat/wake.ts, blocked).
const blocked: Record<string, string> = {
  gate: 'it waits for you',
  dispatch_unknown: "its worker's state is unknown",
  needs_auth: 'its account needs signing in again'
}

function blockedText(reason: string | undefined): string {
  return (reason && blocked[reason]) || reason || 'no reason given'
}

function firstLine(text: string): string {
  const line = text.split('\n', 1)[0]?.trim() ?? ''

  return line.length > 120 ? `${line.slice(0, 119)}…` : line
}

// safeLink keeps only an http(s) URL, as the web's safeLink does: a worker writes the link.
export function safeLink(ref: string | undefined): string | undefined {
  if (!ref) return undefined

  try {
    const url = new URL(ref)

    return url.protocol === 'https:' || url.protocol === 'http:' ? url.href : undefined
  } catch {
    return undefined
  }
}
