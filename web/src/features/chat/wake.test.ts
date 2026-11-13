import { describe, expect, test } from "vitest"

import { wakeLine, type WakeNames } from "./wake"

const names: WakeNames = {
  task: (id) =>
    id === "tsk_1" ? { title: "Add the /health handler", agent: "implementer" } : undefined,
  run: (id) => (id === "run_1" ? "health page\nwith a test" : undefined),
  question: (id) => (id === "qst_1" ? "Use goth or hand-written OAuth?\nBoth work." : undefined),
}

describe("a wake-up's line", () => {
  // Scenario: the chat reads its wake-ups with the work it already loaded. Each says what happened in words, with
  // the Task or Run named by its title, and a link to open it.
  test("says what happened, naming the work by its title", () => {
    const done = wakeLine({ event: "task.status", task_id: "tsk_1", status: "succeeded" }, names)
    expect(done).toMatchObject({
      tone: "done",
      text: "Task Add the /health handler done",
      link: { kind: "task", id: "tsk_1", label: "Add the /health handler" },
    })

    const failed = wakeLine(
      {
        event: "task.status",
        task_id: "tsk_1",
        status: "failed",
        reason: "3 failed attempts; the last: worker_error",
      },
      names,
    )
    expect(failed).toMatchObject({
      tone: "failed",
      text: "Task Add the /health handler failed: 3 failed attempts; the last: worker_error",
    })

    const asks = wakeLine(
      { event: "question.opened", task_id: "tsk_1", question_id: "qst_1" },
      names,
    )
    expect(asks).toMatchObject({
      icon: "question",
      text: "implementer asks: Use goth or hand-written OAuth?",
      action: { kind: "task", id: "tsk_1" },
    })
    expect(asks.link).toBeUndefined()

    const finished = wakeLine({ event: "run.finished", run_id: "run_1", status: "finished" }, names)
    expect(finished).toMatchObject({
      text: "Run health page finished",
      link: { kind: "run", id: "run_1" },
    })
  })

  // Scenario: the work is not loaded (an old Run's, or the board still reading). The line names the ids and
  // still says what happened.
  test("falls back to the ids, and words for the reasons", () => {
    expect(
      wakeLine({ event: "task.status", task_id: "tsk_9", status: "blocked", reason: "gate" }).text,
    ).toBe("Task tsk_9 blocked: it waits for you")
    expect(
      wakeLine({ event: "question.opened", task_id: "tsk_9", question_id: "qst_9" }).text,
    ).toBe("A worker asks a question")
    expect(wakeLine({ event: "escalation", task_id: "tsk_9", escalation: "stalled" }).text).toBe(
      "Task tsk_9 stalled",
    )
    expect(wakeLine({ event: "run.canceled", run_id: "run_9", status: "canceled" }).text).toBe(
      "Run run_9 canceled",
    )
  })
})
