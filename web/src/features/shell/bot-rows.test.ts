import { expect, test } from "vitest"

import type { Bot, Chat, Department } from "@/api/generated/types.gen"

import { botSections, reorderBots, rowChat } from "./bot-rows"

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
  persona: "",
  memory: "",
  memory_limit: 2200,
  fallbacks: [],
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

const department = (id: string, position: number): Department => ({ id, name: id, position })

// Scenario: the sidebar lists every Bot, with chats or not, in the order the owner set (issues #470 and #471): the
// main Bot first whatever its place and whatever department it names, then each department by its position with
// its Bots by theirs, an empty one too, then the Bots in none; a Bot naming a department not read yet is in none;
// each Bot's chats newest first.
test("every Bot is listed by its position, the main one first, then each department, then the rest", () => {
  const sections = botSections(
    [
      bot("reporter", 1),
      bot("researcher", 3, { department: "maps" }),
      bot("coordinator", 2, { department: "maps" }),
      bot("writer", 0),
      bot("tester", 4, { department: "maps" }),
      bot("pilot", 5, { department: "pilot" }),
      bot("stray", 6, { department: "gone" }),
    ],
    [
      chat("old", "researcher", "2026-11-10T09:00:00Z"),
      chat("new", "researcher", "2026-11-11T09:00:00Z"),
    ],
    [department("pilot", 1), department("maps", 0), department("empty", 2)],
  )

  expect(sections.main?.bot).toBe("coordinator")
  expect(sections.departments.map((d) => [d.department.id, ids(d.rows)])).toEqual([
    ["maps", ["researcher", "tester"]],
    ["pilot", ["pilot"]],
    ["empty", []],
  ])
  expect(ids(sections.rest)).toEqual(["writer", "reporter", "stray"])
  expect(sections.departments[0]!.rows[0]!.chats.map((c) => c.id)).toEqual(["new", "old"])
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
  expect(botSections([], [])).toEqual({ main: undefined, departments: [], rest: [] })
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

// Scenario: a Bot's row stands for its main chat (issue #471), which the server names, whatever chat moved last; a
// Bot with none has no main chat on its row.
test("a Bot's row names its main chat", () => {
  const sections = botSections(
    [bot("coordinator", 0, { main_chat: "home" }), bot("writer", 1)],
    [
      chat("home", "coordinator", "2026-11-10T09:00:00Z"),
      chat("lark", "coordinator", "2026-11-11T09:00:00Z"),
      chat("draft", "writer", "2026-11-11T09:00:00Z"),
    ],
  )

  expect(sections.main?.mainChat).toBe("home")
  expect(sections.main?.chats.map((c) => c.id)).toEqual(["lark", "home"])
  expect(sections.rest[0]?.mainChat).toBeUndefined()
})

// Scenario: the row opens the main chat (issue #471); when another chat of the Bot waits on the owner and the main
// chat does not, the row leads to the newest chat that waits; a Bot with no main chat and nothing waiting opens none,
// and its row starts a chat.
test("a Bot's row opens its main chat, or the chat that waits on the owner", () => {
  const chats = [
    chat("routine", "coordinator", "2026-11-12T09:00:00Z"),
    chat("old", "coordinator", "2026-11-11T09:00:00Z"),
    chat("home", "coordinator", "2026-11-10T09:00:00Z"),
  ]
  const row = { bot: "coordinator", mainChat: "home", chats }

  expect(rowChat(row, new Set())).toBe("home")
  expect(rowChat(row, new Set(["old"]))).toBe("old")
  expect(rowChat(row, new Set(["old", "routine"]))).toBe("routine")
  expect(rowChat(row, new Set(["old", "home"]))).toBe("home")
  expect(rowChat({ bot: "writer", chats: [] }, new Set())).toBeUndefined()
  expect(rowChat({ bot: "coordinator", chats }, new Set(["old"]))).toBe("old")
})
