import { beforeEach, expect, test } from "vitest"

import type { Chat } from "@/api/generated/types.gen"

import { botRows, keepOpenBots, readOpenBots } from "./bot-rows"

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

// Scenario: the sidebar lists a Bot a row, the one whose chat moved last first, each with its chats newest first.
test("chats are grouped by their Bot, the Bot whose chat moved last first", () => {
  const rows = botRows([
    chat("old", "coordinator", "2026-11-10T09:00:00Z"),
    chat("research", "researcher", "2026-11-12T09:00:00Z"),
    chat("new", "coordinator", "2026-11-11T09:00:00Z"),
  ])

  expect(rows.map((r) => [r.bot, r.chats.map((c) => c.id)])).toEqual([
    ["researcher", ["research"]],
    ["coordinator", ["new", "old"]],
  ])
})

// Scenario: coordinator is listed even before it has a chat, so a first chat can be started from it; it comes
// after the Bots that have chats.
test("coordinator is always listed, last while it has no chat", () => {
  expect(botRows([]).map((r) => [r.bot, r.chats.length])).toEqual([["coordinator", 0]])
  expect(botRows([chat("a", "reporter", "2026-11-12T09:00:00Z")]).map((r) => r.bot)).toEqual([
    "reporter",
    "coordinator",
  ])
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
