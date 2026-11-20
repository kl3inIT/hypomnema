import { frontChat, hasTab, openTab, paneOf, tabKey, type Pane, type PaneTab } from "./pane"
import { latestPane, setPane, soloId, usePane } from "./use-grid"

// soloPaneOf is the pane of a chat's page as kept in this browser, with the page's chat always among its tabs.
export function soloPaneOf(chat: string, stored: Pane | undefined): Pane {
  const home: PaneTab = { kind: "chat", chat }
  if (stored === undefined) return paneOf(home)

  return hasTab(stored, tabKey(home))
    ? stored
    : { tabs: [home, ...stored.tabs], active: stored.active }
}

// useSoloChat is the chat being viewed on a chat's page (issue #923): the chat in front of its pane; none while a tab
// that is not a chat is in front (a new chat's draft, a file, the browser, the phone), so that nothing acts on the
// chat behind it, nor on a page with no chat.
export function useSoloChat(chat: string | undefined): string | undefined {
  const stored = usePane(soloId(chat ?? ""))
  if (chat === undefined) return undefined

  return frontChat(soloPaneOf(chat, stored)) ?? undefined
}

// showSoloChat makes a chat the tab in front of its own page's pane before the page opens (issue #923): Back to main
// chat lands on the main chat, not on a tab its page kept in front.
export function showSoloChat(chat: string) {
  const stored = latestPane(soloId(chat))
  if (stored !== undefined)
    setPane(soloId(chat), openTab(soloPaneOf(chat, stored), { kind: "chat", chat }))
}
