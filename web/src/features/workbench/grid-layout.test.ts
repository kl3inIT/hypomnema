import { describe, expect, it } from "vitest"

import {
  edgeOf,
  gridChats,
  parseGrid,
  removeChat,
  splitGrid,
  swapChats,
  type GridLayout,
} from "./grid-layout"

const two: GridLayout = { axis: "columns", children: ["a", "b"] }
const four: GridLayout = {
  axis: "columns",
  children: [
    { axis: "rows", children: ["a", "c"] },
    { axis: "rows", children: ["b", "d"] },
  ],
}

describe("a chat put on an edge of a pane", () => {
  it("makes a grid of two from one chat, on the side it was dropped", () => {
    expect(splitGrid("a", "a", "b", "right")).toEqual(two)
    expect(splitGrid("a", "a", "b", "left")).toEqual({ axis: "columns", children: ["b", "a"] })
    expect(splitGrid("a", "a", "b", "top")).toEqual({ axis: "rows", children: ["b", "a"] })
    expect(splitGrid("a", "a", "b", "bottom")).toEqual({ axis: "rows", children: ["a", "b"] })
  })

  it("splits the pane it was dropped on, and leaves the others", () => {
    expect(splitGrid(two, "b", "c", "bottom")).toEqual({
      axis: "columns",
      children: ["a", { axis: "rows", children: ["b", "c"] }],
    })
  })

  it("keeps the grid to two rows and two columns", () => {
    // A pane that is one of two columns cannot be cut into columns again.
    expect(splitGrid(two, "b", "c", "right")).toBeNull()
    expect(splitGrid({ axis: "rows", children: ["a", "b"] }, "a", "c", "top")).toBeNull()
  })

  it("takes four chats and no fifth, and no chat twice", () => {
    const three = splitGrid(two, "a", "c", "bottom")!
    const all = splitGrid(three, "b", "d", "bottom")!

    expect(all).toEqual(four)
    expect(splitGrid(all, "a", "e", "left")).toBeNull()
    expect(splitGrid(two, "a", "b", "bottom")).toBeNull()
    expect(splitGrid(two, "x", "c", "bottom"), "a pane that is not there").toBeNull()
  })
})

describe("a chat taken off the grid", () => {
  it("gives its room to its neighbour", () => {
    expect(removeChat(four, "c")).toEqual({
      axis: "columns",
      children: ["a", { axis: "rows", children: ["b", "d"] }],
    })
  })

  it("leaves one chat, which is no grid, and nothing after the last", () => {
    expect(removeChat(two, "a")).toBe("b")
    expect(removeChat("a", "a")).toBeNull()
    expect(removeChat(two, "x")).toEqual(two)
  })
})

describe("two panes changing places", () => {
  it("swaps the chats and keeps the shape", () => {
    expect(gridChats(swapChats(four, "a", "d"))).toEqual(["d", "c", "b", "a"])
    expect(swapChats(two, "a", "x"), "a chat that is not on the grid").toBe(two)
    expect(swapChats(two, "a", "a")).toBe(two)
  })
})

describe("a grid kept in the browser", () => {
  it("is read back as it was kept", () => {
    expect(parseGrid(JSON.stringify(four))).toEqual(four)
  })

  it("is not taken when it is not a grid", () => {
    for (const kept of [
      null,
      "",
      "not JSON",
      '"a"',
      JSON.stringify({ axis: "columns", children: ["a", "a"] }),
      JSON.stringify({ axis: "columns", children: ["a"] }),
      JSON.stringify({ axis: "diagonal", children: ["a", "b"] }),
      JSON.stringify({
        axis: "columns",
        children: ["a", { axis: "columns", children: ["b", "c"] }],
      }),
      JSON.stringify({ axis: "columns", children: ["a", ""] }),
    ]) {
      expect(parseGrid(kept)).toBeNull()
    }
  })
})

describe("the edge of a pane a pointer is over", () => {
  const pane = { left: 100, top: 50, width: 400, height: 200 }

  it("is the left or the right in the outer quarters, else the top or the bottom half", () => {
    expect(edgeOf(pane, 150, 150)).toBe("left")
    expect(edgeOf(pane, 450, 150)).toBe("right")
    expect(edgeOf(pane, 300, 100)).toBe("top")
    expect(edgeOf(pane, 300, 200)).toBe("bottom")
  })
})
