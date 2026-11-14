import { describe, expect, it } from "vitest"

import { alone, dropTab, withoutPane, type Bench } from "./moves"
import { closeTab, openTab, paneOf, replaceTab, type PaneTab } from "./pane"

const chat = (id: string): PaneTab => ({ kind: "chat", chat: id })
const browser: PaneTab = { kind: "browser", tab: "owner" }

// Two panes side by side: p1 has chats a and b, b in front; p2 has chat c.
const p1 = { tabs: [chat("a"), chat("b")], active: "chat:b" }
const bench: Bench = {
  layout: { axis: "columns", children: ["p1", "p2"] },
  panes: { p1, p2: paneOf(chat("c")) },
}

describe("a tab dropped in the middle of a pane", () => {
  it("joins that pane in front, and leaves the pane it came from", () => {
    const next = dropTab(bench, "p2", "center", { tab: chat("b"), from: "p1" }, "new")

    expect(next?.panes.p2).toEqual({ tabs: [chat("c"), chat("b")], active: "chat:b" })
    expect(next?.panes.p1).toEqual({ tabs: [chat("a")], active: "chat:a" })
  })

  it("takes a chat from the sidebar out of the pane that has it, so a chat is on the grid once", () => {
    const next = dropTab(bench, "p1", "center", { tab: chat("c"), from: null }, "new")

    expect(next).toEqual({ layout: "p1", panes: { p1: openTab(p1, chat("c")) } })
  })

  it("is not taken on the pane it came from", () => {
    expect(dropTab(bench, "p1", "center", { tab: chat("a"), from: "p1" }, "new")).toBeNull()
  })
})

describe("a tab dropped on a side of a pane", () => {
  it("makes a new pane of it there", () => {
    const next = dropTab(bench, "p2", "bottom", { tab: browser, from: null }, "p3")

    expect(next?.layout).toEqual({
      axis: "columns",
      children: ["p1", { axis: "rows", children: ["p2", "p3"] }],
    })
    expect(next?.panes.p3).toEqual(paneOf(browser))
  })

  it("splits its own pane when that pane has other tabs, and not when it is the only one", () => {
    const next = dropTab(bench, "p1", "bottom", { tab: chat("a"), from: "p1" }, "p3")

    expect(next?.panes.p1?.tabs).toEqual([chat("b")])
    expect(next?.panes.p3).toEqual(paneOf(chat("a")))
    expect(dropTab(bench, "p2", "bottom", { tab: chat("c"), from: "p2" }, "p3")).toBeNull()
  })

  it("takes away a pane it leaves empty", () => {
    const next = dropTab(bench, "p1", "bottom", { tab: chat("c"), from: "p2" }, "p3")

    expect(next?.layout).toEqual({ axis: "rows", children: ["p1", "p3"] })
    expect(next?.panes.p2).toBeUndefined()
  })

  it("is not taken where the grid has no room", () => {
    // p1 is a column already: it cannot be cut into columns again.
    expect(dropTab(bench, "p1", "right", { tab: browser, from: null }, "p3")).toBeNull()
  })
})

describe("closing", () => {
  it("a tab brings the one after it, or else before it, in front", () => {
    const three = { tabs: [chat("a"), chat("b"), chat("c")], active: "chat:b" }

    expect(closeTab(three, "chat:b")?.active).toBe("chat:c")
    expect(closeTab({ ...three, active: "chat:c" }, "chat:c")?.active).toBe("chat:b")
    expect(closeTab(three, "chat:a")?.active, "one behind").toBe("chat:b")
  })

  it("the last tab of a pane takes the pane off the grid, and the last pane leaves none", () => {
    expect(closeTab(paneOf(chat("c")), "chat:c")).toBeNull()
    expect(withoutPane(bench, "p2")?.layout).toBe("p1")
    expect(withoutPane({ layout: "p1", panes: bench.panes }, "p1")).toBeNull()
  })
})

describe("a grid of one pane", () => {
  it("comes back to its chat in front, or else its first chat, and to none without a chat", () => {
    expect(alone({ layout: "p1", panes: bench.panes })).toBe("b")
    expect(
      alone({
        layout: "p1",
        panes: { p1: { tabs: [browser, chat("a")], active: "browser:owner" } },
      }),
    ).toBe("a")
    expect(alone({ layout: "p1", panes: { p1: paneOf(browser) } })).toBeNull()
    expect(alone(bench), "two panes are a grid").toBeNull()
  })
})

describe("a new chat", () => {
  it("takes its tab's place once it is made", () => {
    const draft: PaneTab = { kind: "draft", id: "d1", bot: "" }
    const pane = openTab(paneOf(chat("a")), draft)

    expect(replaceTab(pane, "draft:d1", chat("n"))).toEqual({
      tabs: [chat("a"), chat("n")],
      active: "chat:n",
    })
  })
})
