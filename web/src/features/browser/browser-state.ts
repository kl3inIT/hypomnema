// What the Browser page works out from the control plane's list of tabs.
import { useQuery } from "@tanstack/react-query"

import "@/api/client"
import { getBrowserOptions } from "@/api/generated/@tanstack/react-query.gen"
import type { BrowserTab } from "@/api/generated/types.gen"

// The owner's own tab, where a sign-in is made. Watching it makes it, so it is always offered.
export const ownerTab = "owner"

// whileOpen is how often the tabs are read again: an agent opens and leaves a tab without an event.
const whileOpen = 5000

export function useBrowser() {
  return useQuery({
    ...getBrowserOptions(),
    // A control plane with no browser is not asked again and again.
    refetchInterval: (query) => (query.state.status === "error" ? false : whileOpen),
    retry: false,
  })
}

// whose says whose a tab is, in a word and in a line. A Bot's tab in a chat is called its Bot's (issue #557), by the
// name botName gives its id.
export function whose(
  tab: BrowserTab,
  botName: (id: string) => string = (id) => id,
): { mark: string; line: string } {
  switch (tab.kind) {
    case "owner":
      return { mark: "you", line: "Your own tab. Sign in here: every agent shares the sign-in." }
    case "coordinator": {
      if (!tab.bot_id) return { mark: "pi", line: "The coordinator's tab." }
      const bot = botName(tab.bot_id)

      return { mark: bot, line: `${bot}'s tab, in this chat.` }
    }
    case "worker":
      return {
        mark: "worker",
        line: tab.task_id ? `A worker's tab, for task ${tab.task_id}.` : "A worker's tab.",
      }
    default:
      return { mark: "token", line: "A tab opened with the owner's token." }
  }
}

// heldName is how a tab the owner holds is named in a line: his own tab as his, another by what it is called.
function heldName(tab: BrowserTab) {
  return tab.kind === "owner" ? "your own tab" : `“${tabName(tab)}”`
}

function joined(names: string[]) {
  return names.length < 2 ? names.join("") : `${names.slice(0, -1).join(", ")} and ${names.at(-1)}`
}

// heldLine says which tabs the owner holds and what that pauses, or null while he holds none (issue #1113). Taking
// control holds only the tab taken, as T3 Code's control is one tab's (apps/server/src/preview/SessionControl.ts):
// only the agent of a tab held waits, and OpenMausBot names whose tools are paused ("{name}'s browser tools are
// paused while you use it", src/locales/en.json, browser.control.yoursHint).
export function heldLine(tabs: BrowserTab[] | undefined, held: boolean): string | null {
  const taken = (tabs ?? []).filter((t) => t.held)
  if (taken.length === 0) return held ? "You hold a tab of the browser." : null

  const agents = taken.filter((t) => t.kind !== "owner").length
  const paused =
    agents === 0
      ? "The agents go on in their own tabs."
      : agents === 1
        ? "Its agent's browser tools are paused until you hand it back."
        : "Their agents' browser tools are paused until you hand them back."

  return `You hold ${joined(taken.map(heldName))}. ${paused}`
}

// elsewhereLine says, in the view of a tab the owner does not hold, which other tabs he holds, or null when none:
// this tab's agent goes on (issue #1113), and "Hand back" is in those tabs' views.
export function elsewhereLine(tabs: BrowserTab[] | undefined, tab: string): string | null {
  const taken = (tabs ?? []).filter((t) => t.held && t.id !== tab)
  if (taken.length === 0) return null

  return `You hold ${joined(taken.map(heldName))}. This tab's agent goes on.`
}

// chatTabs are the tabs of the chats in a pane, whichever agent opened them (issue #557): those whose chat is one
// of them. The owner's own tab is no chat's.
export function chatTabs(tabs: BrowserTab[] | undefined, chats: ReadonlySet<string>) {
  return (tabs ?? []).filter((t) => t.chat_id !== undefined && chats.has(t.chat_id))
}

// tabName is what a tab is called: its page's title, as in any browser (Orca's BrowserTab), or what it is while
// it has none.
export function tabName(tab: BrowserTab) {
  if (tab.title && tab.title !== tab.url && tab.title !== "about:blank") return tab.title
  if (tab.kind === "owner") return "My tab"

  return tab.url && tab.url !== "about:blank" ? tab.url : "New tab"
}

// freshTabs are the tabs of these chats that are new since the list seen before (issue #703): those not in it.
// Nothing is new while no list was seen yet, so that a page opened, or reloaded, leaves the tabs already there as
// they are. A tab the owner closed in the pane stays in the list and is not new again; a tab its agent closed and
// opened again is. As OpenClaw's browser panel follows the newest tab of its session once
// (ui/src/components/browser/browser-panel-presentation.ts, followPreferredTab).
export function freshTabs(
  seen: ReadonlySet<string> | null,
  tabs: BrowserTab[] | undefined,
  chats: ReadonlySet<string>,
): BrowserTab[] {
  if (seen === null) return []

  return chatTabs(tabs, chats).filter((t) => !seen.has(t.id))
}

// stripTabs are the tabs a browser pane's strip lists (issue #703), as OpenClaw's PanelTabStrip lists the tabs of
// its panel (ui/src/components/browser/browser-panel-render.tsx): the owner's own tab, always, and then the tabs of
// the pane's chats and of the chats whose tab is open in the pane, so that a pane beside a chat lists its tabs.
// `open` are the browser tabs open in the pane.
export function stripTabs(
  tabs: BrowserTab[] | undefined,
  chats: readonly string[],
  open: readonly string[],
): BrowserTab[] {
  const list = tabs ?? []
  const of = new Set(chats)
  for (const id of open) {
    const chat = list.find((t) => t.id === id)?.chat_id
    if (chat !== undefined) of.add(chat)
  }

  const owner = list.find((t) => t.id === ownerTab) ?? {
    id: ownerTab,
    kind: "owner" as const,
    url: "",
    title: "",
    views: 0,
    held: false,
  }

  return [owner, ...chatTabs(list, of)]
}

// browserOpen is the tool a Bot opens a page with; its call shows as a card in the chat (issue #703).
export const browserOpen = "agent_browser_open"

// browserOpenUrl is the address a call of agent_browser_open was given, or empty.
export function browserOpenUrl(args?: Record<string, unknown>): string {
  const url = args?.url

  return typeof url === "string" ? url.trim() : ""
}

// chatBotTab is the tab a chat's Bot opens pages in (issue #557): the coordinator's tab of that chat.
export function chatBotTab(tabs: BrowserTab[] | undefined, chat: string): BrowserTab | undefined {
  return (tabs ?? []).find((t) => t.kind === "coordinator" && t.chat_id === chat)
}
