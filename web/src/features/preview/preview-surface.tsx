import { cn } from "cn"
import type { ReactNode, RefObject } from "react"

import { Skeleton } from "@/components/ui/skeleton"

// Copied from MemoryOS (web/src/features/preview/preview-surface.tsx), on this app's colours.

// PreviewCanvas is the reading canvas: a sunken backdrop the document sits on, as a document reader shows a page.
export function PreviewCanvas({
  children,
  className,
  ref,
}: {
  children: ReactNode
  className?: string
  ref?: RefObject<HTMLDivElement | null>
}) {
  return (
    <div
      ref={ref}
      data-slot="preview-canvas"
      className={cn(
        "min-h-0 flex-1 [scrollbar-gutter:stable] overflow-auto overscroll-contain bg-muted/30 p-4 sm:px-5",
        className,
      )}
    >
      {children}
    </div>
  )
}

// PreviewSkeleton is pages in the shape they will take, so the canvas keeps its size while the document loads.
export function PreviewSkeleton({ pages = 2, width }: { pages?: number; width?: number }) {
  return (
    <output aria-label="Opening the file…" className="flex flex-col items-center gap-5">
      {Array.from({ length: pages }, (_, index) => (
        <div
          key={index}
          // US Letter portrait, the proportions an unmeasured page borrows everywhere else in the reader.
          className="flex w-full max-w-3xl"
          style={{ aspectRatio: "612 / 792", ...(width ? { width, flex: "none" } : {}) }}
        >
          <Skeleton style={{ flex: 1 }} />
        </div>
      ))}
    </output>
  )
}

// CutNote says a view shows only the beginning of its file, beside the download that holds the whole (issue #422,
// row 5: MemoryOS's limits, its note with the download beside it).
export function CutNote({
  children,
  download,
  filename,
  className,
}: {
  children: ReactNode
  download: string
  filename: string
  className?: string
}) {
  return (
    <p role="note" className={cn("text-xs text-muted-foreground", className)}>
      {children}{" "}
      <a href={download} download={filename} className="font-medium text-foreground underline">
        Download the whole file
      </a>
    </p>
  )
}
