import { useState } from "react"

import { ArtifactFullScreen } from "./artifact-full-screen"
import { ArtifactPanel } from "./artifact-panel"

// ArtifactSide is an artifact in the panel beside the chat, where the Files panel opens (issue #402, row 5), as
// Claude's artifacts, ChatGPT's canvas and assistant-ui's with-artifacts split the view: the chat stays usable, so
// the owner asks for a change while looking. It is wider than the Files panel, half the pane, since a page is laid
// out for a screen. A phone has no room beside the chat: there the card opens the artifact over the whole screen.
export function ArtifactSide({ id, onClose }: { id: string; onClose: () => void }) {
  const [rev, setRev] = useState<number | undefined>()
  const [full, setFull] = useState(false)

  return (
    <aside
      aria-label="Artifact"
      className="hidden min-h-0 w-1/2 max-w-4xl min-w-80 shrink-0 flex-col border-l bg-sidebar md:flex"
    >
      <ArtifactPanel
        id={id}
        rev={rev}
        onRev={setRev}
        where="side"
        onClose={onClose}
        onFullScreen={() => setFull(true)}
      />
      {full && (
        <ArtifactFullScreen
          id={id}
          rev={rev}
          onRev={setRev}
          onExit={() => setFull(false)}
          onClose={() => {
            setFull(false)
            onClose()
          }}
        />
      )}
    </aside>
  )
}
