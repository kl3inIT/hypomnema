import { createContext, useContext } from "react"

// OpenBrowserTabContext lets a browser tab's card in a chat open that tab where the pane the chat is in opens it
// (issue #703): beside the chat, or in the pane on a phone. `chat` is the chat the card is in. Outside a pane it
// is null, and the card only names the tab.
export type OpenBrowserTab = { chat: string; open: (tab: string) => void }

export const OpenBrowserTabContext = createContext<OpenBrowserTab | null>(null)

export function useOpenBrowserTab() {
  return useContext(OpenBrowserTabContext)
}
