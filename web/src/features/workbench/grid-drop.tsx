import { useState, type DragEvent } from "react"

import { cn } from "@/lib/utils"

import { dropZone, type DropZone } from "./grid-layout"
import type { Dragged } from "./moves"
import { setDragged, tabDrag, useDragged } from "./use-grid"

const ghost: Record<DropZone, string> = {
  center: "inset-0",
  left: "inset-y-0 left-0 w-1/2",
  right: "inset-y-0 right-0 w-1/2",
  top: "inset-x-0 top-0 h-1/2",
  bottom: "inset-x-0 bottom-0 h-1/2",
}

// GridDrop lies over a pane's body while a tab is dragged and takes its drop: on a side the tab makes a new pane
// there, in the middle it joins this pane, as Orca's TabGroupDropOverlay (tab-group/TabGroupDropOverlay.tsx). The
// ghost shows where it lands. `takes` says whether a drop on a zone is taken; where it is not, nothing is shown.
// After Omnigent's GridDropOverlay (web/src/grid/GridDropTargets.tsx).
export function GridDrop({
  takes,
  onDrop,
}: {
  takes: (dragged: Dragged, zone: DropZone) => boolean
  onDrop: (dragged: Dragged, zone: DropZone) => void
}) {
  const dragged = useDragged()
  const [over, setOver] = useState<DropZone | null>(null)

  if (dragged === null) return null

  const at = (event: DragEvent<HTMLDivElement>) => {
    const zone = dropZone(event.currentTarget.getBoundingClientRect(), event.clientX, event.clientY)

    return takes(dragged, zone) ? zone : null
  }

  return (
    <div
      data-testid="grid-drop"
      className="absolute inset-0 z-30"
      onDragOver={(event) => {
        if (!event.dataTransfer.types.includes(tabDrag)) return
        const next = at(event)
        setOver(next)
        if (next !== null) event.preventDefault()
      }}
      onDragLeave={() => setOver(null)}
      onDrop={(event) => {
        event.preventDefault()
        const zone = at(event)
        setOver(null)
        setDragged(null)
        if (zone !== null) onDrop(dragged, zone)
      }}
    >
      {over !== null && (
        <div
          data-testid="grid-ghost"
          className={cn(
            "pointer-events-none absolute rounded-lg border-2 border-dashed border-primary bg-primary/20",
            ghost[over],
          )}
        />
      )}
    </div>
  )
}
