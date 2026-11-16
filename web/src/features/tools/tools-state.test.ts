import { expect, test } from "vitest"

import { familyOn, kept, matches, others } from "./tools-state"

const browser = { pattern: "agent_browser_*", tools: ["agent_browser_open", "agent_browser_click"] }
const memory = { pattern: "memory", tools: ["memory"] }

// An entry is a name or a family, as the control plane reads it (issue #536).
test("an entry names a tool by its name or its family", () => {
  expect(matches("agent_browser_*", "agent_browser_open")).toBe(true)
  expect(matches("memory", "memory")).toBe(true)
  expect(matches("memory", "memory_x")).toBe(false)
  expect(matches("*", "routines")).toBe(true)
})

// A family's switch is on unless its own entry is off or a list of only some tools leaves it out.
test("a family is on unless turned off or left out of the list", () => {
  expect(familyOn(browser, [], [])).toBe(true)
  expect(familyOn(browser, [], ["agent_browser_*"])).toBe(false)
  expect(kept(memory, ["agent_browser_open"])).toBe(false)
  expect(familyOn(browser, ["agent_browser_open"], [])).toBe(true)
})

// What a switch does not set, the form keeps and shows.
test("entries no switch sets are kept apart", () => {
  expect(others([browser, memory], ["agent_browser_*", "agent_browser_eval"])).toEqual([
    "agent_browser_eval",
  ])
})
