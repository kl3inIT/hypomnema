import { Link } from "@tanstack/react-router"
import { Pause, Pencil, Play, Trash2, X } from "lucide-react"
import type { ReactNode } from "react"

import type { Routine } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { PendingApprovals } from "@/features/approvals/approval-card"
import { ConfirmDialog } from "@/features/settings/settings-parts"
import { cn } from "@/lib/utils"

import { FixButton, StatusMark } from "./parts"
import {
  errorText,
  kindLine,
  lastWhen,
  lasted,
  limitLine,
  nextWhen,
  routineStatus,
  runLine,
  startedByWords,
  statusWords,
} from "./routines-state"
import {
  useRemove,
  useRevoke,
  useRoutine,
  useRoutineRuns,
  useRunNow,
  useSwitch,
} from "./use-routines"

// RoutinePanel is one Routine beside the list (issue #211). Adapted from OpenMausBot (Apache License 2.0,
// Copyright 2026 Milind Soni and OpenMausBot contributors, https://github.com/milind-soni/OpenMausBot;
// src/components/automations/AutomationPanel.tsx L54-152): the name and who it is for, the status with its one
// fix, what it does, its recent runs one line each that open their chat, and Run now, Pause or Resume, Edit and
// Delete at the foot. The runs come a page at a time with "Load more" and how many are shown of how many, after
// OpenClaw (MIT, Copyright (c) 2026 OpenClaw Foundation, https://github.com/openclaw/openclaw;
// ui/src/components/cron-jobs-pagination.tsx). Changed: it adds the schedule's facts, the script's text, what was
// allowed always with "Take back", and the Approvals its runs wait on.

export function RoutinePanel({
  routineId,
  onClose,
  onEdit,
  onRemoved,
  onAnswer,
}: {
  routineId: string
  onClose: () => void
  onEdit: (routine: Routine) => void
  onRemoved: () => void
  onAnswer: (routine: Routine) => void
}) {
  const routine = useRoutine(routineId)
  const r = routine.data

  return (
    <aside
      aria-label="Routine"
      className="flex h-full w-full flex-col border-l bg-background sm:w-[min(400px,100vw)]"
    >
      <header className="flex items-start gap-3 border-b px-5 py-4">
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-base leading-snug font-semibold">
            {r?.name ?? (routine.error ? "Routine" : "Loading…")}
          </h2>
          {r && (
            <p className="mt-0.5 truncate text-xs text-muted-foreground">
              {r.bot} · {kindLine(r)}
            </p>
          )}
        </div>
        <Button variant="ghost" size="icon-sm" aria-label="Close" onClick={onClose}>
          <X />
        </Button>
      </header>
      {routine.error ? (
        <p className="px-5 py-6 text-sm text-muted-foreground">
          This Routine could not be read. It may have been removed.
        </p>
      ) : r ? (
        <Body routine={r} onEdit={onEdit} onRemoved={onRemoved} onAnswer={onAnswer} />
      ) : null}
    </aside>
  )
}

function Body({
  routine,
  onEdit,
  onRemoved,
  onAnswer,
}: {
  routine: Routine
  onEdit: (routine: Routine) => void
  onRemoved: () => void
  onAnswer: (routine: Routine) => void
}) {
  const status = routineStatus(routine)
  const runNow = useRunNow()
  const flip = useSwitch()
  const remove = useRemove()
  const busy = runNow.isPending || flip.isPending
  const error = runNow.error ?? flip.error

  return (
    <>
      <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-5 py-4">
        <div className="flex flex-wrap items-center gap-2 text-sm" data-testid="routine-status">
          <StatusMark status={status} />
          <span className={cn("font-medium", status.state === "needs-you" && "text-needs-you")}>
            {statusWords[status.state]}
          </span>
          {(status.reason ?? routine.paused_reason) && (
            <span className="min-w-0 text-muted-foreground">
              {status.reason ?? routine.paused_reason}
            </span>
          )}
          <FixButton
            routine={routine}
            status={status}
            busy={busy}
            onAnswer={(r) =>
              r.last_run?.chat_id
                ? onAnswer(r)
                : document
                    .getElementById("routine-approvals")
                    ?.scrollIntoView({ behavior: "smooth", block: "start" })
            }
            onRun={(r) => runNow.mutate(r)}
          />
        </div>

        <p className="text-sm leading-relaxed">{routine.sentence}</p>

        <Facts routine={routine} />

        <div id="routine-approvals" className="flex flex-col gap-3 empty:hidden">
          <PendingApprovals routineId={routine.id} />
        </div>

        <Section title={routine.kind === "script" ? `Script · ${routine.script}` : "Prompt"}>
          <pre
            data-testid="routine-work"
            className="max-h-64 overflow-auto rounded-lg bg-muted px-3 py-2 font-mono text-xs leading-relaxed break-words whitespace-pre-wrap"
          >
            {routine.kind === "script" ? routine.script_text : routine.prompt}
          </pre>
        </Section>

        <Grants routine={routine} />

        <Runs routine={routine} />

        {error && (
          <p role="alert" className="rounded-lg bg-failed-soft px-3 py-2 text-xs text-failed">
            {errorText(error)}
          </p>
        )}
      </div>

      <footer className="grid grid-cols-2 gap-2 border-t px-5 py-3.5">
        <Button disabled={busy} onClick={() => runNow.mutate(routine)}>
          <Play />
          {runNow.isPending ? "Starting…" : "Run now"}
        </Button>
        <Button variant="outline" disabled={busy} onClick={() => flip.mutate(routine)}>
          {routine.state === "on" ? <Pause /> : <Play />}
          {routine.state === "on" ? "Pause" : "Resume"}
        </Button>
        <Button variant="ghost" onClick={() => onEdit(routine)}>
          <Pencil />
          Edit
        </Button>
        <ConfirmDialog
          trigger={
            <Button variant="destructive">
              <Trash2 />
              Delete
            </Button>
          }
          title={`Delete ${routine.name}?`}
          consequence={`“${routine.sentence}” A run still going is stopped, and its runs are forgotten.`}
          action="Delete"
          run={() => remove.mutateAsync(routine)}
          onDone={onRemoved}
          done="Routine deleted"
        />
      </footer>
    </>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-xs font-semibold tracking-wider text-muted-foreground uppercase">
        {title}
      </h3>
      {children}
    </section>
  )
}

