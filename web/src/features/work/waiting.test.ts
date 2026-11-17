import { expect, test } from "vitest"

import { waitingText } from "./waiting"

// Scenario: a queued Task says in one sentence why it waits (issues #660, #731). A full fleet, a request larger than
// every machine and the reasons that need someone are each said in words; a Task with no reason says nothing, and a
// reason this page does not know is still shown.
test("why a queued Task waits is said in one sentence", () => {
  expect(waitingText("nodes_full")).toBe("Waiting: every machine that can run it is busy")
  expect(waitingText("no_node_fits")).toBe(
    "Waiting: it asks for more CPU or memory than any machine that can run it has",
  )
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
