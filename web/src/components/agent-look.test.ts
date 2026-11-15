import { expect, test } from "vitest"

import { look } from "./agent-look"

// An agent keeps its face: the same name gives the same body and colour, and the starting agents differ.
test("an agent's avatar comes from its name", () => {
  expect(look("reviewer")).toEqual(look("reviewer"))

  const faces = ["implementer", "reviewer", "explorer", "coordinator"].map((name) => {
    const { body, color } = look(name)

    return `${body.id} ${color}`
  })
  expect(new Set(faces).size).toBe(4)
})
