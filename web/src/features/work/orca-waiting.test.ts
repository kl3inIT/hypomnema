import { expect, test } from "vitest"

// The Orca plugin's port of the "you" column (issue #1061): Orca loads only a plugin's own files, so the rule is
// copied there, and this keeps the copy the same as columnOf.
import { waitingFor } from "../../../../orca/src/waiting"
import { board, question, run, task } from "./fixtures"
import { boardCards } from "./work-state"

const open = run("run_1")
const closed = run("run_2", { status: "canceled" })
const stalled = run("run_3", {
  review_stall: {
    task_id: "review",
    pull_request: "https://github.com/owner/repo/pull/840",
    verdict: "incorrect",
    merge_state: "BLOCKED",
    mergeable: "MERGEABLE",
    checks: "PASSING",
    waiting_for_owner: true,
  },
})

const asking = task("asking", "run_1")
const toPi = task("to-pi", "run_1")
const answered = task("answered", "run_1")
const tasks = [
  asking,
  toPi,
  answered,
  task("failed", "run_1", { status: "failed" }),
  task("failed-closed", "run_2", { status: "failed" }),
  task("gate", "run_1", { status: "blocked", blocked_reason: "gate" }),
  task("login", "run_1", { status: "blocked", blocked_reason: "needs_auth" }),
  task("unknown", "run_1", { status: "blocked", blocked_reason: "dispatch_unknown" }),
  task("dependency", "run_1", { status: "blocked", blocked_reason: "dependency_failed" }),
  task("running", "run_1"),
  task("pending", "run_1", { status: "pending" }),
  task("succeeded", "run_1", { status: "succeeded" }),
  task("review", "run_3", { status: "succeeded" }),
  task("earlier", "run_3", { status: "succeeded" }),
  task("orphan", "run_9", { status: "failed" }),
]
const questions = [
  question("q1", asking),
  question("q2", toPi, { addressed_to: "coordinator" }),
  question("q3", answered, { status: "answered" }),
]

// Scenario: the plugin tells the owner of exactly the Tasks the Work page puts in its "you" column.
test("the plugin waits on the Tasks the Work page puts in its you column", () => {
  const b = board(tasks, [open, closed, stalled])
  const you = boardCards(b, questions)
    .filter((c) => c.column === "you")
    .map((c) => c.task.id)

  expect(waitingFor(b, questions).map((i) => i.taskId)).toEqual(you)
  expect(you).toEqual(["asking", "failed", "gate", "login", "unknown", "review"])
})
