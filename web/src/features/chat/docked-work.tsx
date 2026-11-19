import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import {
  Ban,
  ChevronRight,
  Circle,
  CircleCheck,
  CircleDot,
  CirclePause,
  CircleStop,
  CircleX,
  MessageCircleQuestion,
  Square,
  type LucideIcon,
} from "lucide-react"
import { useMemo } from "react"

import "@/api/client"
import { listProfilesOptions } from "@/api/generated/@tanstack/react-query.gen"
import type { RunDetail, TaskDetail } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import { RuntimeMark } from "@/components/icons/runtime-mark"
import { workUsage } from "@/features/models/usage-state"
import { QuestionForm } from "@/features/questions/question-form"
import { ConfirmAction, RunActions } from "@/features/runs/run-actions"
import { useCancelTask } from "@/features/tasks/task-actions"
import { useNow } from "@/hooks/use-now"
import { firstLine } from "@/lib/text"
import { waitingText } from "@/features/work/waiting"
import { runtimeOf } from "@/features/work/work-state"
import { cn } from "@/lib/utils"

import {
  askingTasks,
  canStop,
  liveLine,
  newestActivity,
  progress,
  runState,
  taskState,
  type DockIcon,
  type DockTone,
} from "./dock-state"
import { dockId, type DockedWork } from "./use-docked-work"

// The chat's own work docked above its composer (issue #314): the questions its Runs' workers put to the owner,
// answered with the QuestionForm beside the Approval cards (t3code's ComposerPendingUserInputPanel.tsx, which
// QuestionForm follows), and its open Runs as strips that open to their Tasks, with Finish and Cancel, after Orca's
// NativeChatDockedTaskList.tsx and NativeChatTaskList.tsx (MIT, Copyright (c) 2026 Lovecast Inc.,
// https://github.com/stablyai/orca). Changed from Orca: a strip a Run instead of one plan, a Task opens its panel,
// and the Run's own actions sit under its Tasks. A running Task's row says how long its worker runs and what it
// does, with Stop beside it, after t3code's subagent rows (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code; chat/V2LifecycleRow.tsx, chat/ThreadActivityBanners.tsx and
// chat/ThreadRelationshipsControl.tsx; issue #687).

const icons: Record<DockIcon, LucideIcon> = {
  queued: Circle,
  working: CircleDot,
  done: CircleCheck,
  failed: CircleX,
  blocked: CirclePause,
  canceled: Ban,
  question: MessageCircleQuestion,
  stopping: CircleStop,
}

const tones: Record<DockTone, string> = {
  running: "text-running",
  done: "text-done",
  failed: "text-failed",
  "needs-you": "text-needs-you",
  quiet: "text-muted-foreground",
}

// DockedQuestions are the open questions to the owner from this chat's Runs, each with the Task it comes from.
export function DockedQuestions({ work }: { work: DockedWork }) {
  const tasks = useMemo(
    () => new Map(work.runs.flatMap((r) => r.tasks.map((t) => [t.id, t] as const))),
    [work.runs],
  )

  if (work.questions.length === 0) return null

  return (
    <div className="flex flex-col gap-3">
      {work.questions.map((q) => {
        const task = tasks.get(q.task_id)
        const from = task ? `Task ${task.position} · ${firstLine(task.spec, 80)}` : "A worker asks"

        return (
          <section key={q.id} aria-label={`Question: ${firstLine(q.text, 80)}`}>
            <p className="mb-1.5 truncate px-1 text-xs text-muted-foreground">{from}</p>
            <QuestionForm question={q} />
          </section>
        )
      })}
    </div>
  )
}

// DockedRuns are the chat's open Runs, a strip each.
export function DockedRuns({ chatId, work }: { chatId: string; work: DockedWork }) {
  const profiles = useQuery({ ...listProfilesOptions(), select: (page) => page.data })
  const runtimes = useMemo(
    () => new Map((profiles.data ?? []).map((p) => [p.name, p.runtime])),
    [profiles.data],
  )
  const asking = useMemo(() => askingTasks(work.questions), [work.questions])
  // The clock ticks only while a Task runs or stops.
  const now = useNow(
    work.runs.some((r) => r.tasks.some((t) => t.status === "running" || !!t.cancel_requested_at)),
  )

  if (work.runs.length === 0) return null

  return (
    <ul aria-label="Runs of this chat" className="flex flex-col gap-1.5">
      {work.runs.map((run) => (
        <DockedRun
          key={run.id}
          id={dockId(chatId, run.id)}
          run={run}
          asking={asking}
          runtimes={runtimes}
          now={now}
          open={!!work.open[run.id]}
          onOpenChange={(next) => work.setOpen(run.id, next)}
        />
      ))}
    </ul>
  )
}

