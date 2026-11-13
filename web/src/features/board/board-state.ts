import { firstLine, type Card, type Column } from "@/features/work/work-state"

// The Board's columns and rows of the work in flight (features/work/work-state.ts places each Task in a column),
// after Orca's agent dashboard (MIT, Copyright (c) 2026 Lovecast Inc., https://github.com/stablyai/orca).

export const columns: { key: Column; name: string; hint?: string }[] = [
  { key: "you", name: "Needs you" },
  { key: "work", name: "Working" },
  { key: "done", name: "Done", hint: "last 24h" },
]

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

export function repos(cards: Card[]) {
  return [...new Set(cards.map((c) => c.run.repo).filter((r): r is string => !!r))].toSorted()
}
