import { beforeEach, expect, test } from "vitest"

import type { Bot, Chat } from "@/api/generated/types.gen"

import { botSections, keepOpenBots, readOpenBots, reorderBots, visibleChats } from "./bot-rows"

beforeEach(() => window.localStorage.clear())

const chat = (id: string, bot: string, updated: string): Chat => ({
  id,
  title: id,
  bot,
  working: false,
  coordinator: true,
  created_at: "2026-11-01T00:00:00Z",
  updated_at: updated,
})

const bot = (id: string, position: number, m: Partial<Bot> = {}): Bot => ({
  id,
  name: id,
  main: id === "coordinator",
  position,
  pinned: false,
  persona: "",
  memory: "",
  memory_limit: 2200,
  skills: [],
  skills_off: [],
  preset: "full",
  tools: [],
  tools_off: [],
  connections: { limited: false, names: [] },
  workers: { limited: false, names: [] },
  scopes: {},
  tool_display: "on",
  guaranteed_dispatches: 4,
  created_at: "2026-11-01T00:00:00Z",
  updated_at: "2026-11-01T00:00:00Z",
  ...m,
})

const ids = (rows: { bot: string }[]) => rows.map((r) => r.bot)

// Scenario: the sidebar lists every Bot, with chats or not, in the order the owner set (issue #470): the main Bot
// first whatever its place, the pinned ones next, the others after; each Bot's chats newest first.
test("every Bot is listed by its position, the main one first and the pinned ones next", () => {
  const sections = botSections(
    [
      bot("reporter", 1),
      bot("researcher", 3, { pinned: true }),
      bot("coordinator", 2, { pinned: true }),
      bot("writer", 0),
      bot("tester", 4, { pinned: true }),
    ],
    [
      chat("old", "researcher", "2026-11-10T09:00:00Z"),
      chat("new", "researcher", "2026-11-11T09:00:00Z"),
    ],
  )

  expect(sections.main?.bot).toBe("coordinator")
  expect(ids(sections.pinned)).toEqual(["researcher", "tester"])
  expect(ids(sections.rest)).toEqual(["writer", "reporter"])
  expect(sections.pinned[0]!.chats.map((c) => c.id)).toEqual(["new", "old"])
  expect(sections.rest[0]!.chats).toEqual([])
})

// Scenario: a chat whose Bot is not read yet, made a moment ago, is listed at the end rather than lost.
test("a chat of a Bot not read yet is listed after the others", () => {
  const sections = botSections(
    [bot("coordinator", 0)],
    [chat("a", "fresh", "2026-11-12T09:00:00Z")],
  )

  expect(sections.main?.bot).toBe("coordinator")
  expect(ids(sections.rest)).toEqual(["fresh"])
  expect(botSections([], [])).toEqual({ main: undefined, pinned: [], rest: [] })
})

// Scenario: a drag in one section names only that section's Bots; they take the places they held together and the
// others keep theirs, as the server orders them.
test("a reorder moves the Bots named into the places they held", () => {
  const bots = [bot("coordinator", 0), bot("a", 1), bot("b", 2), bot("c", 3), bot("d", 4)]

  expect(reorderBots(bots, ["d", "b"]).map((b) => [b.id, b.position])).toEqual([
    ["coordinator", 0],
    ["a", 1],
    ["d", 2],
    ["c", 3],
    ["b", 4],
  ])
  expect(reorderBots(bots, ["a", "b"])[1]).toBe(bots[1])
})

// Scenario: which Bots the owner opened survives a reload; what the browser kept that is not such a record is
// read as nothing opened.
test("the Bots the owner opened are kept, and anything else kept reads as none", () => {
  keepOpenBots({ coordinator: true, reporter: false })
  expect(readOpenBots()).toEqual({ coordinator: true, reporter: false })

  window.localStorage.setItem("hypomnema.sidebar.open-bots", "not json")
  expect(readOpenBots()).toEqual({})
  window.localStorage.setItem("hypomnema.sidebar.open-bots", '["coordinator"]')
  expect(readOpenBots()).toEqual({})
})

// Scenario: an open Bot with 15 chats, newest first, lists its 6 newest (issue #599); the open chat, a working one and
// one waiting on the owner are listed whatever their age, in the list's order; Show all lists every chat.
test("an open Bot lists its 6 newest chats and every chat that needs looking at", () => {
  const list = Array.from({ length: 15 }, (_, i) =>
    chat(`c${i}`, "coordinator", `2026-11-${String(30 - i).padStart(2, "0")}T09:00:00Z`),
  )
  list[9] = { ...list[9]!, working: true }

  expect(visibleChats(list, undefined, new Set(), false).map((c) => c.id)).toEqual([
    "c0",
    "c1",
    "c2",
    "c3",
    "c4",
    "c5",
    "c9",
  ])
  expect(visibleChats(list, "c14", new Set(["c11"]), false).map((c) => c.id)).toEqual([
    "c0",
    "c1",
    "c2",
    "c3",
    "c4",
    "c5",
    "c9",
    "c11",
    "c14",
  ])
  expect(visibleChats(list, "c14", new Set(["c11"]), true)).toEqual(list)
  expect(visibleChats(list.slice(0, 4), undefined, new Set(), false)).toEqual(list.slice(0, 4))
})
