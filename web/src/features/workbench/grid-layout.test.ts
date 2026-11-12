import { describe, expect, it } from "vitest"

import { dropZone, parseGrid, removePane, splitGrid, type GridLayout } from "./grid-layout"

const two: GridLayout = { axis: "columns", children: ["a", "b"] }
const four: GridLayout = {
  axis: "columns",
  children: [
    { axis: "rows", children: ["a", "c"] },
    { axis: "rows", children: ["b", "d"] },
  ],
}

describe("a pane put on an edge of another", () => {
  it("makes a grid of two from one pane, on the side it was dropped", () => {
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

  it("takes four panes and no fifth, and no pane twice", () => {
    const three = splitGrid(two, "a", "c", "bottom")!
    const all = splitGrid(three, "b", "d", "bottom")!

    expect(all).toEqual(four)
    expect(splitGrid(all, "a", "e", "left")).toBeNull()
    expect(splitGrid(two, "a", "b", "bottom")).toBeNull()
    expect(splitGrid(two, "x", "c", "bottom"), "a pane that is not there").toBeNull()
  })
})

describe("a pane taken off the grid", () => {
  it("gives its room to its neighbour", () => {
    expect(removePane(four, "c")).toEqual({
      axis: "columns",
      children: ["a", { axis: "rows", children: ["b", "d"] }],
    })
  })

  it("leaves one pane, and nothing after the last", () => {
    expect(removePane(two, "a")).toBe("b")
    expect(removePane("a", "a")).toBeNull()
    expect(removePane(two, "x")).toEqual(two)
  })
})

describe("a grid kept in the browser", () => {
  it("is read back as it was kept, a grid of one pane too", () => {
    expect(parseGrid(JSON.stringify(four))).toEqual(four)
    expect(parseGrid('"a"')).toBe("a")
  })

  it("is not taken when it is not a grid", () => {
    for (const kept of [
      null,
      "",
      "not JSON",
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

describe("where a pointer is over a pane", () => {
  const pane = { left: 100, top: 50, width: 400, height: 200 }

  it("is the middle inside the outer tenth of each side, as in Orca", () => {
    expect(dropZone(pane, 300, 150)).toBe("center")
    expect(dropZone(pane, 141, 71)).toBe("center")
  })

  it("is a side in the outer tenth: the left or the right by thirds, else the top or the bottom", () => {
    expect(dropZone(pane, 120, 150)).toBe("left")
    expect(dropZone(pane, 490, 150)).toBe("right")
    expect(dropZone(pane, 300, 55)).toBe("top")
    expect(dropZone(pane, 300, 245)).toBe("bottom")
    // A corner goes to the side.
    expect(dropZone(pane, 105, 55)).toBe("left")
  })
})
