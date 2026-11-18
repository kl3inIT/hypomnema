import { createContext, useContext } from "react"

// OpenTakeContext lets a take's card in a chat open the take as a tab of the pane the chat is in (issue #817), as
// a browser tab's card opens its tab (../browser/open-tab-context.ts). Outside a pane it is null, and the card
// only says the take's state.
export const OpenTakeContext = createContext<((take: string) => void) | null>(null)

export function useOpenTake() {
  return useContext(OpenTakeContext)
}
