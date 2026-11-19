import { frontChat, hasTab, paneOf, tabKey, type Pane, type PaneTab } from "./pane"
import { soloId, usePane } from "./use-grid"

// soloPaneOf is the pane of a chat's page as kept in this browser, with the page's chat always among its tabs.
export function soloPaneOf(chat: string, stored: Pane | undefined): Pane {
  const home: PaneTab = { kind: "chat", chat }
  if (stored === undefined) return paneOf(home)

  return hasTab(stored, tabKey(home))
    ? stored
    : { tabs: [home, ...stored.tabs], active: stored.active }
}

// useSoloChat is the chat being viewed on a chat's page (issue #923): the chat in front of its pane, or the page's
// chat while a tab that is not a chat is in front; none on a page with no chat.
export function useSoloChat(chat: string | undefined): string | undefined {
  const stored = usePane(soloId(chat ?? ""))
  if (chat === undefined) return undefined

  return frontChat(soloPaneOf(chat, stored)) ?? chat
}
