import { useState, type ReactNode } from "react"

import { cn } from "@/lib/utils"

import type { Attachments } from "./attachments"

// AttachDrop takes files dropped anywhere on it into the message being written (issue #343), as t3code and Orca's
// pane take a drop (NativeChatPaneFileDropSurface.tsx): an open chat, the New chat page and a pane's new chat tab
// (issue #357) alike.
export function AttachDrop({
  attachments,
  className,
  children,
}: {
  attachments: Attachments
  className?: string
  children: ReactNode
}) {
  // dragging is true while files are held over it.
  const [dragging, setDragging] = useState(false)
  const holdsFiles = (event: React.DragEvent) => event.dataTransfer.types.includes("Files")

  return (
    <div
      className={cn("relative", className)}
      onDragOver={(event) => {
        if (!holdsFiles(event)) return
        event.preventDefault()
        event.dataTransfer.dropEffect = "copy"
        setDragging(true)
      }}
      onDragLeave={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDragging(false)
      }}
      onDrop={(event) => {
        if (!holdsFiles(event)) return
        event.preventDefault()
        setDragging(false)
        attachments.attach([...event.dataTransfer.files])
      }}
    >
      {dragging && (
        <div
          aria-hidden
          className="pointer-events-none absolute inset-2 z-30 flex items-center justify-center rounded-2xl border-2 border-dashed border-primary/60 bg-background/80 text-sm text-foreground"
        >
          Drop files to attach them to your message
        </div>
      )}
      {children}
    </div>
  )
}
