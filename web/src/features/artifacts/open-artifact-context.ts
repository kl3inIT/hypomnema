import { createContext, useContext } from "react"

// OpenArtifactContext lets an artifact's card open it in the panel beside the chat of the pane the chat is in
// (issue #402, row 5), as OpenFileContext opens an attached file there. Outside a pane it is null, and the card
// opens the artifact over the whole screen.
export type OpenArtifact = (id: string) => void

export const OpenArtifactContext = createContext<OpenArtifact | null>(null)

export function useOpenArtifact() {
  return useContext(OpenArtifactContext)
}