function DockedRun({
  id,
  run,
  asking,
  runtimes,
  now,
  open,
  onOpenChange,
}: {
  id: string
  run: RunDetail
  asking: ReadonlySet<string>
  runtimes: Map<string, string>
  now: number
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const state = runState(run, asking)
  const Icon = icons[state.icon]
  const objective = firstLine(run.objective, 120)
  const activity = newestActivity(run)

  return (
    <li
      id={id}
      aria-label={`Run ${objective}`}
      className="rounded-md border border-border bg-muted/30"
    >
      <Collapsible open={open} onOpenChange={onOpenChange}>
        {/* The strip is a row that opens, Orca's trigger, kept as its own design rather than a Button
            (docs/conventions.md): it spans the card and its words truncate. Under it, the newest thing its
            running workers do, as t3code's running-work line. */}
        <CollapsibleTrigger
          render={
            <button
              type="button"
              aria-label={`${objective}: ${state.word}, ${progress(run)}${activity ? `. ${activity}` : ""}`}
              className="flex w-full min-w-0 flex-col gap-0.5 rounded-md px-3 py-2 text-left text-xs text-muted-foreground outline-none hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring max-sm:min-h-11"
            />
          }
        >
          <span className="flex w-full min-w-0 items-center gap-1.5">
            <Icon aria-hidden className={cn("size-4 shrink-0", tones[state.tone])} />
            <span className="min-w-0 flex-1 truncate font-medium text-foreground/90">
              {objective}
            </span>
            <span className={cn("shrink-0 font-medium", tones[state.tone])}>{state.word}</span>
            <span aria-hidden className="shrink-0 text-muted-foreground/50">
              ·
            </span>
            <span className="shrink-0 tabular-nums">{progress(run)}</span>
            <ChevronRight
              aria-hidden
              className={cn("size-3.5 shrink-0 transition-transform", open && "rotate-90")}
            />
          </span>
          {activity && (
            <span className="w-full truncate ps-5.5 pe-5" title={activity}>
              {activity}
            </span>
          )}
        </CollapsibleTrigger>
        <CollapsibleContent>
          {run.tasks.length === 0 ? (
            <p className="px-3 pb-2 text-xs text-muted-foreground">No Tasks yet.</p>
          ) : (
            <ul aria-label="Tasks" className="max-h-36 overflow-y-auto px-1.5 pb-1">
              {run.tasks.map((task) => (
                <TaskRow
                  key={task.id}
                  task={task}
                  asking={asking.has(task.id)}
                  runtime={runtimeOf(task, runtimes)}
                  now={now}
                />
              ))}
            </ul>
          )}
          {workUsage(run.usage) && (
            <p className="truncate border-t border-border px-3 py-1.5 text-xs text-muted-foreground tabular-nums">
              Used {workUsage(run.usage)}
            </p>
          )}
          {run.status === "open" && (
            <div className="flex justify-end border-t border-border px-2 py-1.5">
              <RunActions run={run} size="xs" />
            </div>
          )}
        </CollapsibleContent>
      </Collapsible>
    </li>
  )
}

// TaskRow is one Task: its status icon and words, its worker's runtime and its first line, and under it, for a
// queued Task, why it waits (issue #660), for a running one how long its worker runs and what it does, and for one
// asked to stop how long since (issue #687). It opens the Task's panel over the chat, kept in the URL as a
// wake-up's Task chip does. A row that opens stays its own design rather than a Button (docs/conventions.md), as
// the Run page's rows; Stop sits beside it.
function TaskRow({
  task,
  asking,
  runtime,
  now,
}: {
  task: TaskDetail
  asking: boolean
  runtime: string
  now: number
}) {
  const navigate = useNavigate()
  const state = taskState(task, asking)
  const Icon = icons[state.icon]
  const title = firstLine(task.spec, 120)
  const waiting = task.status === "ready" && !asking ? waitingText(task.waiting_reason) : undefined
  const detail = liveLine(task, now) ?? waiting

  return (
    <li className="group/task flex min-w-0 items-center gap-0.5">
      <button
        type="button"
        aria-label={`Open Task ${title}: ${state.word}${detail ? `. ${detail}` : ""}`}
        onClick={() =>
          void navigate({
            to: ".",
            search: (prev: Record<string, unknown>) => ({ ...prev, task: task.id }),
          })
        }
        className="flex min-h-7 w-full min-w-0 flex-1 items-center gap-2 rounded-md px-1.5 py-1 text-left text-xs hover:bg-accent max-sm:min-h-9"
      >
        <Icon aria-hidden className={cn("size-3.5 shrink-0", tones[state.tone])} />
        <RuntimeMark runtime={runtime} className="size-4.5" />
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate text-foreground/90" title={title}>
            {title}
          </span>
          {detail && (
            <span className="truncate text-muted-foreground tabular-nums" title={detail}>
              {detail}
            </span>
          )}
        </span>
        <span className={cn("shrink-0", tones[state.tone])}>{state.word}</span>
      </button>
      {canStop(task) && <StopTask task={task} />}
    </li>
  )
}

// StopTask stops a running or queued Task from its row, asking first as every action that ends something does
// (docs/conventions.md, AlertDialog), with the mutation of the panel's Cancel Task. The square shows on hover or
// focus, and always on a touch screen, as t3code's "Stop subagent" (ThreadRelationshipsControl.tsx).
function StopTask({ task }: { task: TaskDetail }) {
  const cancel = useCancelTask(task.id)
  const name = firstLine(task.spec, 80)
  const running = task.status === "running"

  return (
    <span className="shrink-0 opacity-0 group-focus-within/task:opacity-100 group-hover/task:opacity-100 pointer-coarse:opacity-100">
      <ConfirmAction
        trigger={
          <Button variant="ghost" size="icon-sm" aria-label={`Stop Task ${name}`} title="Stop Task">
            <Square aria-hidden className="size-3 fill-current" />
          </Button>
        }
        title={`Stop the Task "${name}"?`}
        consequence={
          running
            ? "Its worker is stopped and the Task is canceled. This cannot be undone."
            : "The Task is canceled before a worker starts on it. This cannot be undone."
        }
        action="Stop Task"
        keep={running ? "Keep it running" : "Keep it queued"}
        destructive
        mutation={cancel}
      />
    </span>
  )
}
