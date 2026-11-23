import { useQueryClient } from "@tanstack/react-query"
import { RefreshCw } from "lucide-react"
import { useEffect, useRef, useState } from "react"

import type { RoutineRun } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { toast } from "@/components/ui/toast"
import { useMainBot } from "@/features/bots/use-bots"
import { useRoutines, useRunNow } from "@/features/routines/use-routines"
import { errorText } from "@/lib/server-error"
import { cn } from "@/lib/utils"

import { lastRead, readBack, scanning, triageName } from "./scan-state"
import { inboxReads } from "./use-inbox"

// useTriage is the main Bot's triage Routine, read every few seconds with the Routines; undefined when there is none.
function useTriage() {
  const main = useMainBot()
  const routines = useRoutines({ bot: main || undefined })

  return routines.data?.find((r) => r.bot === main && r.name === triageName)
}

// ScanNow starts the triage at once instead of waiting for its next run (issue #1115), through the Routines' own
// "Run now" (useRunNow, POST /routines/{id}/runs), as Chatwoot and mail clients put a refresh on the list itself.
// While a run goes, whoever started it, the button says so and is disabled; when it ends, the Inbox is read again.
// Without a triage Routine there is nothing to run, and nothing shows.
export function ScanNow({ now }: { now: number }) {
  const triage = useTriage()
  const run = useRunNow()
  const queryClient = useQueryClient()
  // begun is the last run this button began: until the Routine read back shows it, the button does not come back
  // between the server's answer and that read.
  const [begun, setBegun] = useState<RoutineRun>()
  const busy =
    run.isPending || (!!triage && (scanning(triage) || (!!begun && !readBack(triage, begun))))

  // A run that was going and has ended refreshes the list at once, not at its next read.
  const was = useRef(busy)
  useEffect(() => {
    if (was.current && !busy) void queryClient.invalidateQueries({ predicate: inboxReads })
    was.current = busy
  }, [busy, queryClient])

  if (!triage) return null

  return (
    <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
      <span aria-live="polite">{lastRead(triage, busy, now)}</span>
      <Button
        variant="outline"
        size="xs"
        disabled={busy}
        onClick={() =>
          run.mutate(triage, {
            onSuccess: setBegun,
            onError: (error) =>
              toast.add({ title: "Not started", description: errorText(error), type: "error" }),
          })
        }
      >
        <RefreshCw data-icon="inline-start" className={cn(busy && "animate-spin")} />
        {busy ? "Scanning…" : "Scan now"}
      </Button>
    </div>
  )
}
