import { useEffect } from "react"
import { createPortal } from "react-dom"

import { ArtifactPanel } from "./artifact-panel"

// ArtifactFullScreen is an artifact over the whole window (issue #402): from the panel's ⤢ on a desktop, where Esc
// and ⤡ go back to the panel, and from the card on a phone, as OpenMausBot's phone views show a page in the same
// frame filling the screen. It is its own design, not a dialog: nothing of the app is under it to come back to but
// the chat, which stays as it was.
export function ArtifactFullScreen({
  id,
  rev,
  onRev,
  onClose,
  onExit,
}: {
  id: string
  rev?: number
  onRev: (rev: number | undefined) => void
  onClose: () => void
  // onExit goes back to the panel beside the chat; absent on a phone, which has none.
  onExit?: () => void
}) {
  const back = onExit ?? onClose

  // Esc goes back, unless the page in the frame has the keyboard: its keys stay its own.
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") back()
    }
    window.addEventListener("keydown", onKey)

    return () => window.removeEventListener("keydown", onKey)
  }, [back])

  return createPortal(
    <dialog
      open
      aria-label="Artifact, full screen"
      className="fixed inset-0 z-50 m-0 flex size-full max-h-none max-w-none flex-col border-0 bg-background p-0 pt-[env(safe-area-inset-top)] pb-[env(safe-area-inset-bottom)] text-foreground"
    >
      <ArtifactPanel
        id={id}
        rev={rev}
        onRev={onRev}
        where="full"
        onFullScreen={onExit}
        onClose={onClose}
      />
    </dialog>,
    document.body,
  )
}
