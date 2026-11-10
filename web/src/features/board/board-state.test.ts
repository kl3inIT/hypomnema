import { expect, test } from "vitest"

import type { Dispatch } from "@/api/generated/types.gen"

import { boardCards, byColumn, evidenceLink, lanes, pullRequest, safeLink } from "./board-state"
import { board, question, run, task } from "./fixtures"

const open = run("run_1")
const closed = run("run_2", { status: "canceled", repo: "kl3inIT/memoryos" })

const dispatch = (m: Partial<Dispatch> = {}): Dispatch => ({
  id: "dsp_1",
  task_id: "t",
  node_id: "nod_1",
  status: "running",
  epoch: 1,
  created_at: "2026-11-09T10:00:00Z",
  updated_at: "2026-11-09T10:00:00Z",
  ...m,
})

// Scenario: each Task lands where the owner should look: what only the owner can move on in Needs you, work in
// flight in Working, what ended in Done.
test.each([
  ["a question to the owner", task("t", "run_1"), open, true, "you"],
  ["a failure in an open Run", task("t", "run_1", { status: "failed" }), open, false, "you"],
  ["a failure in a canceled Run", task("t", "run_2", { status: "failed" }), closed, false, "done"],
  [
    "a stop for a decision",
    task("t", "run_1", { status: "blocked", blocked_reason: "gate" }),
    open,
    false,
    "you",
  ],
  [
    "a missing login",
    task("t", "run_1", { status: "blocked", blocked_reason: "needs_auth" }),
    open,
    false,
    "you",
  ],
  [
    "a failed dependency",
    task("t", "run_1", { status: "blocked", blocked_reason: "dependency_failed" }),
    open,
    false,
    "work",
  ],
  ["a running worker", task("t", "run_1"), open, false, "work"],
  ["a Task waiting to start", task("t", "run_1", { status: "pending" }), open, false, "work"],
  ["a success", task("t", "run_1", { status: "succeeded" }), open, false, "done"],
] as const)("%s goes to %s", (_, t, r, asks, want) => {
  const [card] = boardCards(board([t], [r]), asks ? [question("q", t)] : [])

  expect(card?.column).toBe(want)
})

// Scenario: a question to pi shows on its card but does not wait for the owner; an answered one shows nowhere.
test("only an open question to the owner waits for the owner", () => {
  const t1 = task("t1", "run_1")
  const t2 = task("t2", "run_1")
  const cards = boardCards(board([t1, t2], [open]), [
    question("q1", t1, { addressed_to: "coordinator" }),
    question("q2", t2, { status: "answered" }),
  ])

  expect(cards.map((c) => c.column)).toEqual(["work", "work"])
  expect(cards[0]?.piQuestion?.id).toBe("q1")
  expect(cards[1]?.question).toBeUndefined()
})

// Scenario: a card names its machine and its runtime, the runtime from the Task or else from its profile.
test("a card names its machine and runtime", () => {
  const t = task("t", "run_1", { latest_dispatch: dispatch() })
  const [card] = boardCards(
    board([t], [open]),
    [],
    [{ id: "nod_1", name: "aioffice-app" } as never],
    [{ name: "implementer", runtime: "codex" } as never],
  )

  expect(card?.node).toBe("aioffice-app")
  expect(card?.runtime).toBe("codex")
})

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

test("a pull request is named by its number", () => {
  const t = task("t", "run_1", {
    latest_dispatch: dispatch({
      status: "succeeded",
      result: {
        summary: "done",
        evidence: [{ kind: "pull_request", ref: "https://github.com/o/r/pull/63" }],
      },
    }),
  })

  expect(pullRequest(t)).toEqual({ url: "https://github.com/o/r/pull/63", label: "PR #63" })
  expect(pullRequest(task("u", "run_1"))).toBeUndefined()
})

// Scenario: a worker reports its pull request as its instructions ask, owner/repo#N. The card and the panel link
// it on GitHub; the same text as another kind of evidence, or with anything added, is not a link.
test("a pull request given as owner/repo#N is linked", () => {
  const t = task("t", "run_1", {
    latest_dispatch: dispatch({
      status: "succeeded",
      result: {
        summary: "done",
        evidence: [{ kind: "pull_request", ref: "kl3inIT/hypomnema#78" }],
      },
    }),
  })

  expect(pullRequest(t)).toEqual({
    url: "https://github.com/kl3inIT/hypomnema/pull/78",
    label: "PR #78",
  })
  expect(evidenceLink("commit", "kl3inIT/hypomnema#78")).toBeUndefined()
  expect(evidenceLink("pull_request", "kl3inIT/hypomnema#78/../x")).toBeUndefined()
  expect(evidenceLink("pull_request", "javascript:alert(1)#1")).toBeUndefined()
})

// Scenario: a worker reports a link that would run script or is not a link; it is never made a link.
test.each(["javascript:alert(1)//pull/1", "data:text/html,x", "not a url"])(
  "%s is not a link",
  (ref) => {
    const t = task("t", "run_1", {
      latest_dispatch: dispatch({
        status: "succeeded",
        result: { summary: "done", evidence: [{ kind: "pull_request", ref }] },
      }),
    })

    expect(pullRequest(t)).toBeUndefined()
    expect(safeLink(ref)).toBeUndefined()
  },
)
