import { describe, expect, test } from "vitest"

import type { ChatMessage, Question, RunDetail, TaskDetail } from "@/api/generated/types.gen"

import {
  askingTasks,
  docked,
  progress,
  runState,
  startOpen,
  taskState,
  wakeCount,
} from "./dock-state"

const task = (id: string, status: TaskDetail["status"], more: Partial<TaskDetail> = {}) =>
  ({
    id,
    run_id: "run_1",
    position: 1,
    profile: "implementer",
    spec: id,
    status,
    failure_count: 0,
    created_at: "2026-11-13T10:00:00Z",
    updated_at: "2026-11-13T10:00:00Z",
    ...more,
  }) as TaskDetail

const run = (id: string, tasks: TaskDetail[], more: Partial<RunDetail> = {}) =>
  ({
    id,
    objective: id,
    status: "open",
    created_by: "pi",
    chat_id: "cht_1",
    created_at: "2026-11-13T10:00:00Z",
    updated_at: "2026-11-13T10:00:00Z",
    tasks,
    ...more,
  }) as RunDetail

describe("a docked Run", () => {
  // Scenario: the owner glances at the strip. It says how far the Run is in words, and what most needs him wins
  // over work going on: a question, then a failure, then a worker running; with every Task ended it can be finished.
  test("says where it stands, what needs the owner first", () => {
    const working = run("r", [task("a", "succeeded"), task("b", "running"), task("c", "pending")])
    expect(progress(working)).toBe("1 of 3 Tasks done")
    expect(runState(working, new Set()).word).toBe("Working")
    expect(runState(working, new Set(["c"])).word).toBe("Needs you")

    const failed = run("r", [task("a", "failed"), task("b", "running")])
    expect(runState(failed, new Set()).word).toBe("Failed")

    const ended = run("r", [task("a", "succeeded"), task("b", "canceled")])
    expect(runState(ended, new Set())).toMatchObject({ word: "Ready to finish", tone: "done" })
    expect(progress(run("r", [task("a", "succeeded")]))).toBe("1 of 1 Task done")
    expect(runState(run("r", []), new Set()).word).toBe("No Tasks yet")
    expect(runState(run("r", [], { status: "finished" }), new Set()).word).toBe("Finished")
  })

  // Scenario: each Task row says its status in words; a block on a failed dependency waits on that Task, any
  // other block waits for the owner, as on the Board.
  test("says a Task's status in the Board's words", () => {
    expect(taskState(task("a", "ready")).word).toBe("Queued")
    expect(taskState(task("a", "running"), true).word).toBe("Waiting for you")
    expect(taskState(task("a", "blocked", { blocked_reason: "gate" })).tone).toBe("needs-you")
    expect(taskState(task("a", "blocked", { blocked_reason: "dependency_failed" })).tone).toBe(
      "quiet",
    )
  })
})

describe("which strips start open", () => {
  const small = run("r1", [task("a", "running"), task("b", "pending")])

  // Scenario: one Run with a few Tasks on a tall window opens by itself; on a short window, with several Runs, or
  // with cards that wait above the composer it starts closed, so the conversation keeps the screen.
  test("one Run opens when it fits, otherwise every strip starts closed", () => {
    expect(startOpen([small], {}, { height: 900, waiting: 0 })).toEqual({ r1: true })
    expect(startOpen([small], {}, { height: 400, waiting: 0 })).toEqual({ r1: false })
    expect(startOpen([small], {}, { height: 900, waiting: 1 })).toEqual({ r1: false })
    expect(startOpen([small, run("r2", [])], {}, { height: 1400, waiting: 0 })).toEqual({
      r1: false,
      r2: false,
    })
  })

  // Scenario: the owner closed the strip; the next read of the Runs does not open it again, and a Run that comes
  // later starts closed beside it.
  test("is decided once a Run, then left to the owner", () => {
    expect(startOpen([small], { r1: false }, { height: 900, waiting: 0 })).toEqual({})
    expect(startOpen([small, run("r2", [])], { r1: true }, { height: 900, waiting: 0 })).toEqual({
      r2: false,
    })
  })
})

describe("the Runs a chat docks", () => {
  // Scenario: a wake-up's chip asks to see a Run that has finished; it stays docked. A Run of another chat is
  // never docked, so its chip keeps the link to its page.
  test("are the chat's open Runs and the ended ones a chip asked for", () => {
    const open = run("r1", [])
    const finished = run("r2", [], { status: "finished" })
    const other = run("r3", [], { chat_id: "cht_2" })

    expect(docked([open, finished, other], "cht_1", []).runs.map((r) => r.id)).toEqual(["r1"])

    const shown = docked([open, finished, other], "cht_1", ["r2", "r3"])
    expect(shown.runs.map((r) => r.id)).toEqual(["r1", "r2"])
    expect([...shown.foreign]).toEqual(["r3"])
  })
})

describe("what the dock reads", () => {
  // Scenario: only questions to the owner that are still open mark a Task as waiting for him; only wake-up rows
  // count as news of the Runs.
  test("open questions to the owner and wake-up rows", () => {
    const q = (id: string, more: Partial<Question>) =>
      ({ id, task_id: id, status: "open", addressed_to: "owner", ...more }) as Question
    expect([
      ...askingTasks([
        q("a", {}),
        q("b", { addressed_to: "coordinator" }),
        q("c", { status: "answered" }),
      ]),
    ]).toEqual(["a"])

    const m = (seq: number, more: Partial<ChatMessage>) =>
      ({ seq, chat_id: "c", role: "system", text: "", created_at: "", ...more }) as ChatMessage
    expect(
      wakeCount([m(1, { kind: "wake" }), m(2, { kind: "failed" }), m(3, { role: "owner" })]),
    ).toBe(1)
  })
})
