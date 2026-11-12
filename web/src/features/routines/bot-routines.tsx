import { Link } from "@tanstack/react-router"
import { CalendarClock, ChevronRight, Plus, Repeat2 } from "lucide-react"
import { useState } from "react"

import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"

import { StatusMark } from "./parts"
import { RoutineForm } from "./routine-form"
import { orderRoutines, routineStatus, statusLine } from "./routines-state"
import { useRoutines, useSwitch } from "./use-routines"

// BotRoutines is the short list of a Bot's Routines on its page (issue #211). Adapted from OpenMausBot (Apache
// License 2.0, Copyright 2026 Milind Soni and OpenMausBot contributors, https://github.com/milind-soni/OpenMausBot;
// src/components/bot-settings/RoutinesSection.tsx: a card with the count and Create, the list, and a link to the
// Routines page; src/components/routines/RoutineList.tsx: each one its name, its schedule and a switch, and an
// empty list that says so). Changed: a row opens the Routine on the Routines page; its runs are there.

export function BotRoutines({ bot }: { bot: string }) {
  const routines = useRoutines({ bot })
  const flip = useSwitch()
  const [making, setMaking] = useState(false)
  const listed = orderRoutines(routines.data ?? [])

  return (
    <section aria-label="Routines" className="flex flex-col gap-3">
      <div className="flex items-center gap-2">
        <CalendarClock className="size-4 text-primary" aria-hidden />
        <h3 className="min-w-0 flex-1 text-sm font-medium">Routines</h3>
        <span className="text-xs text-muted-foreground tabular-nums">{listed.length}</span>
        <Button size="xs" variant="outline" onClick={() => setMaking(true)}>
          <Plus />
          New
        </Button>
      </div>

      {routines.error ? (
        <p role="alert" className="rounded-lg bg-failed-soft p-3 text-xs text-failed">
          The Routines could not be read.
        </p>
      ) : routines.isPending ? (
        <p className="text-xs text-muted-foreground">Loading…</p>
      ) : listed.length === 0 ? (
        <div className="rounded-xl border border-dashed p-5 text-center text-[13px] text-muted-foreground">
          <Repeat2 className="mx-auto mb-2 size-5 opacity-60" aria-hidden />
          No Routines yet. Ask {bot} in a chat, or make one.
        </div>
      ) : (
        <ul className="flex flex-col gap-2">
          {listed.map((r) => (
            <li key={r.id} className="flex items-start gap-2.5 rounded-xl border bg-card p-3.5">
              <StatusMark status={routineStatus(r)} className="mt-1.5" />
              <Link
                to="/routines"
                search={{ bot, open: r.id }}
                className="min-w-0 flex-1 hover:text-primary"
              >
                <span className="block truncate text-[13px] font-semibold">{r.name}</span>
                <span className="mt-0.5 block text-xs leading-relaxed text-muted-foreground">
                  {r.schedule.charAt(0).toUpperCase() + r.schedule.slice(1)}
                </span>
                <span className="mt-0.5 block text-xs text-muted-foreground">{statusLine(r)}</span>
              </Link>
              <span className="mt-1 shrink-0">
                <Switch
                  checked={r.state === "on"}
                  disabled={flip.isPending}
                  aria-label={`Turn ${r.name} on or off`}
                  onCheckedChange={() => flip.mutate(r)}
                />
              </span>
            </li>
          ))}
        </ul>
      )}

      <Link
        to="/routines"
        search={{ bot }}
        className="flex items-center gap-0.5 self-start text-xs text-link hover:underline"
      >
        All of {bot}'s Routines
        <ChevronRight className="size-3.5" aria-hidden />
      </Link>

      <RoutineForm open={making} onOpenChange={setMaking} bot={bot} />
    </section>
  )
}
