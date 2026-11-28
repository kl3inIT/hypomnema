import { describe, expect, it } from "vitest"

import type { BrowserTab } from "@/api/generated/types.gen"

import { paneOf, parsePane } from "./pane"
import { paneStrip, pickFromStrip } from "./strip"

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
const tabs = [owner, bot, worker, other]
const ids = (list: BrowserTab[]) => list.map((t) => t.id)

describe("the browser strip of a pane beside a chat", () => {
  it("still lists the chat's tabs once the owner's tab is shown in their place", () => {
    const beside = paneOf({ kind: "browser", tab: "b-c1" })
    expect(ids(paneStrip(beside, tabs))).toEqual(["owner", "b-c1", "d-1"])

    const mine = pickFromStrip(beside, "browser:b-c1", "owner", tabs)
    expect(mine.active).toBe("browser:owner")
    expect(ids(paneStrip(mine, tabs))).toEqual(["owner", "b-c1", "d-1"])

    // The Bot's tab is picked again from the same strip, and then the worker's.
    const back = pickFromStrip(mine, "browser:owner", "b-c1", tabs)
    expect(back.active).toBe("browser:b-c1")
    const theirs = pickFromStrip(back, "browser:b-c1", "d-1", tabs)
    expect(ids(paneStrip(theirs, tabs))).toEqual(["owner", "b-c1", "d-1"])
  })

  it("keeps the chat's tabs listed after the pane is read back from the browser's storage", () => {
    const mine = pickFromStrip(
      paneOf({ kind: "browser", tab: "b-c1" }),
      "browser:b-c1",
      "owner",
      tabs,
    )
    const kept = parsePane(JSON.parse(JSON.stringify(mine)))
    if (kept === null) throw new Error("the pane was not read back")

    expect(ids(paneStrip(kept, tabs))).toEqual(["owner", "b-c1", "d-1"])
  })

  it("keeps the chat's tabs listed when the tab shown is gone from the browser", () => {
    const theirs = pickFromStrip(
      paneOf({ kind: "browser", tab: "b-c1" }),
      "browser:b-c1",
      "d-1",
      tabs,
    )

    expect(ids(paneStrip(theirs, [owner, bot, other]))).toEqual(["owner", "b-c1"])
  })
})
