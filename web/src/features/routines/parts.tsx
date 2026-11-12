import { TriangleAlert } from "lucide-react"

import type { Routine } from "@/api/generated/types.gen"
import { cn } from "@/lib/utils"

import { fixWords, type Status } from "./routines-state"

// StatusMark and FixButton are adapted from OpenMausBot (Apache License 2.0, Copyright 2026 Milind Soni and
// OpenMausBot contributors, https://github.com/milind-soni/OpenMausBot; src/components/automations/parts.tsx):
// a coloured dot for On and Paused, a warning sign for Needs you, and the one button that mends what Needs you
// names. Changed: drawn with the app's status tones and Button; a Routine is never done, so there is no check.

// The mark sits in a box of one width, so the names beside a dot and beside a warning sign line up.
export function StatusMark({ status, className }: { status: Status; className?: string }) {
  return (
    <span
      aria-hidden
      className={cn("flex h-4 w-4 shrink-0 items-center justify-center", className)}
    >
      {status.state === "needs-you" ? (
        <TriangleAlert className="size-3.75 text-needs-you" />
      ) : (
        <span
          className={cn(
            "size-2.5 rounded-full",
            status.state === "working" ? "bg-done" : "bg-queued/60",
          )}
        />
      )}
    </span>
  )
}

// FixButton: "Answer" opens where the waiting run asks; "Run now" tries it again.
export function FixButton({
  routine,
  status,
  busy,
  onAnswer,
  onRun,
  className,
}: {
  routine: Routine
  status: Status
  busy?: boolean
  onAnswer: (routine: Routine) => void
  onRun: (routine: Routine) => void
  className?: string
}) {
  if (status.state !== "needs-you" || !status.fix) return null

  const fix = status.fix

  return (
    <button
      type="button"
      disabled={busy}
      onClick={() => (fix === "answer" ? onAnswer(routine) : onRun(routine))}
      className={cn(
        "h-6 shrink-0 rounded-md border border-needs-you/40 bg-needs-you-soft px-2 text-xs font-medium text-needs-you hover:bg-needs-you/20 disabled:opacity-50",
        className,
      )}
    >
      {fixWords[fix]}
    </button>
  )
}
