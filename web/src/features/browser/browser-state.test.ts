import { describe, expect, it } from "vitest"

import type { BrowserTab } from "@/api/generated/types.gen"

import { chatBotTab, freshTabs, stripTabs } from "./browser-state"

const tab = (id: string, more: Partial<BrowserTab> = {}): BrowserTab => ({
  id,
  kind: "worker",
  url: "https://example.com/",
  title: "Example",
  views: 0,
  ...more,
})

const owner = tab("owner", { kind: "owner" })
const bot = tab("b-c1", { kind: "coordinator", chat_id: "c1", bot_id: "bot_1" })
const worker = tab("d-1", { chat_id: "c1" })
const other = tab("d-2", { chat_id: "c2" })

describe("freshTabs", () => {
  it("finds nothing new in the first list seen", () => {
    expect(freshTabs(null, [owner, bot], new Set(["c1"]))).toEqual([])
  })

  it("finds the tabs of these chats that were not in the list seen before", () => {
    const seen = new Set(["owner", "b-c1"])

    expect(freshTabs(seen, [owner, bot, worker, other], new Set(["c1"]))).toEqual([worker])
  })
})

describe("stripTabs", () => {
  it("lists the owner's tab first, then the tabs of the pane's chats", () => {
    expect(stripTabs([bot, owner, other, worker], ["c1"], []).map((t) => t.id)).toEqual([
      "owner",
      "b-c1",
      "d-1",
    ])
  })

  it("lists the owner's tab even before the browser has it", () => {
    expect(stripTabs(undefined, ["c1"], [])).toEqual([
      { id: "owner", kind: "owner", url: "", title: "", views: 0 },
    ])
  })

  it("lists the tabs of the chats whose tab is open in the pane, as beside a chat", () => {
    expect(stripTabs([owner, bot, worker, other], [], ["d-1"]).map((t) => t.id)).toEqual([
      "owner",
      "b-c1",
      "d-1",
    ])
  })
})

describe("chatBotTab", () => {
  it("is the chat's Bot's tab, not a worker's of the chat", () => {
    expect(chatBotTab([worker, bot], "c1")).toBe(bot)
    expect(chatBotTab([worker], "c1")).toBeUndefined()
  })
})
