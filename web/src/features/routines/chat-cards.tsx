import { Link, useNavigate } from "@tanstack/react-router"
import { ArrowRight, Loader2, Repeat2 } from "lucide-react"
import { useState } from "react"

import type { RoutineRun } from "@/api/generated/types.gen"
import { cn } from "@/lib/utils"

import { RoutineForm } from "./routine-form"
import { errorText, lastWhen, routineStatus, statusLine, capital } from "./routines-state"
import { useRoutine, useRoutineRuns, useSwitch, useUndo } from "./use-routines"

// The cards a Routine has in a chat (issue #211), adapted from OpenMausBot (Apache License 2.0, Copyright 2026
// Milind Soni and OpenMausBot contributors, https://github.com/milind-soni/OpenMausBot):
// src/components/TriggerCard.tsx L70-98 ("Set up: <sentence>" that opens it, its status line, and Pause · Edit ·
// Undo) and src/components/RoutineRunCard.tsx L83-124 (a run's name, its state, when, what it said with the whole
// report folded, and Open, or Review when it waits for the owner). Changed: the card reads the Routine and its run
// from the server; a script run has no chat to open.

const action = "rounded px-1 py-0.5 hover:bg-accent hover:text-foreground disabled:opacity-50"

export function SetUpCard({ routineId }: { routineId: string }) {
  const routine = useRoutine(routineId)
  const flip = useSwitch()
  const undo = useUndo()
  const navigate = useNavigate()
  const [editing, setEditing] = useState(false)
  const r = routine.data

  if (routine.error) {
    return (
      <p className="mb-4 w-full max-w-[640px] rounded-xl border border-dashed px-3.5 py-2.5 text-[13px] text-muted-foreground">
        This Routine no longer exists.
      </p>
    )
  }
  if (!r) return null

  const status = routineStatus(r)
  const line = statusLine(r)
  const error = flip.error ?? undo.error

  return (
    <section
      aria-label={`Set up: ${r.sentence}`}
      data-routine-card={r.id}
      className="mb-4 w-full max-w-[640px] rounded-xl border bg-card px-3.5 py-2.5 text-[13px]"
    >
      <div className="flex items-start gap-2">
        <Repeat2
          aria-hidden
          className={cn(
            "mt-0.5 size-3.5 shrink-0",
            status.state === "needs-you"
              ? "text-needs-you"
              : r.state === "on"
                ? "text-primary"
                : "text-muted-foreground",
          )}
        />
        <button
          type="button"
          onClick={() => void navigate({ to: "/routines", search: { bot: r.bot, open: r.id } })}
          className="min-w-0 text-left leading-snug font-medium hover:text-primary"
        >
          Set up: {r.sentence}
        </button>
      </div>
      <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 pl-5.5 text-xs">
        <span
          className={cn(
            "min-w-0 flex-1 basis-48 truncate",
            status.state === "needs-you" ? "text-needs-you" : "text-muted-foreground",
          )}
          title={line}
        >
          {status.state === "needs-you" ? `Needs you: ${status.reason}` : line}
        </span>
        <span className="flex shrink-0 items-center gap-0.5 text-muted-foreground">
          <button
            type="button"
            disabled={flip.isPending}
            onClick={() => flip.mutate(r)}
            className={action}
          >
            {r.state === "on" ? "Pause" : "Resume"}
          </button>
          <span aria-hidden>·</span>
          <button type="button" onClick={() => setEditing(true)} className={action}>
            Edit
          </button>
          {r.can_undo && (
            <>
              <span aria-hidden>·</span>
              <button
                type="button"
                disabled={undo.isPending}
                onClick={() => undo.mutate(r)}
                className={cn(action, "text-primary")}
              >
                {undo.isPending ? "Undoing…" : "Undo"}
              </button>
            </>
          )}
        </span>
      </div>
      {error && (
        <p role="alert" className="mt-1 pl-5.5 text-xs text-needs-you">
          {errorText(error)}
        </p>
      )}
      <RoutineForm open={editing} onOpenChange={setEditing} routine={r} />
    </section>
  )
}

const runCopy: Record<RoutineRun["state"], { label: string; tone: string }> = {
  running: { label: "Running", tone: "text-running" },
  waiting: { label: "Waits for you", tone: "text-needs-you" },
  succeeded: { label: "Done", tone: "text-done" },
  failed: { label: "Didn't finish", tone: "text-failed" },
  canceled: { label: "Stopped", tone: "text-muted-foreground" },
}

const detailLimit = 240

export function RunCard({
  routineId,
  runId,
  text,
  createdAt,
}: {
  routineId: string
  runId: string
  // text is what the message says, for when the run is no longer kept.
  text: string
  createdAt: string
}) {
  const routine = useRoutine(routineId)
  const runs = useRoutineRuns(routineId)
  const run = runs.data?.pages.flatMap((p) => p.data).find((x) => x.id === runId)
  const name = routine.data?.name ?? "A Routine"
  const said = (
    run?.output ||
    run?.error ||
    text.replace(/^Routine "[^"]*" ran \([^)]*\):\s*/, "")
  ).trim()
  const copy = run ? runCopy[run.state] : runCopy.succeeded
  const at = run?.started_at ?? createdAt

  return (
    <section
      aria-label={`${name}: ${copy.label}`}
      data-run-card={runId}
      className="mb-4 w-full max-w-[680px] rounded-xl border bg-card px-4 py-3"
    >
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
            <h3 className="truncate text-sm font-semibold">{name}</h3>
            <span
              aria-live="polite"
              className={cn("inline-flex items-center gap-1 text-xs font-semibold", copy.tone)}
            >
              {run?.state === "running" && <Loader2 aria-hidden className="size-3 animate-spin" />}
              {copy.label}
            </span>
          </div>
          <time dateTime={at} className="mt-0.5 block text-xs text-muted-foreground">
            {capital(lastWhen(at))}
          </time>
          {said && (
            <p className="mt-1.5 text-[13px] leading-relaxed whitespace-pre-wrap text-muted-foreground">
              {said.length > detailLimit ? `${said.slice(0, detailLimit - 1).trimEnd()}…` : said}
            </p>
          )}
          {said.length > detailLimit && (
            <details className="mt-2 text-xs text-muted-foreground">
              <summary className="cursor-pointer font-medium hover:text-foreground">
                Show the report
              </summary>
              <p className="mt-2 leading-relaxed whitespace-pre-wrap">{said}</p>
            </details>
          )}
        </div>
        {run?.chat_id && (
          <Link
            to="/chats/$chatId"
            params={{ chatId: run.chat_id }}
            aria-label={`${run.state === "waiting" ? "Review" : "Open"} ${name}`}
            className={cn(
              "flex shrink-0 items-center gap-1 rounded-md px-2 py-1 text-xs font-semibold",
              run.state === "waiting"
                ? "bg-needs-you-soft text-needs-you hover:bg-needs-you/20"
                : "text-muted-foreground hover:bg-accent hover:text-foreground",
            )}
          >
            {run.state === "waiting" ? "Review" : "Open"}
            <ArrowRight aria-hidden className="size-3.25" />
          </Link>
        )}
      </div>
    </section>
  )
}
