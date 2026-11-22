import { describe, expect, test } from "vitest"

import type { TaskDetail } from "@/api/generated/types.gen"

import { resumedSince, wakeLine, type WakeNames } from "./wake"

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
    expect(
      wakeLine({
        event: "escalation",
        task_id: "tsk_9",
        escalation: "pull_request",
        reason: "merged",
      }).text,
    ).toBe("Task tsk_9 has news of its pull request: merged")
    expect(wakeLine({ event: "run.canceled", run_id: "run_9", status: "canceled" }).text).toBe(
      "Run run_9 canceled",
    )
  })
})

describe("a finished Task's wake-up", () => {
  const at = "2026-11-17T10:05:00Z"
  const task = (more: Partial<TaskDetail> = {}): TaskDetail => ({
    id: "tsk_1",
    run_id: "run_1",
    position: 1,
    profile: "implementer",
    kind: "narrow",
    spec: "Add the /health handler",
    status: "succeeded",
    failure_count: 0,
    created_at: "2026-11-17T10:00:00Z",
    updated_at: at,
    latest_dispatch: {
      id: "dsp_1",
      task_id: "tsk_1",
      node_id: "n1",
      status: "succeeded",
      epoch: 1,
      created_at: "2026-11-17T10:00:00Z",
      updated_at: at,
      started_at: "2026-11-17T10:00:00Z",
      settled_at: at,
      result: {
        summary: "Added GET /health with a test.\nIt returns 200 and the version.",
        evidence: [{ kind: "pull_request", ref: "kl3inIT/hypomnema#700" }],
      },
    },
    ...more,
  })
  const with_ = (now: TaskDetail): WakeNames => ({
    ...names,
    task: () => ({ title: "Add the /health handler", now }),
  })
  const done = { event: "task.status", task_id: "tsk_1", status: "succeeded" } as const

  // Scenario: a worker finished while the owner was in the chat (issue #687). The wake-up says the first line of
  // its result and links its pull request, read from the Task the chat loaded, so he need not open the panel.
  test("carries the first line of its result and its pull request", () => {
    expect(wakeLine(done, with_(task()), at)).toMatchObject({
      tone: "done",
      detail: "Added GET /health with a test.",
      text: "Task Add the /health handler done: Added GET /health with a test.",
      pr: { url: "https://github.com/kl3inIT/hypomnema/pull/700", label: "PR #700" },
    })
    // A Task the chat has not loaded says only what happened.
    expect(wakeLine(done, names, at).detail).toBeUndefined()
  })

  // Scenario: the owner asked for more after it finished, so the Task runs again. Its old wake-up shows how it
  // stands now, running with its worker's newest step and a clock, as t3code's resumed subagent; the Runs read
  // before the wake-up (the worker that just ended, still read as running) do not count.
  test("shows the Task as it stands now once it runs again", () => {
    const again = task({
      status: "running",
      latest_dispatch: {
        id: "dsp_2",
        task_id: "tsk_1",
        node_id: "n1",
        status: "running",
        epoch: 2,
        created_at: "2026-11-17T10:20:00Z",
        updated_at: "2026-11-17T10:21:00Z",
        started_at: "2026-11-17T10:20:00Z",
        activity: "Edit: internal/health.go",
      },
    })
    expect(wakeLine(done, with_(again), at)).toMatchObject({
      tone: "running",
      text: "Task Add the /health handler running: Edit: internal/health.go",
      since: "2026-11-17T10:20:00Z",
    })
    expect(wakeLine(done, with_(task({ status: "ready" })), at).text).toBe(
      "Task Add the /health handler queued again",
    )

    const stale = task({ status: "running" })
    expect(resumedSince(stale, at)).toBe(false)
    expect(wakeLine(done, with_(stale), at).text).toBe(
      "Task Add the /health handler done: Added GET /health with a test.",
    )
  })
})
