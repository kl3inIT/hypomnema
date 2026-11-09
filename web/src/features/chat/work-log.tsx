// Adapted from t3code apps/web/src/components/chat/WorkLog.tsx (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code). Rows of an agent's work: an icon, a one-line label, a trailing slot, and
// details that open under them.
import {
  createContext,
  use,
  useLayoutEffect,
  useRef,
  type ComponentProps,
  type ReactNode,
} from "react"

import { cn } from "@/lib/utils"

const GroupedRows = createContext(false)

export function WorkLogBlock({
  layout = "standalone",
  children,
}: {
  layout?: "standalone" | "group-header" | "group-content"
  children: ReactNode
}) {
  return (
    <div
      className={layout === "group-header" ? "pb-0" : layout === "group-content" ? "pb-1" : "pb-2"}
    >
      {children}
    </div>
  )
}

export function WorkLogList({ children }: { children: ReactNode }) {
  return (
    <GroupedRows value>
      <div className="flex min-w-0 flex-col">{children}</div>
    </GroupedRows>
  )
}

type RowContent = {
  icon?: ReactNode
  label: ReactNode
  trailing?: ReactNode
  wrapLabel?: boolean
}

function WorkLogLine({ icon, label, trailing, wrapLabel }: RowContent) {
  return (
    <div
      className={cn(
        "flex min-h-6 min-w-0 gap-1.5 text-sm leading-relaxed select-none",
        wrapLabel ? "items-start" : "items-center",
      )}
    >
      <div className="flex min-w-0 flex-1 items-baseline gap-1.5">
        {icon ? (
          <span className="relative w-6 shrink-0 leading-0">
            <span className="inline-block size-6 overflow-hidden align-[calc(0.5cap_-_0.75rem)]">
              <span className="flex size-full items-center justify-center">{icon}</span>
            </span>
          </span>
        ) : null}
        <div
          className={cn(
            "min-w-0 flex-1 text-secondary-label",
            !wrapLabel && "truncate [&_*]:whitespace-nowrap",
          )}
        >
          {label}
        </div>
      </div>
      {trailing}
    </div>
  )
}

const interactionClassName =
  "cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/70"

function useRowClassName(interactive: boolean) {
  const grouped = use(GroupedRows)

  return cn(
    "group/timeline-row relative w-full min-w-0 rounded-md px-0.5 text-left transition-colors",
    grouped ? "py-0" : "py-0.5",
    interactive && interactionClassName,
  )
}

export function WorkLogButton({
  icon,
  label,
  trailing,
  wrapLabel = false,
  ...buttonProps
}: RowContent & Omit<ComponentProps<"button">, "className" | "style" | "children">) {
  const className = useRowClassName(true)

  return (
    <button {...buttonProps} type="button" className={className}>
      <WorkLogLine icon={icon} label={label} trailing={trailing} wrapLabel={wrapLabel} />
    </button>
  )
}

// WorkLogRow is a row that may open: with onToggle, its line is a button and its children (the details) sit
// under it, outside the button, so the output can be selected.
export function WorkLogRow({
  icon,
  label,
  trailing,
  wrapLabel = false,
  expanded,
  onToggle,
  children,
}: RowContent & { expanded?: boolean; onToggle?: () => void; children?: ReactNode }) {
  const className = useRowClassName(false)
  const line = <WorkLogLine icon={icon} label={label} trailing={trailing} wrapLabel={wrapLabel} />

  return (
    <div className={className}>
      {onToggle ? (
        <button
          type="button"
          aria-expanded={expanded}
          onClick={onToggle}
          className={cn("w-full rounded-md text-left", interactionClassName)}
        >
          {line}
        </button>
      ) : (
        line
      )}
      {children}
    </div>
  )
}

const liveEdgePx = 4

export function WorkLogDetails({
  children,
  kind = "text",
  followLive = false,
}: {
  children: ReactNode
  kind?: "text" | "panel"
  followLive?: boolean
}) {
  const scrollerRef = useRef<HTMLDivElement>(null)
  const atLiveEdgeRef = useRef(true)

  // Keeps output that is still arriving scrolled to its end while the reader is there.
  useLayoutEffect(() => {
    const scroller = scrollerRef.current
    if (followLive && atLiveEdgeRef.current && scroller) scroller.scrollTop = scroller.scrollHeight
  })

  return (
    <div
      ref={scrollerRef}
      onScroll={(event) => {
        const { scrollHeight, scrollTop, clientHeight } = event.currentTarget
        atLiveEdgeRef.current = scrollHeight - scrollTop - clientHeight <= liveEdgePx
      }}
      className={cn(
        "cursor-auto",
        kind === "text"
          ? "ms-7 flex max-h-96 flex-col gap-3 overflow-auto px-0.5 py-1 select-text"
          : "mt-0.5 mb-1.5 pb-2",
      )}
    >
      {children}
    </div>
  )
}
