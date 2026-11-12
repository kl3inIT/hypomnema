// The grid (issue #235): up to four panes on one screen, two rows by two columns. A pane is a group of tabs
// (pane.ts); here it is only its id.
//
// Adapted from Omnigent (web/src/grid/gridLayout.ts, Apache License 2.0; web/THIRD_PARTY_NOTICES.md). Changed: a
// leaf is a pane's id, there is one grid and it has no name, and a pointer's place in a pane gives the edge here
// (Omnigent's drop zones are elements of its GridDropTargets.tsx).

export type SplitEdge = "left" | "right" | "top" | "bottom"
export type DropZone = SplitEdge | "center"
type Axis = "columns" | "rows"

// A layout is one pane, or two layouts side by side or one above the other.
export type GridLayout = string | { axis: Axis; children: [GridLayout, GridLayout] }

export const maxPanes = 4

export function gridPanes(layout: GridLayout): string[] {
  return typeof layout === "string" ? [layout] : layout.children.flatMap(gridPanes)
}

// splitGrid puts a pane on an edge of another, or returns null when it cannot: the pane is on screen already,
// there are four, or the pane was split that way before. Each axis is split once along a path, so the
// grid is at most two rows and two columns.
export function splitGrid(
  layout: GridLayout,
  target: string,
  pane: string,
  edge: SplitEdge,
  used: Axis[] = [],
): GridLayout | null {
  const panes = gridPanes(layout)
  if (!panes.includes(target) || panes.includes(pane) || panes.length >= maxPanes) return null

  const axis = edge === "left" || edge === "right" ? "columns" : "rows"
  if (typeof layout === "string") {
    if (used.includes(axis)) return null

    return { axis, children: edge === "left" || edge === "top" ? [pane, layout] : [layout, pane] }
  }

  const index = gridPanes(layout.children[0]).includes(target) ? 0 : 1
  const next = splitGrid(layout.children[index], target, pane, edge, [...used, layout.axis])
  if (next === null) return null

  return {
    ...layout,
    children: index === 0 ? [next, layout.children[1]] : [layout.children[0], next],
  }
}

// removePane takes a pane off the grid; its neighbour takes the room. null when it was the only one.
export function removePane(layout: GridLayout, pane: string): GridLayout | null {
  if (typeof layout === "string") return layout === pane ? null : layout

  const first = removePane(layout.children[0], pane)
  const second = removePane(layout.children[1], pane)
  if (first === null) return second
  if (second === null) return first

  return { ...layout, children: [first, second] }
}

// parseGrid reads a grid kept in the browser, or null when it is not one: no pane, a pane twice, or a shape the
// grid cannot have.
export function parseGrid(raw: string | null): GridLayout | null {
  const valid = (value: unknown, axes: Axis[] = []): value is GridLayout => {
    if (typeof value === "string") return value.length > 0
    if (!value || typeof value !== "object" || axes.length >= 2) return false

    const { axis, children } = value as Record<string, unknown>
    if (axis !== "columns" && axis !== "rows") return false

    return (
      !axes.includes(axis) &&
      Array.isArray(children) &&
      children.length === 2 &&
      children.every((child) => valid(child, [...axes, axis]))
    )
  }

  try {
    const value: unknown = JSON.parse(raw ?? "null")
    if (!valid(value)) return null

    const panes = gridPanes(value)
    if (new Set(panes).size !== panes.length) return null

    return value
  } catch {
    return null
  }
}

// dropZone is where a pointer is over a pane: the outer tenth of each side splits it on that side, the middle
// takes the tab into the pane. As Orca's resolveDropZone (src/renderer/src/components/tab-group/tab-drop-zone.ts,
// MIT): the sides are split by thirds, so a corner goes to the left or the right.
export function dropZone(
  rect: { left: number; top: number; width: number; height: number },
  x: number,
  y: number,
): DropZone {
  const across = x - rect.left
  const down = y - rect.top
  const sideX = rect.width * 0.1
  const sideY = rect.height * 0.1
  if (across > sideX && across < rect.width - sideX && down > sideY && down < rect.height - sideY) {
    return "center"
  }
  if (across < rect.width / 3) return "left"
  if (across > (rect.width * 2) / 3) return "right"

  return down < rect.height / 2 ? "top" : "bottom"
}
