import { expect, test } from "vitest"

import { toolEntries } from "./tools-state"

test("exceptions preserve tool and family names and omit empty entries", () => {
  expect(toolEntries(["phone", " agent_browser_* ", "", "  "])).toEqual([
    "phone",
    "agent_browser_*",
  ])
})
