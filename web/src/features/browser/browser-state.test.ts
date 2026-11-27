import { describe, expect, it } from "vitest"

import type { BrowserTab } from "@/api/generated/types.gen"

import { chatBotTab, elsewhereLine, freshTabs, heldLine, stripTabs } from "./browser-state"

const tab = (id: string, more: Partial<BrowserTab> = {}): BrowserTab => ({
  id,
  kind: "worker",
  url: "https://example.com/",
  title: "Example",
  views: 0,
  held: false,
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
      { id: "owner", kind: "owner", url: "", title: "", views: 0, held: false },
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

// Taking control holds only the tab taken (issue #1113): what the owner is told names the tabs he holds and whose
// tools wait.
describe("heldLine", () => {
  it("says nothing while no tab is held", () => {
    expect(heldLine([owner, bot], false)).toBeNull()
  })

  it("says the agents go on while only the owner's own tab is held", () => {
    expect(heldLine([{ ...owner, held: true }, bot], true)).toBe(
      "You hold your own tab. The agents go on in their own tabs.",
    )
  })

  it("names an agent's tab held, whose agent waits", () => {
    expect(heldLine([owner, { ...bot, held: true, title: "Maps search" }], true)).toBe(
      "You hold “Maps search”. Its agent's browser tools are paused until you hand it back.",
    )
  })

  it("names every tab held", () => {
    const tabs = [
      { ...owner, held: true },
      { ...bot, held: true },
      { ...worker, held: true },
    ]

    expect(heldLine(tabs, true)).toBe(
      "You hold your own tab, “Example” and “Example”. Their agents' browser tools are paused until you hand them back.",
    )
  })

  it("still says a tab is held when the list does not show it", () => {
    expect(heldLine([owner], true)).toBe("You hold a tab of the browser.")
  })
})

describe("elsewhereLine", () => {
  it("names the other tabs held, and says this tab goes on", () => {
    expect(elsewhereLine([{ ...owner, held: true }, bot], "b-c1")).toBe(
      "You hold your own tab. This tab's agent goes on.",
    )
  })

  it("says nothing of the tab itself or when nothing else is held", () => {
    expect(elsewhereLine([owner, { ...bot, held: true }], "b-c1")).toBeNull()
    expect(elsewhereLine([owner, bot], "b-c1")).toBeNull()
  })
})
