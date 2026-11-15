// Adapted from Hermes Agent's desktop sidebar (https://github.com/NousResearch/hermes-agent,
// apps/desktop/src/app/chat/sidebar/reorderable-list.tsx and chrome.tsx SidebarRowGrab), MIT License, Copyright (c)
// 2025 Nous Research; the licence is in web/THIRD_PARTY_NOTICES.md. Changed (issue #470): the sensors are onyx's
// (web/src/sections/sidebar/AppSidebar.tsx: a pointer that moves 8 pixels, the keyboard with
// sortableKeyboardCoordinates) and the list keeps to the vertical axis with dnd-kit's restrictToVerticalAxis, as
// t3code's sidebar does; the grabber is lucide's GripVertical in place of Hermes's codicon, and the handle is drawn
// with this sidebar's classes; the handle stops no click, being beside the row's link and not in it.
import {
  closestCenter,
  DndContext,
  type DragEndEvent,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
} from "@dnd-kit/core"
import { restrictToVerticalAxis } from "@dnd-kit/modifiers"
import {
  arrayMove,
  SortableContext,
  sortableKeyboardCoordinates,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable"
import { GripVertical } from "lucide-react"
import type { HTMLAttributes, ReactNode } from "react"

import { cn } from "@/lib/utils"

// Sidebar reordering is a strictly vertical list: this stops dnd-kit's auto-scroll from dragging the rail, or the
// window, sideways when the pointer nears an edge.
const reorderAutoScroll = { threshold: { x: 0, y: 0.2 } }

const modifiers = [restrictToVerticalAxis]

// ReorderableList is one self-contained reorderable list: it owns its DndContext, so a drag only collides with this
// list's own items. Pair each item with useSortableBindings(id) (use-sortable-bindings.ts); the list reports the new id order and the caller
// keeps it.
export function ReorderableList({
  children,
  ids,
  onReorder,
}: {
  children: ReactNode
  ids: string[]
  onReorder: (ids: string[]) => void
}) {
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 8 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )

  const handleDragEnd = ({ activatorEvent, active, over }: DragEndEvent) => {
    // dnd-kit only restores focus for keyboard drags; after a pointer drop the browser leaves :focus on the grab
    // handle, which keeps the grabber shown. Drop that focus so the row returns to rest once the pointer leaves.
    if (!(activatorEvent instanceof KeyboardEvent)) {
      ;(document.activeElement as HTMLElement | null)?.blur()
    }

    if (!over || active.id === over.id) return

    const from = ids.indexOf(String(active.id))
    const to = ids.indexOf(String(over.id))

    if (from >= 0 && to >= 0) onReorder(arrayMove(ids, from, to))
  }

  return (
    <DndContext
      autoScroll={reorderAutoScroll}
      collisionDetection={closestCenter}
      modifiers={modifiers}
      onDragEnd={handleDragEnd}
      sensors={sensors}
    >
      <SortableContext items={ids} strategy={verticalListSortingStrategy}>
        {children}
      </SortableContext>
    </DndContext>
  )
}

// RowGrab is the row's lead (its avatar) that turns into a grabber when pointed at or focused: Hermes's dot ↔ grabber
// swap.
export function RowGrab({
  ariaLabel,
  children,
  className,
  dragging = false,
  dragHandleProps,
}: {
  ariaLabel: string
  children: ReactNode
  className?: string
  dragging?: boolean
  dragHandleProps: HTMLAttributes<HTMLElement>
}) {
  return (
    <span
      {...dragHandleProps}
      aria-label={ariaLabel}
      data-reorder-handle
      className={cn(
        "group/handle flex cursor-grab touch-none items-center justify-center rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring active:cursor-grabbing",
        className,
      )}
    >
      <span
        className={cn(
          "relative flex transition-opacity group-hover/handle:opacity-0 group-focus-visible/handle:opacity-0",
          dragging && "opacity-0",
        )}
      >
        {children}
      </span>
      <GripVertical
        aria-hidden
        className={cn(
          "absolute size-4 text-muted-foreground opacity-0 transition-opacity group-hover/handle:opacity-100 group-focus-visible/handle:opacity-100",
          dragging && "text-foreground opacity-100",
        )}
      />
    </span>
  )
}
