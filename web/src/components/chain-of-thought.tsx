import { ChevronDown, Dot, type LucideIcon } from "lucide-react"
import type { ComponentProps, ReactNode } from "react"

import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import { cn } from "@/lib/utils"

// ChainOfThought is a folded list of steps on a line, adapted from AI Elements' chain-of-thought.tsx
// (https://github.com/vercel/ai-elements, packages/elements/src/chain-of-thought.tsx, Apache License 2.0,
// Copyright 2023 Vercel, Inc.) for an Inbox item's grouped steps, "Hypo did 5 steps" (issue #1143). Changed: one
// Collapsible of the app's (base-ui) holds the header and the steps, in place of a context and two Radix
// Collapsibles kept in step by useControllableState; the header's brain icon and words are the caller's; the
// search results and image parts are left out; the line under a step's icon reaches the next step.
export function ChainOfThought({
  defaultOpen = false,
  className,
  children,
}: {
  defaultOpen?: boolean
  className?: string
  children: ReactNode
}) {
  return (
    <div className={cn("w-full", className)}>
      <Collapsible defaultOpen={defaultOpen}>{children}</Collapsible>
    </div>
  )
}

// The header's label names it for a screen reader, as its words may hold more than text.
export function ChainOfThoughtHeader({ label, children }: { label: string; children: ReactNode }) {
  return (
    <CollapsibleTrigger
      render={
        <button
          type="button"
          aria-label={label}
          className="flex w-full cursor-pointer items-center gap-2 rounded-sm text-start text-sm text-muted-foreground transition-colors outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
        />
      }
    >
      <span className="min-w-0 flex-1">{children}</span>
      <ChevronDown className="size-4 shrink-0 transition-transform [[data-panel-open]_&]:rotate-180" />
    </CollapsibleTrigger>
  )
}

export function ChainOfThoughtContent({ children }: { children: ReactNode }) {
  return (
    <CollapsibleContent>
      <div className="mt-2 flex flex-col gap-3">{children}</div>
    </CollapsibleContent>
  )
}
const stepStatusStyles = {
  active: "text-foreground",
  complete: "text-muted-foreground",
  pending: "text-muted-foreground/50",
}

export function ChainOfThoughtStep({
  className,
  icon: Icon = Dot,
  label,
  description,
  status = "complete",
  children,
  ...props
}: ComponentProps<"div"> & {
  icon?: LucideIcon
  label: ReactNode
  description?: ReactNode
  status?: keyof typeof stepStatusStyles
}) {
  return (
    <div
      className={cn("group/step flex gap-2 text-sm", stepStatusStyles[status], className)}
      {...props}
    >
      <div className="relative mt-0.5">
        <Icon className="size-4" aria-hidden />
        <div className="absolute top-5 -bottom-3 left-1/2 -mx-px w-px bg-border group-last/step:hidden" />
      </div>
      <div className="min-w-0 flex-1 space-y-2 overflow-hidden">
        <div>{label}</div>
        {description && <div className="text-xs text-muted-foreground">{description}</div>}
        {children}
      </div>
    </div>
  )
}
