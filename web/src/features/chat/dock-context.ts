import { createContext, useContext } from "react"

import type { TaskDetail } from "@/api/generated/types.gen"

// DockContext lets a wake-up card's Run chip open the Run docked above the composer (issue #314) instead of
// leaving the chat. Outside a chat, or for a Run not docked in it, docked says no and the chip opens the Run's chat.
// task is a Task as the dock last read it with its Run, for a wake-up card's result (issue #687).
export type Dock = {
  docked: (runId: string) => boolean
  show: (runId: string) => void
  task: (taskId: string) => TaskDetail | undefined
}

export const DockContext = createContext<Dock>({
  docked: () => false,
  show: () => {},
  task: () => undefined,
})

export function useDock() {
  return useContext(DockContext)
}
