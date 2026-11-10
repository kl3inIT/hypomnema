import { CircleAlert, GitPullRequest, MessageCircleQuestion } from "lucide-react"

import { span } from "@/lib/time"
import { cn } from "@/lib/utils"

import { firstLine, pullRequest, type Card } from "./board-state"
import { RuntimeMark } from "./runtime-mark"

// TaskCard is one worker's Task on the board, after Orca's AgentKanbanCard (MIT, Copyright (c) 2026 Lovecast Inc.,
// https://github.com/stablyai/orca; components/dashboard-popout/AgentKanbanCard.tsx): what runs it and what it is
// for on top, one line of what it does now, then machine, model, pull request and age. A card waiting for the
// owner is tinted and shows the question; it is answered in the Task's panel, where the whole question is read.
export function TaskCard({
  card,
  showRun = true,
  onOpen,
}: {
  card: Card
  showRun?: boolean
  onOpen: () => void
}) {
  const { task, run } = card
  const pr = pullRequest(task)
  const dispatch = task.latest_dispatch

  return (
    <button
      type="button"
      onClick={onOpen}
      data-testid="task-card"
      className={cn(
        "flex w-full flex-col gap-1.5 rounded-lg border bg-card p-2.5 text-left transition-colors outline-none hover:border-border-strong focus-visible:ring-2 focus-visible:ring-ring/60 max-sm:p-3",
        card.column === "you" &&
          (card.question
            ? "border-needs-you/40 bg-[color-mix(in_srgb,var(--status-needs-you)_6%,var(--card))]"
            : "border-failed/40 bg-[color-mix(in_srgb,var(--status-failed-soft)_40%,var(--card))]"),
        card.queued && "opacity-60",
      )}
    >
      <span className="flex min-w-0 items-start gap-2">
        <RuntimeMark runtime={card.runtime} />
        <span className="line-clamp-2 min-w-0 flex-1 text-[13px] leading-snug font-medium">
          {firstLine(task.spec)}
        </span>
        <StatusDot card={card} />
      </span>

      {showRun && (
        <span className="truncate text-2xs text-muted-foreground">
          {firstLine(run.objective, 80)}
          {run.repo && ` · ${run.repo.split("/").at(-1)}`}
        </span>
      )}

      <Middle card={card} />

      <span className="flex flex-wrap items-center gap-1.5 text-2xs text-muted-foreground">
        {card.node && (
          <span className="rounded-md bg-foreground/5 px-1.5 leading-[18px]">{card.node}</span>
        )}
        {(dispatch?.model || task.model) && <span>{dispatch?.model || task.model}</span>}
        {pr && (
          <span className="inline-flex items-center gap-0.5 font-mono text-done">
            <GitPullRequest className="size-3" aria-hidden />
            {pr.label}
          </span>
        )}
        <span className="ms-auto tabular-nums">{span(task.updated_at)}</span>
      </span>
    </button>
  )
}

function StatusDot({ card }: { card: Card }) {
  if (card.queued) {
    return (
      <span
        aria-hidden
        className="mt-1.5 size-[7px] shrink-0 rounded-full border border-dashed border-muted-foreground"
      />
    )
  }

  const tone = {
    you: card.question ? "bg-needs-you" : "bg-failed",
    work: "bg-running motion-safe:animate-pulse",
    done: card.task.status === "succeeded" ? "bg-done" : "bg-muted-foreground",
  }[card.column]

  return <span aria-hidden className={cn("mt-1.5 size-[7px] shrink-0 rounded-full", tone)} />
}

const blocked: Record<string, string> = {
  gate: "Stopped for your decision",
  needs_auth: "Its account needs a login on the machine",
  dispatch_unknown: "Lost touch with its worker",
  dependency_failed: "Waits: a Task it needs failed",
}

// Middle is the card's one line of state: the question, the failure, or what the worker does now, which is the
// newest line of its transcript (issue #43) and how long it has run.
function Middle({ card }: { card: Card }) {
  const { task } = card
  const dispatch = task.latest_dispatch

  if (card.question) {
    return (
      <span className="block rounded-md bg-needs-you/12 px-2 py-1.5 text-xs text-needs-you ring-1 ring-needs-you/25 ring-inset">
        <span className="flex items-center gap-1">
          <MessageCircleQuestion className="size-3" aria-hidden />
          {card.question.mode === "gate" ? "Decision needed" : "Waits for you"}
        </span>
        <span className="mt-0.5 line-clamp-3 text-foreground">{card.question.text}</span>
        <span className="mt-1 block font-medium">Answer →</span>
      </span>
    )
  }

  if (card.column === "you") {
    const why =
      task.status === "failed"
        ? `Failed${task.failure_count > 1 ? ` ${task.failure_count} times` : ""}${dispatch?.failure_class ? `: ${dispatch.failure_class}` : ""}`
        : (blocked[task.blocked_reason ?? ""] ?? "Blocked")

    return (
      <span className="flex items-start gap-1 rounded-md bg-failed-soft px-2 py-1.5 text-xs text-failed">
        <CircleAlert className="mt-px size-3 shrink-0" aria-hidden />
        {why}
      </span>
    )
  }

  if (card.piQuestion) {
    return (
      <span className="truncate text-xs text-secondary-label">
        Asked pi: {card.piQuestion.text}
      </span>
    )
  }

  if (task.status === "blocked") {
    return (
      <span className="text-xs text-secondary-label">
        {blocked[task.blocked_reason ?? ""] ?? "Blocked"}
      </span>
    )
  }

  if (card.queued) {
    return (
      <span className="text-xs text-secondary-label">
        {task.status === "ready" ? "Ready, waits for a machine" : "Waits for the Tasks it needs"}
      </span>
    )
  }

  if (task.status === "running" && dispatch?.started_at) {
    return (
      <span className="flex min-w-0 items-baseline gap-1 text-xs text-secondary-label">
        <span className="min-w-0 live-tool-shine truncate">{dispatch.activity || "Working"}</span>
        <span className="shrink-0 tabular-nums">· {span(dispatch.started_at)}</span>
      </span>
    )
  }

  const summary = dispatch?.result?.summary || task.result

  return summary ? (
    <span className="line-clamp-2 text-xs text-secondary-label">{firstLine(summary, 200)}</span>
  ) : null
}
