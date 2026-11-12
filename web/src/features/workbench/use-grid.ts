// The grid, its panes, and the tab being dragged, as this browser keeps them (issue #235). As Omnigent's
// GridSessionsContext (web/src/grid/GridSessionsContext.tsx): kept in localStorage, followed across this browser's
// windows, and not offered on a phone or with a coarse pointer. A pane is kept as Omnigent keeps a session's
// workspace tabs (web/src/lib/sessionWorkspaceState.ts).
import { useSyncExternalStore } from "react"

import { gridPanes, parseGrid } from "./grid-layout"
import type { Bench, Dragged } from "./moves"
import { parsePane, tabKey, type Pane } from "./pane"

const gridKey = "hypomnema.grid"
const panesKey = "hypomnema.panes"

// The type a dragged tab is carried as: only a tab, or a row of the sidebar, makes it.
export const tabDrag = "application/x-hypomnema-tab"

// The pane of a chat's own page.
export const soloId = (chat: string) => `chat-${chat}`

// A new pane of the grid gets an id no other has.
export const newPaneId = () => `pane-${crypto.randomUUID().slice(0, 8)}`

let panes: Record<string, Pane> = readPanes()
let bench: Bench | null = readBench()
let dragged: Dragged | null = null
const listeners = new Set<() => void>()

function readPanes(): Record<string, Pane> {
  try {
    const value: unknown = JSON.parse(window.localStorage.getItem(panesKey) ?? "{}")
    if (!value || typeof value !== "object") return {}

    const kept: Record<string, Pane> = {}
    for (const [id, pane] of Object.entries(value)) {
      const parsed = parsePane(pane)
      if (parsed !== null) kept[id] = parsed
    }

    return kept
  } catch {
    return {}
  }
}

// readBench is the grid kept, when each of its panes is kept too.
function readBench(): Bench | null {
  try {
    const layout = parseGrid(window.localStorage.getItem(gridKey))
    if (layout === null || gridPanes(layout).some((id) => panes[id] === undefined)) return null

    return { layout, panes }
  } catch {
    return null
  }
}

function tell() {
  for (const listener of listeners) listener()
}

function keep() {
  try {
    window.localStorage.setItem(panesKey, JSON.stringify(panes))
    if (bench === null) window.localStorage.removeItem(gridKey)
    else window.localStorage.setItem(gridKey, JSON.stringify(bench.layout))
  } catch {
    // Kept until the page closes.
  }
  tell()
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  const onStorage = (event: StorageEvent) => {
    if (event.key !== null && event.key !== gridKey && event.key !== panesKey) return
    panes = readPanes()
    bench = readBench()
    listener()
  }
  window.addEventListener("storage", onStorage)

  return () => {
    listeners.delete(listener)
    window.removeEventListener("storage", onStorage)
  }
}

// setBench keeps the grid and its panes, or forgets the grid with null. The panes of a grid that are not on it
// any more are forgotten; those of a chat's own page are kept.
export function setBench(next: Bench | null) {
  const kept = Object.fromEntries(Object.entries(panes).filter(([id]) => id.startsWith("chat-")))
  const on =
    next === null
      ? {}
      : Object.fromEntries(
          gridPanes(next.layout).flatMap((id) => {
            const pane = next.panes[id]

            return pane === undefined ? [] : [[id, pane] as const]
          }),
        )
  panes = { ...kept, ...on }
  bench = next === null ? null : { layout: next.layout, panes }
  keep()
}

// setPane keeps the pane of a chat's own page, or forgets it with null.
export function setPane(id: string, pane: Pane | null) {
  panes = { ...panes }
  if (pane === null) delete panes[id]
  else panes[id] = pane
  if (bench !== null) bench = { ...bench, panes }
  keep()
}

export function setDragged(next: Dragged | null) {
  const same =
    dragged !== null &&
    next !== null &&
    dragged.from === next.from &&
    tabKey(dragged.tab) === tabKey(next.tab)
  if (same || (dragged === null && next === null)) return
  dragged = next
  tell()
}

// useBench is the grid and its panes, or null when there is no grid.
export function useBench() {
  return useSyncExternalStore(subscribe, () => bench)
}

// usePane is a pane kept, or undefined.
export function usePane(id: string) {
  return useSyncExternalStore(subscribe, () => panes[id])
}

// useDragged is the tab being dragged, or null.
export function useDragged() {
  return useSyncExternalStore(subscribe, () => dragged)
}

const desktopQuery = "(min-width: 768px) and (pointer: fine)"

function subscribeDesktop(listener: () => void) {
  const media = window.matchMedia(desktopQuery)
  media.addEventListener("change", listener)

  return () => media.removeEventListener("change", listener)
}

// useGridOffered says whether this screen can show a grid: a desktop's, with a mouse.
export function useGridOffered() {
  return useSyncExternalStore(subscribeDesktop, () => window.matchMedia(desktopQuery).matches)
}