function Facts({ routine }: { routine: Routine }) {
  const rows: [string, ReactNode][] = [
    ["Schedule", routine.schedule.charAt(0).toUpperCase() + routine.schedule.slice(1)],
    [
      "Next run",
      routine.state === "on" && routine.next_run_at
        ? nextWhen(routine.next_run_at)
        : "Not scheduled",
    ],
    ["Time limit", routine.kind === "script" ? limitLine(routine.timeout_seconds) : "1 hour"],
    [
      "Results",
      routine.chat_id && routine.announce ? (
        <Link
          key="results"
          to="/chats/$chatId"
          params={{ chatId: routine.chat_id }}
          className="text-link hover:underline"
        >
          The chat it was made in
        </Link>
      ) : (
        "Told nowhere"
      ),
    ],
    ["Made by", routine.created_by === "bot" ? `${routine.bot}, in a chat` : "You"],
  ]

  if (routine.skipped > 0) {
    rows.push([
      "Skipped",
      `${routine.skipped} time${routine.skipped === 1 ? "" : "s"} while a run still went${
        routine.last_skipped_at ? ` · last ${lastWhen(routine.last_skipped_at)}` : ""
      }`,
    ])
  }

  return (
    <dl className="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-sm">
      {rows.map(([term, value]) => (
        <div key={term} className="contents">
          <dt className="text-muted-foreground">{term}</dt>
          <dd className="min-w-0 break-words">{value}</dd>
        </div>
      ))}
    </dl>
  )
}

// Grants are what the owner allowed this Routine always; each can be taken back.
function Grants({ routine }: { routine: Routine }) {
  const revoke = useRevoke()

  if (routine.grants.length === 0) return null

  return (
    <Section title="Allowed always">
      <ul className="flex flex-col gap-1">
        {routine.grants.map((g) => (
          <li
            key={`${g.integration}:${g.label}:${g.action}`}
            className="flex items-center gap-2 rounded-lg border px-3 py-2 text-sm"
          >
            <span className="min-w-0 flex-1">
              <span className="block truncate">{g.action_name}</span>
              <span className="block truncate text-xs text-muted-foreground">
                {g.integration}:{g.label}
              </span>
            </span>
            <Button
              variant="outline"
              size="xs"
              disabled={revoke.isPending}
              onClick={() => revoke.mutate({ routine, grant: g })}
            >
              Take back
            </Button>
          </li>
        ))}
      </ul>
      {revoke.error && (
        <p role="alert" className="text-xs text-destructive">
          {errorText(revoke.error)}
        </p>
      )}
    </Section>
  )
}

const markTone = {
  "✓": "text-done",
  "✗": "text-failed",
  "…": "text-running",
  "!": "text-needs-you",
}

// Runs lists the recent runs, one line each; a prompt run opens its chat.
function Runs({ routine }: { routine: Routine }) {
  const runs = useRoutineRuns(routine.id)
  const pages = runs.data?.pages ?? []
  const shown = pages.flatMap((p) => p.data)
  const total = Math.max(pages[0]?.total ?? 0, shown.length)

  return (
    <Section title="Recent runs">
      {runs.isPending ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : shown.length === 0 ? (
        <p className="text-sm text-muted-foreground">It hasn't run yet.</p>
      ) : (
        <ul className="-mx-2 flex flex-col">
          {shown.map((run) => {
            const line = runLine(run)
            const body = (
              <>
                <span className="shrink-0 text-muted-foreground">{line.when}</span>
                <span className={cn("shrink-0 font-semibold", markTone[line.mark])}>
                  {line.mark}
                </span>
                <span className="min-w-0 flex-1 truncate">{line.text}</span>
              </>
            )
            const title = `${startedByWords[run.started_by]}${lasted(run) ? ` · took ${lasted(run)}` : ""}`
            const row = "flex w-full items-baseline gap-2 rounded-lg px-2 py-1.5 text-left text-sm"

            return (
              <li key={run.id} data-testid="run" data-state={run.state}>
                {run.chat_id ? (
                  <Link
                    to="/chats/$chatId"
                    params={{ chatId: run.chat_id }}
                    title={`Open · ${title}`}
                    className={cn(row, "hover:bg-accent")}
                  >
                    {body}
                  </Link>
                ) : (
                  <div title={title} className={row}>
                    {body}
                  </div>
                )}
              </li>
            )
          })}
        </ul>
      )}
      {shown.length > 0 && (
        <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
          <span>
            {shown.length} of {total} shown
          </span>
          {runs.hasNextPage && (
            <Button
              variant="outline"
              size="xs"
              disabled={runs.isFetchingNextPage}
              onClick={() => void runs.fetchNextPage()}
            >
              {runs.isFetchingNextPage ? "Loading…" : "Load more"}
            </Button>
          )}
        </div>
      )}
    </Section>
  )
}
