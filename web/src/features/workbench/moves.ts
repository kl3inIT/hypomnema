// What dragging a tab and closing one do to the grid and its panes (issue #235), as Orca's tab groups do
// (src/renderer/src/components/tab-group/useTabDragSplit.ts): a tab dropped on the middle of a pane joins it, one
// dropped on a side makes a new pane there, and a pane left with no tab leaves the grid.

import { gridPanes, removePane, splitGrid, type DropZone, type GridLayout } from "./grid-layout"
import {
  closeTab,
  hasTab,
  openTab,
  paneChats,
  paneOf,
  tabKey,
  type Pane,
  type PaneTab,
} from "./pane"

// A bench is the grid and the panes on it.
export type Bench = { layout: GridLayout; panes: Record<string, Pane> }

// A tab being dragged, and the pane it comes from; none for a chat from the sidebar.
export type Dragged = { tab: PaneTab; from: string | null }

// sourceOf is the pane a dragged tab leaves: the one it was dragged from, or for a chat from the sidebar, the pane
// that has it open already, so that a chat is on the grid once.
function sourceOf(bench: Bench, dragged: Dragged): string | null {
  if (dragged.from !== null) return dragged.from

  const key = tabKey(dragged.tab)

  return (
    gridPanes(bench.layout).find((id) => {
      const pane = bench.panes[id]

      return pane !== undefined && hasTab(pane, key)
    }) ?? null
  )
}

// leave takes a tab out of a pane; a pane left empty leaves the grid. null when the grid is left with none.
function leave(bench: Bench, pane: string, key: string): Bench | null {
  const from = bench.panes[pane]
  if (from === undefined) return bench

  const next = closeTab(from, key)
  if (next !== null) return { ...bench, panes: { ...bench.panes, [pane]: next } }

  return withoutPane(bench, pane)
}

export function withoutPane(bench: Bench, pane: string): Bench | null {
  const layout = removePane(bench.layout, pane)
  if (layout === null) return null

  const panes = { ...bench.panes }
  delete panes[pane]

  return { layout, panes }
}

// dropTab is the bench after a tab is dropped on a zone of a pane, or null when that drop is not taken: on its own
// pane's middle, on a side of its own pane when it is that pane's only tab, or on a side when there is no room.
// `fresh` is the id a new pane gets.
export function dropTab(
  bench: Bench,
  target: string,
  zone: DropZone,
  dragged: Dragged,
  fresh: string,
): Bench | null {
  const into = bench.panes[target]
  if (into === undefined) return null

  const source = sourceOf(bench, dragged)
  const key = tabKey(dragged.tab)

  if (zone === "center") {
    if (source === target) return null

    const joined = { ...bench, panes: { ...bench.panes, [target]: openTab(into, dragged.tab) } }

    return source === null ? joined : leave(joined, source, key)
  }

  if (source === target && into.tabs.length === 1) return null

  const layout = splitGrid(bench.layout, target, fresh, zone)
  if (layout === null) return null

  const split = { layout, panes: { ...bench.panes, [fresh]: paneOf(dragged.tab) } }

  return source === null ? split : leave(split, source, key)
}

// alone is the chat a grid of one pane comes back to, shown on its own page: the chat in front, or the first chat.
// null while there are several panes, or when the one pane has no chat.
export function alone(bench: Bench): string | null {
  if (typeof bench.layout !== "string") return null

  const pane = bench.panes[bench.layout]
  if (pane === undefined) return null

  const front = pane.tabs.find((tab) => tabKey(tab) === pane.active)

  return front?.kind === "chat" ? front.chat : (paneChats(pane)[0] ?? null)
}
