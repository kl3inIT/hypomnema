import { expect, test } from "vitest"

import { board, question, run, task } from "@/features/work/fixtures"
import { boardCards } from "@/features/work/work-state"

import { byColumn, lanes } from "./board-state"

const open = run("run_1")

// Scenario: in Working, workers that run come before Tasks that wait; a column shows the newest change first.
test("a column puts running work first, newest first", () => {
  const cards = boardCards(
    board(
      [
        task("waits", "run_1", { status: "pending", updated_at: "2026-11-09T12:00:00Z" }),
        task("older", "run_1", { updated_at: "2026-11-09T10:00:00Z" }),
        task("newer", "run_1", { updated_at: "2026-11-09T11:00:00Z" }),
      ],
      [open],
    ),
  )

  expect(byColumn(cards).work.map((c) => c.task.id)).toEqual(["newer", "older", "waits"])
})

// Scenario: grouped by Run, the Run that needs the owner comes first and counts its Tasks done; a repository filter
// keeps only that repository's work.
test("rows per Run put the one that needs the owner first", () => {
  const busy = run("run_3", { repo: "kl3inIT/beyondpilot" })
  const t1 = task("t1", "run_1", { status: "succeeded", updated_at: "2026-11-09T12:00:00Z" })
  const t2 = task("t2", "run_3")
  const t3 = task("t3", "run_3", { status: "succeeded" })
  const cards = boardCards(board([t1, t2, t3], [open, busy]), [question("q", t2)])

  const rows = lanes(cards, "run")
  expect(rows.map((l) => l.key)).toEqual(["run_3", "run_1"])
  expect(rows[0]?.done).toBe(1)

  expect(lanes(cards, "repo", "kl3inIT/hypomnema").map((l) => l.title)).toEqual([
    "kl3inIT/hypomnema",
  ])
  expect(lanes(cards, "status", "kl3inIT/beyondpilot")[0]?.cards).toHaveLength(2)
})
