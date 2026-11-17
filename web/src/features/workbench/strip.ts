import type { BrowserTab } from "@/api/generated/types.gen"
import { stripTabs } from "@/features/browser/browser-state"

import { hasTab, paneChats, replaceTab, type Pane, type PaneTab } from "./pane"

// paneStrip is what the strip over a pane's browser tab lists (issue #703): `stripTabs` of the pane's chats, of the
// chats its browser tabs were shown for, and of the chats whose tab is open in it.
export function paneStrip(pane: Pane, tabs: BrowserTab[] | undefined): BrowserTab[] {
  const shown = pane.tabs.flatMap((tab) => (tab.kind === "browser" ? [tab] : []))

  return stripTabs(
    tabs,
    [...paneChats(pane), ...shown.flatMap((tab) => (tab.chat === undefined ? [] : [tab.chat]))],
    shown.map((tab) => tab.tab),
  )
}

// browserTab is a pane's tab of a browser tab, with the chat whose tabs its strip lists: `chat`, or the browser
// tab's own chat.
export function browserTab(tab: string, tabs: BrowserTab[] | undefined, chat?: string): PaneTab {
  const of = chat ?? tabs?.find((t) => t.id === tab)?.chat_id

  return of === undefined ? { kind: "browser", tab } : { kind: "browser", tab, chat: of }
}

// pickFromStrip is the pane once a tab of the strip over its browser tab `from` was clicked: a tab open in the
// pane comes in front; another takes the place of the one shown, as OpenClaw's panel shows one tab at a time, and
// keeps the chat whose tabs the strip lists, so that the strip still lists them when it shows the owner's tab.
export function pickFromStrip(
  pane: Pane,
  from: string,
  tab: string,
  tabs: BrowserTab[] | undefined,
): Pane {
  const key = `browser:${tab}`
  if (hasTab(pane, key)) return { ...pane, active: key }

  const shown = pane.tabs.find((each) => each.kind === "browser" && `browser:${each.tab}` === from)
  const chat =
    shown?.kind === "browser"
      ? (shown.chat ?? tabs?.find((t) => t.id === shown.tab)?.chat_id)
      : undefined

  return replaceTab(pane, from, browserTab(tab, tabs, chat))
}
