import { Clapperboard, ExternalLink } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"

import { useOpenTake } from "./open-take-context"
import { clock, finalStates, stateLine, useTake } from "./take-state"

// TakeCard is a take an agent filmed, in the chat under the call that started it (issue #817, row 13), in the
// shape of a browser tab's card (../browser/browser-tab-card.tsx, after OpenClaw's tab card): the take's title, its
// state as it goes, from filming to ready, with its length and steps, and "Open" once it can be watched, which opens
// it as a tab of the pane. A take that failed says why; a take deleted says so.
export function TakeCard({ take }: { take: string }) {
  const read = useTake(take)
  const open = useOpenTake()
  const t = read.data
  const ready = t?.state === "available"
  const working = t !== undefined && !finalStates.includes(t.state)
  const failed = t?.state === "failed"
  const title = t?.title ?? "A take of the phone"

  return (
    <div className="pb-4">
      <section
        aria-label={`Take: ${title}`}
        className="flex w-full max-w-md items-center gap-3 rounded-xl border bg-card p-2 ps-3 shadow-xs"
      >
        <span
          className={cn(
            "flex size-8 shrink-0 items-center justify-center rounded-lg border bg-muted",
            failed && "border-destructive/40 text-destructive",
          )}
        >
          <Clapperboard className="size-4" aria-hidden />
        </span>
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="truncate text-sm font-medium text-foreground">{title}</span>
          <span
            className={cn(
              "flex items-center gap-1.5 truncate text-xs text-muted-foreground",
              failed && "text-destructive",
            )}
          >
            {working && <Spinner />}
            {t
              ? `${stateLine(t)} · ${clock(t.duration_ms)} · ${t.steps.length} ${t.steps.length === 1 ? "step" : "steps"}`
              : read.isError
                ? "The take is not there any more"
                : "Reading the take…"}
          </span>
        </span>
        {open && ready && (
          <Button variant="outline" size="sm" onClick={() => open(take)}>
            <ExternalLink />
            Open
          </Button>
        )}
      </section>
    </div>
  )
}
