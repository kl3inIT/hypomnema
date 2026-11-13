import { expect, test } from "vitest"

import { elapsed, since, span } from "./time"

const now = Date.parse("2026-11-12T10:00:00Z")
const before = (seconds: number) => new Date(now - seconds * 1000).toISOString()

// Scenario: something happened a few seconds ago; it reads "just now", not "0m ago", until a minute has passed.
test("under a minute is just now", () => {
  expect(since(before(0), now)).toBe("just now")
  expect(since(before(59), now)).toBe("just now")
  expect(since(before(-30), now)).toBe("just now")
  expect(since(before(60), now)).not.toBe("just now")
  expect(since(before(60), now)).toMatch(/1/)
})

test("later moments read in minutes, hours and days", () => {
  expect(since(before(5 * 60), now)).toMatch(/5/)
  expect(since(before(3 * 3600), now)).toMatch(/3/)
  expect(since(before(2 * 86400), now)).toMatch(/2/)
})

test("span says how long something has lasted", () => {
  expect(span(before(40), now)).toBe("40s")
  expect(span(before(6 * 60), now)).toBe("6m")
  expect(span(before(2 * 3600 + 5 * 60), now)).toBe("2h 5m")
})

test("elapsed keeps the seconds while a duration is under an hour", () => {
  expect(elapsed(before(34), now)).toBe("34s")
  expect(elapsed(before(94), now)).toBe("1m 34s")
  expect(elapsed(before(6 * 60 + 2), now)).toBe("6m 02s")
  expect(elapsed(before(2 * 3600 + 5 * 60 + 9), now)).toBe("2h 5m")
})
