import { expect, test } from "vitest"

import type { ChatMessage } from "@/api/generated/types.gen"

import { recall, sentTexts, type Walk } from "./recall"

const sent = ["deploy it", "and a test\nfor it", "add a health page"]

// press presses ↑ or ↓ in a box holding text with the caret at caret, and gives the box after.
function press(walk: Walk, key: "up" | "down", text: string, caret = text.length) {
  return recall(walk, key, text, caret, sent)
}

// Scenario: in an empty box the owner presses ↑ three times, then ↓ until the box is empty again.
test("↑ walks back through what was sent and ↓ comes back to the empty box", () => {
  expect(press(null, "up", "", 0)).toEqual({ walk: 0, text: "deploy it", caret: 0 })
  expect(press(0, "up", "deploy it", 0)).toEqual({ walk: 1, text: "and a test\nfor it", caret: 0 })
  expect(press(1, "up", "and a test\nfor it", 0)).toEqual({
    walk: 2,
    text: "add a health page",
    caret: 0,
  })
  // At the oldest, ↑ is the textarea's own.
  expect(press(2, "up", "add a health page", 0)).toBeNull()

  expect(press(2, "down", "add a health page")).toEqual({
    walk: 1,
    text: "and a test\nfor it",
    caret: "and a test\nfor it".length,
  })
  expect(press(1, "down", "and a test\nfor it")).toEqual({ walk: 0, text: "deploy it", caret: 9 })
  expect(press(0, "down", "deploy it")).toEqual({ walk: null, text: "", caret: 0 })
  expect(press(null, "down", "")).toBeNull()
})

// Scenario: the box holds what the owner is writing. ↑ and ↓ move the caret; they bring nothing back.
test("↑ does not start the walk in a box with text", () => {
  expect(press(null, "up", "half a thought", 0)).toBeNull()
  expect(press(null, "up", "half a thought")).toBeNull()
  expect(press(null, "down", "half a thought")).toBeNull()
})

// Scenario: a message of two lines is in the box from the walk; with the caret inside it, the arrows move between
// its lines.
test("while walking, the arrows step only from the start or the end of the text", () => {
  expect(press(1, "up", "and a test\nfor it", 12)).toBeNull()
  expect(press(1, "down", "and a test\nfor it", 3)).toBeNull()
  expect(press(1, "up", "and a test\nfor it")?.walk).toBe(2)
})

test("nothing sent, nothing to bring back", () => {
  expect(recall(null, "up", "", 0, [])).toBeNull()
})

const message = (seq: number, role: ChatMessage["role"], text: string): ChatMessage => ({
  seq,
  chat_id: "cht_1",
  role,
  text,
  created_at: "2026-11-14T10:00:00Z",
})

// Scenario: the chat holds the owner's messages among pi's replies and the tools it ran. ↑ brings back only the
// owner's, newest first, and one sent twice in a row once.
test("the texts walked are the owner's, newest first", () => {
  expect(
    sentTexts([
      message(1, "owner", "add a health page"),
      message(2, "coordinator", "Done."),
      message(3, "owner", "deploy it"),
      message(4, "tool", "ok"),
      message(5, "owner", "deploy it"),
      message(6, "owner", "  "),
    ]),
  ).toEqual(["deploy it", "add a health page"])
})
