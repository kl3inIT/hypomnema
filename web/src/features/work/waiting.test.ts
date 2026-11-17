import { expect, test } from "vitest"

import type { Bot } from "@/api/generated/types.gen"

import { waitingText } from "./waiting"

const vmap = { id: "bot_vmap", name: "vmap", max_dispatches: 4 } as Bot

// Scenario: a queued Task says in one sentence why it waits (issue #660). A Bot at its limit is named with its
// number; the owner's Runs without a Bot, a full fleet and the reasons that need someone are each said in words; a
// Task with no reason says nothing, and a reason this page does not know is still shown.
test("why a queued Task waits is said in one sentence", () => {
  expect(waitingText("bot_at_limit:bot_vmap", [vmap])).toBe(
    "Waiting: vmap is at its limit of 4 workers at once",
  )
  expect(waitingText("bot_at_limit:bot_gone", [vmap])).toBe(
    "Waiting: its Bot already runs as many workers as it may",
  )
  expect(waitingText("bot_at_limit:")).toBe(
    "Waiting: your Runs without a Bot already run as many workers as they may",
  )
  expect(waitingText("nodes_full")).toBe("Waiting: every machine that can run it is busy")
  expect(waitingText("nodes_low_memory")).toBe(
    "Waiting: every machine that can run it is low on memory",
  )
  expect(waitingText("no_node_reaches:git.vmaps.example")).toBe(
    "Waiting: no machine reaches git.vmaps.example",
  )
  expect(waitingText("no_node_has_tool:gws")).toBe("Waiting: no machine has gws installed")
  expect(waitingText("accounts_cooling")).toBe(
    "Waiting: every account of its agent waits for its limit to reset",
  )
  expect(waitingText(undefined)).toBeUndefined()
  expect(waitingText("something_new")).toBe("Waiting: something_new")
})
