import { createContext, useContext } from "react"

// OpenFileContext lets a file attached to a message open in the pane the chat is in (issue #343): as a tab of the
// pane, with the Files panel beside it, as a file opened from the panel does. Outside a pane it is null, and the
// file's chip only names it.
export type OpenFile = (workspace: string, path: string) => void

export const OpenFileContext = createContext<OpenFile | null>(null)

export function useOpenFile() {
  return useContext(OpenFileContext)
}
