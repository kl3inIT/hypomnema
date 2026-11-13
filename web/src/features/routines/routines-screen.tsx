import { useNavigate } from "@tanstack/react-router"
import { Plus, Repeat2 } from "lucide-react"
import { useState } from "react"

import type { Routine } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { useBots } from "@/features/bots/use-bots"
import { AvatarName } from "@/features/agents/agent-avatar"
import { PageHeader, PageHeading } from "@/features/shell/app-shell"
import { cn } from "@/lib/utils"

import { FixButton, StatusMark } from "./parts"
import { RoutineForm } from "./routine-form"
import { RoutinePanel } from "./routine-panel"
import { errorText, orderRoutines, routineStatus, statusLine } from "./routines-state"
import { useRoutines, useRunNow, useSwitch } from "./use-routines"

// RoutinesScreen is the Routines of every Bot, or of one (issue #211), with the open one beside the list. Adapted
// from OpenMausBot (Apache License 2.0, Copyright 2026 Milind Soni and OpenMausBot contributors,
// https://github.com/milind-soni/OpenMausBot; src/components/TriggersPage.tsx L76-155): a Bot filter and New in
// the header; each row a status mark, the name, the sentence, a status line or, when it needs the owner, the reason
// and its one button, and an on/off switch; the panel beside the list, over it on a narrow screen; and an empty
// list that says what a Routine is. Changed: no calendar, no departments; a Routine's runs are on its panel.

export function RoutinesScreen({
  bot,
  open,
  onChange,
}: {
  // bot is the Bot whose Routines are shown; "" for every Bot's.
  bot: string
  // open is the Routine whose panel is open.
  open?: string
  onChange: (next: { bot?: string; open?: string }) => void
}) {
  const routines = useRoutines({ bot: bot || undefined })
  const bots = useBots()
  const flip = useSwitch()
  const runNow = useRunNow()
  const navigate = useNavigate()
  const [editing, setEditing] = useState<Routine | "new" | null>(null)
  const listed = orderRoutines(routines.data ?? [])
  const error = flip.error ?? runNow.error

  // answer goes where a waiting run asks: a prompt run's chat, or the panel that holds a script run's Approval.
  const answer = (r: Routine) => {
    if (r.last_run?.chat_id) {
      void navigate({ to: "/chats/$chatId", params: { chatId: r.last_run.chat_id } })
    } else {
      onChange({ open: r.id })
    }
  }

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader />

      <div className="relative flex min-h-0 flex-1">
        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="w-full px-3 pt-1 pb-8 sm:px-5">
            <PageHeading
              title="Routines"
              description="Work a Bot does on its own, on a schedule."
              className="mb-5"
            >
              <Select
                value={bot || "all"}
                onValueChange={(next) => onChange({ bot: next === "all" ? "" : (next ?? "") })}
              >
                <SelectTrigger size="sm" aria-label="Bot">
                  <SelectValue>
                    {(v: string) => (v === "all" ? "All Bots" : <AvatarName name={v} />)}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">All Bots</SelectItem>
                  {(bots.data ?? []).map((b) => (
                    <SelectItem key={b.name} value={b.name}>
                      <AvatarName name={b.name} />
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Button size="sm" onClick={() => setEditing("new")}>
                <Plus />
                New Routine
              </Button>
            </PageHeading>
            {routines.error ? (
              <div className="flex flex-col items-center gap-3 py-12 text-sm text-muted-foreground">
                <p>The Routines could not be read.</p>
                <Button variant="outline" size="sm" onClick={() => void routines.refetch()}>
                  Try again
                </Button>
              </div>
            ) : routines.isPending ? (
              <p className="py-12 text-center text-sm text-muted-foreground">Loading…</p>
            ) : listed.length === 0 ? (
              <section aria-label="No Routines" className="px-6 py-16 text-center">
                <div className="mx-auto mb-4 flex size-12 items-center justify-center rounded-2xl bg-primary/10 text-primary">
                  <Repeat2 className="size-5.5" />
                </div>
                <h2 className="text-base font-semibold">
                  {bot ? `${bot} has no Routines yet.` : "Your Bots can do work on their own."}
                </h2>
                <p className="mx-auto mt-2 max-w-sm text-sm text-muted-foreground">
                  A Routine is work a Bot does on a schedule. Ask a Bot in its chat, such as “every
                  morning at 7, summarize what changed overnight”, or make one here.
                </p>
                <div className="mt-6">
                  <Button onClick={() => setEditing("new")}>
                    <Plus />
                    New Routine
                  </Button>
                </div>
              </section>
            ) : (
              <ul
                aria-label="Routines"
                className="divide-y overflow-hidden rounded-2xl border bg-card"
              >
                {listed.map((r) => {
                  const status = routineStatus(r)

                  return (
                    <li
                      key={r.id}
                      data-routine={r.id}
                      data-status={status.state}
                      className={cn(
                        "flex items-start gap-3 px-4 py-3.5",
                        open === r.id && "bg-accent/60",
                      )}
                    >
                      <StatusMark status={status} className="mt-1.5" />
                      <div className="min-w-0 flex-1">
                        <button
                          type="button"
                          onClick={() => onChange({ open: r.id })}
                          className="block max-w-full truncate text-left text-[15px] leading-snug font-medium hover:text-primary"
                        >
                          {r.name}
                        </button>
                        <p className="mt-0.5 text-[13px]">{r.sentence}</p>
                        {status.state === "needs-you" ? (
                          <div className="mt-1.5 flex flex-wrap items-center gap-2 text-[13px] text-needs-you">
                            Needs you: {status.reason}
                            <FixButton
                              routine={r}
                              status={status}
                              busy={runNow.isPending}
                              onAnswer={answer}
                              onRun={(x) => runNow.mutate(x)}
                            />
                          </div>
                        ) : (
                          <p className="mt-0.5 text-[13px] text-muted-foreground">
                            {statusLine(r)}
                          </p>
                        )}
                      </div>
                      <span className="mt-1 shrink-0">
                        <Switch
                          checked={r.state === "on"}
                          disabled={flip.isPending}
                          aria-label={`Turn ${r.name} on or off`}
                          onCheckedChange={() => flip.mutate(r)}
                        />
                      </span>
                    </li>
                  )
                })}
              </ul>
            )}
            {error && (
              <p role="alert" className="mt-3 text-xs text-destructive">
                {errorText(error)}
              </p>
            )}
          </div>
        </div>
        {open && (
          <div className="absolute inset-0 z-30 flex justify-end shadow-2xl sm:inset-y-0 sm:right-0 sm:left-auto lg:static lg:shadow-none">
            <RoutinePanel
              key={open}
              routineId={open}
              onClose={() => onChange({ open: undefined })}
              onEdit={(r) => setEditing(r)}
              onRemoved={() => onChange({ open: undefined })}
              onAnswer={answer}
            />
          </div>
        )}
      </div>

      <RoutineForm
        open={editing !== null}
        onOpenChange={(next) => !next && setEditing(null)}
        routine={editing === "new" || editing === null ? undefined : editing}
        bot={bot}
        onSaved={(r) => onChange({ open: r.id })}
      />
    </main>
  )
}
