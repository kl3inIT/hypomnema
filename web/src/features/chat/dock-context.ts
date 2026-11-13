import { createContext, useContext } from "react"

// DockContext lets a wake-up card's Run chip open the Run docked above the composer (issue #314) instead of
// leaving the chat. Outside a chat, or for a Run not docked in it, docked says no and the chip opens the Run's chat.
export type Dock = {
  docked: (runId: string) => boolean
  show: (runId: string) => void
}

export const DockContext = createContext<Dock>({ docked: () => false, show: () => {} })

export function useDock() {
  return useContext(DockContext)
}
