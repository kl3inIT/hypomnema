// The grid of chats (issue #235): up to four chats on one screen, two rows by two columns.
//
// Adapted from Omnigent (web/src/grid/gridLayout.ts, Apache License 2.0; web/THIRD_PARTY_NOTICES.md). Changed: a
// leaf is a chat, there is one grid and it has no name, and a pointer's place in a pane gives the edge here
// (Omnigent's drop zones are elements of its GridDropTargets.tsx).

export type SplitEdge = "left" | "right" | "top" | "bottom"
type Axis = "columns" | "rows"

// A layout is one chat, or two layouts side by side or one above the other.
export type GridLayout = string | { axis: Axis; children: [GridLayout, GridLayout] }

export const maxPanes = 4

export function gridChats(layout: GridLayout): string[] {
  return typeof layout === "string" ? [layout] : layout.children.flatMap(gridChats)
}

// splitGrid puts a chat on an edge of the pane of another, or returns null when it cannot: the chat is on screen
// already, there are four, or the pane was split that way before. Each axis is split once along a path, so the
// grid is at most two rows and two columns.
export function splitGrid(
  layout: GridLayout,
  target: string,
  chat: string,
  edge: SplitEdge,
  used: Axis[] = [],
): GridLayout | null {
  const chats = gridChats(layout)
  if (!chats.includes(target) || chats.includes(chat) || chats.length >= maxPanes) return null

  const axis = edge === "left" || edge === "right" ? "columns" : "rows"
  if (typeof layout === "string") {
    if (used.includes(axis)) return null

    return { axis, children: edge === "left" || edge === "top" ? [chat, layout] : [layout, chat] }
  }

  const index = gridChats(layout.children[0]).includes(target) ? 0 : 1
  const next = splitGrid(layout.children[index], target, chat, edge, [...used, layout.axis])
  if (next === null) return null

  return {
    ...layout,
    children: index === 0 ? [next, layout.children[1]] : [layout.children[0], next],
  }
}

// removeChat takes a chat off the grid; its neighbour takes the room. null when it was the only one.
export function removeChat(layout: GridLayout, chat: string): GridLayout | null {
  if (typeof layout === "string") return layout === chat ? null : layout

  const first = removeChat(layout.children[0], chat)
  const second = removeChat(layout.children[1], chat)
  if (first === null) return second
  if (second === null) return first

  return { ...layout, children: [first, second] }
}

// swapChats makes two chats change places.
export function swapChats(layout: GridLayout, a: string, b: string): GridLayout {
  const chats = gridChats(layout)
  if (a === b || !chats.includes(a) || !chats.includes(b)) return layout

  const swap = (node: GridLayout): GridLayout =>
    typeof node === "string"
      ? node === a
        ? b
        : node === b
          ? a
          : node
      : { ...node, children: [swap(node.children[0]), swap(node.children[1])] }

  return swap(layout)
}

// parseGrid reads a grid kept in the browser, or null when it is not one: fewer than two chats, a chat twice, or
// a shape the grid cannot have.
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

    const chats = gridChats(value)
    if (chats.length < 2 || new Set(chats).size !== chats.length) return null

    return value
  } catch {
    return null
  }
}

// edgeOf is the edge of a pane a pointer is over: a quarter on each side, the middle half above and below
// (Omnigent's GridDropTargets.tsx, with four edges).
export function edgeOf(
  rect: { left: number; top: number; width: number; height: number },
  x: number,
  y: number,
): SplitEdge {
  const across = (x - rect.left) / rect.width
  if (across < 0.25) return "left"
  if (across > 0.75) return "right"

  return (y - rect.top) / rect.height < 0.5 ? "top" : "bottom"
}
