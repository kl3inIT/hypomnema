// The tabs open in each chat's pane, as this browser keeps them (issue #235, step 2). As Omnigent remembers a
// session's workspace tabs (web/src/lib/sessionWorkspaceState.ts): in localStorage, a chat at a time.
import { useSyncExternalStore } from "react"

const storageKey = "hypomnema.pane-tabs"

// The tab that is the chat itself. It is always there and is not kept.
export const chatTab = "chat"

// What is open beside the chat: tabs of the shared browser, by their id, and which tab is in front.
export type PaneTabs = { browser: string[]; active: string }

const none: PaneTabs = { browser: [], active: chatTab }

let all: Record<string, PaneTabs> = read()
const listeners = new Set<() => void>()

function read(): Record<string, PaneTabs> {
  try {
    const value: unknown = JSON.parse(window.localStorage.getItem(storageKey) ?? "{}")
    if (!value || typeof value !== "object") return {}

    const kept: Record<string, PaneTabs> = {}
    for (const [chat, tabs] of Object.entries(value)) {
      const { browser, active } = (tabs ?? {}) as Record<string, unknown>
      if (!Array.isArray(browser) || typeof active !== "string") continue
      kept[chat] = { browser: browser.filter((id) => typeof id === "string"), active }
    }

    return kept
  } catch {
    return {}
  }
}

function subscribe(listener: () => void) {
  listeners.add(listener)

  return () => listeners.delete(listener)
}

function keep(chat: string, next: PaneTabs) {
  all = { ...all, [chat]: next }

  try {
    window.localStorage.setItem(storageKey, JSON.stringify(all))
  } catch {
    // Kept until the page closes.
  }
  for (const listener of listeners) listener()
}

// browserTabId is the id of a pane's tab that shows a tab of the shared browser.
export const browserTabId = (tab: string) => `browser:${tab}`

export function usePaneTabs(chat: string): PaneTabs {
  return useSyncExternalStore(subscribe, () => all[chat] ?? none)
}

// openBrowser opens a tab of the shared browser in a chat's pane, or goes to it when it is open already.
export function openBrowser(chat: string, tab: string) {
  const now = all[chat] ?? none
  keep(chat, {
    browser: now.browser.includes(tab) ? now.browser : [...now.browser, tab],
    active: browserTabId(tab),
  })
}

export function closeBrowser(chat: string, tab: string) {
  const now = all[chat] ?? none
  keep(chat, {
    browser: now.browser.filter((id) => id !== tab),
    active: now.active === browserTabId(tab) ? chatTab : now.active,
  })
}

export function showTab(chat: string, active: string) {
  keep(chat, { ...(all[chat] ?? none), active })
}
