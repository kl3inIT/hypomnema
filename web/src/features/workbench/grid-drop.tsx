import { useState, type DragEvent } from "react"

import { cn } from "@/lib/utils"

import { edgeOf, type SplitEdge } from "./grid-layout"
import { chatDrag, setDragged, useDragged, type Dragged } from "./use-grid"

const half: Record<SplitEdge, string> = {
  left: "inset-y-0 left-0 w-1/2",
  right: "inset-y-0 right-0 w-1/2",
  top: "inset-x-0 top-0 h-1/2",
  bottom: "inset-x-0 bottom-0 h-1/2",
}

// GridDrop lies over a pane while a chat is dragged and takes its drop. A chat from the sidebar lands on an edge
// and takes that half, shown as a dashed ghost; a pane's header dragged here changes places with this pane, and
// the ghost is the whole pane. `splits` says whether the dragged chat can take an edge; where it cannot, nothing
// is shown and the drop is not taken. After Omnigent's GridDropOverlay (web/src/grid/GridDropTargets.tsx).
export function GridDrop({
  chat,
  splits,
  onSplit,
  onSwap,
}: {
  chat: string
  splits: (dragged: string, edge: SplitEdge) => boolean
  onSplit: (dragged: string, edge: SplitEdge) => void
  onSwap?: (dragged: string) => void
}) {
  const dragged = useDragged()
  const [over, setOver] = useState<SplitEdge | "swap" | null>(null)

  if (dragged === null || dragged.chat === chat) return null

  const swap = dragged.from === "pane" && onSwap !== undefined
  const at = (event: DragEvent<HTMLDivElement>, held: Dragged) => {
    if (swap) return "swap" as const

    const edge = edgeOf(event.currentTarget.getBoundingClientRect(), event.clientX, event.clientY)

    return splits(held.chat, edge) ? edge : null
  }

  return (
    <div
      data-testid="grid-drop"
      className="absolute inset-0 z-30"
      onDragOver={(event) => {
        if (!event.dataTransfer.types.includes(chatDrag)) return
        const next = at(event, dragged)
        setOver(next)
        if (next !== null) event.preventDefault()
      }}
      onDragLeave={() => setOver(null)}
      onDrop={(event) => {
        event.preventDefault()
        const where = at(event, dragged)
        setOver(null)
        setDragged(null)
        if (where === "swap") onSwap?.(dragged.chat)
        else if (where !== null) onSplit(dragged.chat, where)
      }}
    >
      {over !== null && (
        <div
          data-testid="grid-ghost"
          className={cn(
            "pointer-events-none absolute rounded-lg border-2 border-dashed border-primary bg-primary/20",
            over === "swap" ? "inset-0" : half[over],
          )}
        />
      )}
    </div>
  )
}
