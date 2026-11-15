// Adapted from Hermes Agent's desktop sidebar (https://github.com/NousResearch/hermes-agent,
// apps/desktop/src/app/chat/sidebar/reorderable-list.tsx, useSortableBindings), MIT License, Copyright (c) 2025 Nous
// Research; the licence is in web/THIRD_PARTY_NOTICES.md. Changed (issue #470): an item can be turned off (a phone,
// which only shows the order).
import { useSortable } from "@dnd-kit/sortable"
import type { CSSProperties, HTMLAttributes } from "react"

export type SortableBindings = ReturnType<typeof useSortableBindings>

// useSortableBindings ties an item to its list. The handle's props (its role, tab stop and dnd-kit's keyboard and
// pointer activators) belong on the grabber only, so a focused control in the row (its menu button) never arms a
// drag on Space.
export function useSortableBindings(id: string, disabled = false) {
  const { attributes, isDragging, listeners, setNodeRef, transform, transition } = useSortable({
    id,
    disabled,
  })
  const dragHandleProps: HTMLAttributes<HTMLElement> = { ...attributes, ...listeners }

  const style: CSSProperties = {
    // Only ever translate on Y: no x, and no scale, which would morph a row with its chats mid-drag.
    transform: transform ? `translate3d(0px, ${transform.y}px, 0)` : undefined,
    transition: isDragging ? undefined : transition,
    willChange: isDragging ? "transform" : undefined,
  }

  return { dragging: isDragging, dragHandleProps, ref: setNodeRef, style, disabled }
}
