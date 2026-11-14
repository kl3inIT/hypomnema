import { cn } from "cn"
import { Minus, Plus } from "lucide-react"
import { useState, type ReactNode } from "react"

import { Button } from "@/components/ui/button"

// Copied from MemoryOS (web/src/features/preview/preview-toolbar.tsx), with this app's buttons and colours.

// PreviewToolbar is the reader's controls, floating over the document rather than framing it, so the page keeps
// the width. Every format shares the shell; a control a format cannot support is simply not passed.
export function PreviewToolbar({
  children,
  className,
}: {
  children: ReactNode
  className?: string
}) {
  return (
    <div
      role="toolbar"
      aria-label="Document controls"
      data-slot="preview-toolbar"
      className={cn(
        "pointer-events-auto flex items-center gap-1 rounded-xl border bg-popover/95 p-1 text-popover-foreground shadow-lg backdrop-blur-sm",
        className,
      )}
    >
      {children}
    </div>
  )
}

// ToolbarGroup groups controls inside the toolbar, with a hairline between groups.
export function ToolbarGroup({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-center gap-1 border-l pl-1 first:border-0 first:pl-0">
      {children}
    </div>
  )
}

export function ZoomControl({
  label,
  onOut,
  onIn,
  outDisabled,
  inDisabled,
}: {
  // The current level as the reader reads it, such as "Fit" or "150%".
  label: string
  onOut: () => void
  onIn: () => void
  outDisabled?: boolean
  inDisabled?: boolean
}) {
  return (
    <ToolbarGroup>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label="Zoom out"
        disabled={outDisabled}
        onClick={onOut}
      >
        <Minus />
      </Button>
      <span
        aria-live="polite"
        className="min-w-12 text-center text-xs font-medium text-muted-foreground tabular-nums"
      >
        {label}
      </span>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label="Zoom in"
        disabled={inDisabled}
        onClick={onIn}
      >
        <Plus />
      </Button>
    </ToolbarGroup>
  )
}

// PageField is the page the reader is on, which they may also type to jump. It follows the document while they
// scroll and only reports a page when what they typed is one.
export function PageField({
  page,
  total,
  onPage,
}: {
  page: number
  total: number
  onPage: (page: number) => void
}) {
  const [typed, setTyped] = useState(String(page))
  // Scrolling moves the reader between pages, so the field follows the document rather than the keystroke.
  const [shown, setShown] = useState(page)
  if (page !== shown) {
    setShown(page)
    setTyped(String(page))
  }
  const commit = () => {
    const wanted = Number.parseInt(typed, 10)
    if (!Number.isFinite(wanted)) {
      setTyped(String(page))
      return
    }
    onPage(Math.min(Math.max(wanted, 1), total))
  }

  return (
    <ToolbarGroup>
      <input
        type="number"
        inputMode="numeric"
        min={1}
        max={total}
        value={typed}
        aria-label="Page number"
        className="h-7 w-11 rounded-md bg-muted px-1.5 text-center text-xs font-medium text-foreground tabular-nums outline-none focus-visible:ring-3 focus-visible:ring-ring/50 [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none"
        onChange={(event) => setTyped(event.target.value)}
        onBlur={commit}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault()
            commit()
          }
        }}
      />
      <span className="pr-1 text-xs text-muted-foreground tabular-nums">/ {total}</span>
    </ToolbarGroup>
  )
}
