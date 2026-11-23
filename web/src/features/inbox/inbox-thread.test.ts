import { expect, test } from "vitest"

import type { Todo, TodoEvent } from "@/api/generated/types.gen"

import { canHand, canSend, sendQuestion, threadOf } from "./inbox-thread"

const source = (m: Partial<Todo["source"]>): Pick<Todo, "source" | "state"> => ({
  state: "open",
  source: { kind: "owner", ...m },
})

const event = (id: number, m: Partial<TodoEvent>): TodoEvent => ({
  id,
  at: "2026-11-23T08:00:00Z",
  revision: id,
  actor: { kind: "owner" },
  kind: "edited",
  before: {},
  after: {},
  ...m,
})

// Scenario: issue #1111, section 2. The item's history is its thread: the owner's prompt, a Bot's progress and his
// answer are comments with their text, in order; every other change is a line of activity.
test("the thread keeps the comments among the activity, in order", () => {
  const thread = threadOf([
    event(1, { kind: "created" }),
    event(2, { kind: "edited", after: { assignee: "bot_main" } }),
    event(3, { kind: "commented", after: { text: "Do it now" } }),
    event(4, {
      kind: "commented",
      actor: { kind: "bot", id: "bot_main" },
      after: { text: "Started" },
    }),
    event(5, { kind: "commented", after: { text: "Keep the old button" } }),
  ])

  expect(thread.map((e) => (e.kind === "comment" ? `${e.actor.kind}: ${e.text}` : e.kind))).toEqual(
    ["activity", "activity", "owner: Do it now", "bot: Started", "owner: Keep the old button"],
  )
  expect(threadOf(undefined)).toEqual([])
})

// Scenario: issue #1111, section 3. Send is offered on an item from the owner's own chat or thread, not closed, and
// its confirmation names the recipient and the place.
test("send names the recipient and the place, and only for the owner's own chats", () => {
  const kai = source({
    kind: "whatsapp",
    account: "me",
    ref: { kind: "whatsapp_message", ref: "3EB0", label: "Kai Yong" },
  })
  expect(canSend(kai)).toBe(true)
  expect(sendQuestion(kai)).toBe("Send to Kai Yong on WhatsApp (me)?")

  expect(sendQuestion(source({ kind: "lark", id: "oc_1" }))).toBe(
    "Send to the chat it came from on Lark?",
  )
  expect(sendQuestion(source({ kind: "gmail", account: "work", id: "t1" }))).toBe(
    "Send to the thread on Gmail (work)?",
  )

  expect(canSend(source({ kind: "owner" }))).toBe(false)
  expect(canSend(source({ kind: "meeting", id: "mtg_1" }))).toBe(false)
  expect(canSend({ ...kai, state: "done" })).toBe(false)
})

// Scenario: issue #1111, section 1. An item is handed to a Bot unless it is closed or waits for an Approval.
test("an item is handed over unless it is closed or in review", () => {
  expect(canHand({ state: "open" })).toBe(true)
  expect(canHand({ state: "proposed" })).toBe(true)
  expect(canHand({ state: "doing" })).toBe(true)
  expect(canHand({ state: "review" })).toBe(false)
  expect(canHand({ state: "done" })).toBe(false)
})
