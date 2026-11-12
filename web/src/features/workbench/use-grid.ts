// The grid as this browser keeps it, and the chat being dragged (issue #235). As Omnigent's GridSessionsContext
// (web/src/grid/GridSessionsContext.tsx): kept in localStorage, followed across this browser's windows, and not
// offered on a phone or with a coarse pointer.
import { useSyncExternalStore } from "react"

import { parseGrid, type GridLayout } from "./grid-layout"

const storageKey = "hypomnema.grid"

// The type a dragged chat is carried as: only a row of the sidebar or a pane's header makes it.
export const chatDrag = "application/x-hypomnema-chat"

let layout: GridLayout | null = read()
// A chat being dragged, and from where: a row of the sidebar, or a pane's header.
export type Dragged = { chat: string; from: "list" | "pane" }

let dragged: Dragged | null = null
const listeners = new Set<() => void>()

function read(): GridLayout | null {
  try {
    return parseGrid(window.localStorage.getItem(storageKey))
  } catch {
    return null
  }
}

function tell() {
  for (const listener of listeners) listener()
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  const onStorage = (event: StorageEvent) => {
    if (event.key !== null && event.key !== storageKey) return
    layout = read()
    listener()
  }
  window.addEventListener("storage", onStorage)

  return () => {
    listeners.delete(listener)
    window.removeEventListener("storage", onStorage)
  }
}

// setGrid keeps the grid, or forgets it with null. A browser that cannot keep it still shows it until the page
// closes.
export function setGrid(next: GridLayout | null) {
  layout = next

  try {
    if (next === null) window.localStorage.removeItem(storageKey)
    else window.localStorage.setItem(storageKey, JSON.stringify(next))
  } catch {
    // Kept until the page closes.
  }
  tell()
}

export function setDragged(next: Dragged | null) {
  if (dragged?.chat === next?.chat && dragged?.from === next?.from) return
  dragged = next
  tell()
}

export function useGrid() {
  return useSyncExternalStore(subscribe, () => layout)
}

// useDragged is the chat being dragged, or null.
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
