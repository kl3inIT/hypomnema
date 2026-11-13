import { expect, test } from "vitest"

import { newChatId } from "./chat-id"

// Scenario: the New chat page chooses its chat's id when it opens (issue #357). It has the shape the server makes
// and holds a client to, and each page chooses another.
test("a new chat's id has the server's shape and is new each time", () => {
  const ids = Array.from({ length: 50 }, newChatId)

  for (const id of ids) expect(id).toMatch(/^cht_[a-z2-7]{26}$/)
  expect(new Set(ids).size).toBe(ids.length)
})
