import { describe, expect, test } from "vitest"

import type { ChatMessage } from "@/api/generated/types.gen"

import {
  chatItems,
  chatReducer,
  emptyChat,
  failed,
  type ChatAction,
  type ChatView,
} from "./chat-state"

let seq = 0
const msg = (m: Partial<ChatMessage> & Pick<ChatMessage, "role">): ChatMessage => ({
  seq: ++seq,
  chat_id: "cht_1",
  text: "",
  created_at: "2026-11-09T10:00:00Z",
  ...m,
})

const run = (...actions: ChatAction[]): ChatView => actions.reduce(chatReducer, emptyChat)

describe("the chat view", () => {
  // Scenario: pi thinks, runs a tool, and answers while the owner watches; each kept message replaces what was
  // shown live, so nothing appears twice.
  test("live text and tools give way to the kept messages", () => {
    const owner = msg({ role: "owner", text: "add a health page" })
    let view = run(
      { type: "message", message: owner },
      { type: "state", working: true },
      { type: "reset" },
      { type: "delta", delta: { kind: "thinking", index: 0, delta: "check " } },
      { type: "delta", delta: { kind: "thinking", index: 0, delta: "the runs" } },
    )

    expect(view.live[0]).toEqual({ kind: "thinking", text: "check the runs" })

    view = (
      [
        {
          type: "message",
          message: msg({
            role: "coordinator",
            parts: [{ type: "thinking", text: "check the runs" }],
          }),
        },
        {
          type: "tool",
          activity: { phase: "start", id: "call_1", name: "bash", args: { command: "ls" } },
        },
      ] satisfies ChatAction[]
    ).reduce(chatReducer, view)

    expect(view.live).toEqual({})
    expect(view.running).toEqual([{ id: "call_1", name: "bash", args: { command: "ls" } }])

    view = chatReducer(view, {
      type: "message",
      message: msg({
        role: "tool",
        text: "pi ran bash: ls",
        parts: [
          { type: "tool_result", id: "call_1", name: "bash", args: { command: "ls" }, output: "a" },
        ],
      }),
    })

    expect(view.running).toEqual([])

    view = chatReducer(view, { type: "delta", delta: { kind: "text", index: 0, delta: "On it" } })
    expect(chatItems(view).at(-1)).toMatchObject({ kind: "reply", text: "On it", streaming: true })

    view = chatReducer(view, {
      type: "message",
      message: msg({
        role: "coordinator",
        text: "On it.",
        parts: [{ type: "text", text: "On it." }],
      }),
    })
    view = chatReducer(view, { type: "state", working: false })

    expect(chatItems(view).map((i) => i.kind)).toEqual(["owner", "activity", "reply"])
    expect(chatItems(view).at(-1)).toMatchObject({ text: "On it.", streaming: false })
  })

  test("a message seen twice, as after a reconnect, is kept once", () => {
    const m = msg({ role: "owner", text: "hi" })
    const view = run({ type: "message", message: m }, { type: "message", message: m })

    expect(view.messages).toHaveLength(1)
  })

  test("going idle clears what was live, as after a stop", () => {
    const view = run(
      { type: "state", working: true },
      { type: "delta", delta: { kind: "text", index: 0, delta: "half" } },
      { type: "tool", activity: { phase: "start", id: "c", name: "bash" } },
      { type: "state", working: false },
    )

    expect(view.live).toEqual({})
    expect(view.running).toEqual([])
  })
})

describe("the conversation items", () => {
  // As Omnara: consecutive thoughts and tools are one group; a reply or the owner closes it.
  test("a burst of work is one group, closed by the reply", () => {
    const view = run(
      { type: "message", message: msg({ role: "owner", text: "go" }) },
      {
        type: "message",
        message: msg({ role: "coordinator", parts: [{ type: "thinking", text: "plan" }] }),
      },
      {
        type: "message",
        message: msg({ role: "tool", parts: [{ type: "tool_result", id: "1", name: "bash" }] }),
      },
      {
        type: "message",
        message: msg({ role: "tool", parts: [{ type: "tool_result", id: "2", name: "read" }] }),
      },
      {
        type: "message",
        message: msg({
          role: "coordinator",
          parts: [
            { type: "thinking", text: "done" },
            { type: "text", text: "Given to a worker." },
          ],
        }),
      },
      {
        type: "message",
        message: msg({
          role: "system",
          kind: "wake",
          event_seq: 7,
          text: "Task t1 of run r1 is succeeded",
          wake: { event: "task.status", run_id: "r1", task_id: "t1", status: "succeeded" },
        }),
      },
    )

    const items = chatItems(view)
    expect(items.map((i) => i.kind)).toEqual(["owner", "activity", "reply", "wake"])
    expect(items[3]).toMatchObject({ wake: { task_id: "t1", status: "succeeded" } })

    const group = items[1]
    if (group?.kind !== "activity") throw new Error("the second item is not a group")
    expect(group.steps.map((s) => s.kind)).toEqual(["thinking", "tool", "tool", "thinking"])
  })

  test("the group pi is working in is open, with its running tool", () => {
    const view = run(
      { type: "message", message: msg({ role: "owner", text: "go" }) },
      { type: "state", working: true },
      {
        type: "tool",
        activity: { phase: "start", id: "c", name: "bash", args: { command: "ls" } },
      },
    )

    const last = chatItems(view).at(-1)
    expect(last).toMatchObject({ kind: "activity", open: true })
    expect(last?.kind === "activity" && last.steps[0]).toMatchObject({ kind: "tool", name: "bash" })
    expect(last?.kind === "activity" && last.steps[0]).not.toHaveProperty("result")
  })

  test("a reply kept before parts existed shows its text", () => {
    const view = run({ type: "message", message: msg({ role: "coordinator", text: "Hello" }) })

    expect(chatItems(view)).toMatchObject([{ kind: "reply", text: "Hello" }])
  })

  // Scenario: the owner pressed Stop, here or elsewhere; the server ends the turn with a stopped row, which reads
  // as the stop. A failure after he wrote again is a failure, and an answered Approval is neither.
  test("a stopped row is the stop, a failed row a failure", () => {
    const view = run(
      { type: "message", message: msg({ role: "owner", text: "refactor everything" }) },
      { type: "message", message: msg({ role: "system", kind: "stopped", text: "Stopped" }) },
      { type: "message", message: msg({ role: "owner", text: "only the page" }) },
      {
        type: "message",
        message: msg({ role: "system", kind: "approval", text: 'The owner approved "Send".' }),
      },
      {
        type: "message",
        message: msg({
          role: "system",
          kind: "failed",
          text: "pi's model call failed: overloaded",
        }),
      },
    )
    const items = chatItems(view)

    expect(items.map((i) => i.kind)).toEqual(["owner", "stopped", "owner", "notice", "notice"])
    expect(items.filter((i) => i.kind === "notice").map((i) => failed(i.message))).toEqual([
      false,
      true,
    ])
  })

  // Scenario: the owner sends a message; it shows at once, and once the stream keeps it, it shows once.
  test("a message being sent shows until it is kept", () => {
    const kept = msg({ role: "owner", text: "hi" })
    const shown = { ...kept, seq: -1 }

    expect(chatItems(emptyChat, { sending: [shown] })).toMatchObject([{ kind: "owner", key: "-1" }])

    const acknowledged = run({ type: "message", message: kept })
    expect(chatItems(acknowledged, { sending: [kept] })).toHaveLength(1)
  })

  // Scenario: the Bot shows a dashboard, then revises it; the stream sends its message again with the same seq. The
  // card is drawn once, as it is now, where it was; any other message sent twice is still taken once.
  test("an artifact's message is updated in place", () => {
    const card = {
      id: "art_1",
      revision: 1,
      title: "Sales",
      type: "html" as const,
      updated_at: "2026-11-14T10:00:00Z",
    }
    const shown = msg({ role: "system", kind: "artifact", text: "Sales", artifact: card })
    const reply = msg({ role: "coordinator", text: "Here it is." })
    const revised = {
      ...shown,
      text: "Sales by region",
      artifact: { ...card, revision: 2, title: "Sales by region" },
    }

    const view = run(
      { type: "message", message: shown },
      { type: "message", message: reply },
      { type: "message", message: revised },
      { type: "message", message: { ...reply, text: "changed" } },
    )
    const items = chatItems(view)

    expect(items.map((i) => i.kind)).toEqual(["artifact", "reply"])
    expect(items[0]).toMatchObject({ artifact: { revision: 2, title: "Sales by region" } })
    expect(items[1]).toMatchObject({ text: "Here it is." })
  })

  test("the stream says when it is reconnecting", () => {
    expect(run({ type: "connection", reconnecting: true }).reconnecting).toBe(true)
    expect(
      run({ type: "connection", reconnecting: true }, { type: "connection", reconnecting: false })
        .reconnecting,
    ).toBe(false)
  })
})

// A chat is read from its end a page at a time (issue #502).
describe("history read in pages", () => {
  // Scenario: a chat opens on a slow link (issue #1129). It is loading until its newest page is read, even an empty
  // one, so the view never calls a long chat empty meanwhile; what the stream brings after the page is what came
  // while the chat is open, which the view may follow with a smooth scroll.
  test("a chat is loading until its newest page, and live once the stream brings more", () => {
    expect(emptyChat).toMatchObject({ loaded: false, streamed: false })

    const read = run({
      type: "history",
      messages: [msg({ role: "owner", text: "hi" })],
      earlier: true,
    })
    expect(read).toMatchObject({ loaded: true, streamed: false })
    expect(run({ type: "history", messages: [], earlier: false }).loaded).toBe(true)

    expect(chatReducer(read, { type: "state", working: true }).streamed).toBe(false)
    expect(
      chatReducer(read, { type: "delta", delta: { kind: "text", index: 0, delta: "on it" } })
        .streamed,
    ).toBe(true)
    expect(
      chatReducer(read, { type: "message", message: msg({ role: "coordinator", text: "done" }) })
        .streamed,
    ).toBe(true)
  })
  // Scenario: the newest page arrives, the stream sends its last message again, then an older page goes on top; a
  // tool group cut by the page edge joins up, and nothing shows twice.
  test("an older page goes on top and joins the group it cut", () => {
    const owner = msg({ role: "owner", text: "check the runs" })
    const thought = msg({
      role: "coordinator",
      text: "",
      parts: [{ type: "thinking", text: "look" }],
    })
    const tool = msg({
      role: "tool",
      text: "ran",
      parts: [{ type: "tool_result", id: "t1", name: "bash" }],
    })
    const reply = msg({ role: "coordinator", text: "Two runs." })
    const next = msg({ role: "owner", text: "thanks" })

    const view = run(
      { type: "history", messages: [tool, reply, next], earlier: true },
      { type: "message", message: next },
      { type: "history", messages: [owner, thought], earlier: false },
    )

    expect(view.earlier).toBe(false)
    expect(view.messages.map((m) => m.seq)).toEqual(
      [owner, thought, tool, reply, next].map((m) => m.seq),
    )
    expect(chatItems(view).map((i) => i.kind)).toEqual(["owner", "activity", "reply", "owner"])
  })

  // Scenario: a Routine set up before the oldest message read waits for its page, so it never shows at the top of
  // the wrong part of the chat.
  test("a card older than what is read waits for its page", () => {
    const old = msg({ role: "owner", text: "every morning", created_at: "2026-11-09T10:00:00Z" })
    const setups = [{ id: "rtn_1", at: "2026-11-09T09:00:00Z" }]

    const partial = run({ type: "history", messages: [old], earlier: true })
    const whole = chatReducer(partial, { type: "history", messages: [], earlier: false })

    expect(chatItems(partial, {}, setups).map((i) => i.kind)).toEqual(["owner"])
    expect(chatItems(whole, {}, setups).map((i) => i.kind)).toEqual(["routine-setup", "owner"])
  })
})
