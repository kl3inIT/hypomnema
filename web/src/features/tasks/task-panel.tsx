import { Dialog } from "@base-ui/react/dialog"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { ArrowDown, ExternalLink, X } from "lucide-react"
import type { ReactNode } from "react"
import { StickToBottom, useStickToBottomContext } from "use-stick-to-bottom"

import "@/api/client"
import { cancelTask, forwardQuestion, getRun, getTask, reopenTask } from "@/api/generated/sdk.gen"
import type { Dispatch } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { since, span } from "@/lib/time"

import { RuntimeMark } from "@/features/agents/runtime-mark"
import { PendingApprovals } from "@/features/approvals/approval-card"
import { QuestionForm } from "@/features/questions/question-form"
import { boardKey, questionsKey, useWork } from "@/features/work/use-work"
import {
  boardCards,
  firstLine,
  evidenceLink,
  pullRequest,
  repoLabel,
  type Card,
} from "@/features/work/work-state"

import { Transcript } from "./transcript"

// TaskPanel opens one Task over the page it came from, as Omnigent opens a session over its canvas
// (web/src/canvas/CanvasWorkspace.tsx): Escape or the close button returns to the board as it was. The page keeps
// the open Task in its URL, so a link or a reload opens it again.
export function TaskPanel({ taskId, onClose }: { taskId?: string; onClose: () => void }) {
  return (
    <Dialog.Root open={!!taskId} onOpenChange={(open) => !open && onClose()}>
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-40 bg-black/60 transition-opacity data-ending-style:opacity-0 data-starting-style:opacity-0" />
        <Dialog.Popup className="fixed inset-y-0 right-0 z-50 flex w-full max-w-(--chat-content-max-width) flex-col border-l bg-background transition-transform outline-none data-ending-style:translate-x-8 data-starting-style:translate-x-8 max-sm:border-l-0">
          {taskId && <PanelBody taskId={taskId} />}
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

function useCard(taskId: string): Card | undefined {
  const { cards } = useWork()
  const onBoard = cards.find((c) => c.task.id === taskId)

  // A Task off the board (an old Run's) is read on its own.
  const task = useQuery({
    queryKey: ["task", taskId],
    queryFn: async () => (await getTask({ path: { task_id: taskId } })).data,
    enabled: !onBoard,
  })
  const run = useQuery({
    queryKey: ["run", task.data?.run_id],
    queryFn: async () => (await getRun({ path: { run_id: task.data?.run_id ?? "" } })).data,
    enabled: !onBoard && !!task.data,
  })

  if (onBoard) return onBoard
  if (!task.data || !run.data) return undefined

  return boardCards({ data: [task.data], runs: [run.data], after: 0 })[0]
}

function PanelBody({ taskId }: { taskId: string }) {
  const card = useCard(taskId)

  if (!card) {
    return (
      <>
        <Dialog.Title className="sr-only">Task</Dialog.Title>
        <p className="p-6 text-sm text-muted-foreground">Loading…</p>
      </>
    )
  }

  const { task, run } = card
  const pr = pullRequest(task)
  const dispatch = task.latest_dispatch
  const live = isLive(dispatch)

  return (
    <>
      <header className="flex h-(--workspace-topbar-height) shrink-0 items-center gap-2.5 border-b px-4">
        <RuntimeMark runtime={card.runtime} />
        <Dialog.Title className="min-w-0 flex-1 truncate text-sm font-medium">
          {firstLine(task.spec)}
        </Dialog.Title>
        <Dialog.Close
          aria-label="Close"
          className="flex size-8 items-center justify-center rounded-lg text-muted-foreground hover:bg-accent hover:text-foreground"
        >
          <X className="size-4" />
        </Dialog.Close>
      </header>

      {/* The panel opens at a running worker's newest step and follows it (Omnigent's Conversation, with
          use-stick-to-bottom); a question or a finished Task opens at the top, where the reader starts. */}
      <StickToBottom
        className="relative min-h-0 flex-1 overflow-y-hidden"
        initial={live && !card.question ? "instant" : false}
        resize="smooth"
      >
        <StickToBottom.Content className="flex flex-col gap-5 px-5 pt-4 pb-8">
          <div className="flex flex-wrap gap-1.5 text-xs text-muted-foreground">
            <Chip>{statusText(card)}</Chip>
            <Link
              to="/runs/$runId"
              params={{ runId: run.id }}
              className="inline-flex h-6 max-w-full items-center truncate rounded-md bg-foreground/5 px-2 hover:text-foreground"
            >
              {firstLine(run.objective, 60)}
            </Link>
            {run.repo && <Chip>{repoLabel(run)}</Chip>}
            {card.node && <Chip>{card.node}</Chip>}
            {(dispatch?.model || task.model) && <Chip>{dispatch?.model || task.model}</Chip>}
            <Chip>{task.profile}</Chip>
            {pr && (
              <a
                href={pr.url}
                target="_blank"
                rel="noreferrer"
                className="inline-flex h-6 items-center gap-1 rounded-md bg-foreground/5 px-2 text-done hover:underline"
              >
                {pr.label}
                <ExternalLink className="size-3" aria-hidden />
              </a>
            )}
          </div>

          <PendingApprovals taskId={task.id} />
          {card.question && <QuestionForm question={card.question} />}
          {card.piQuestion && <PiQuestion card={card} />}
          <Actions card={card} />

          {(dispatch?.result || task.result) && (
            <Section title="Result">
              <p className="text-sm leading-relaxed whitespace-pre-wrap text-foreground/88">
                {dispatch?.result?.summary || task.result}
              </p>
              {dispatch?.result?.evidence.map((e) => (
                <p key={e.kind + e.ref} className="mt-1 truncate text-xs text-muted-foreground">
                  {e.kind}:{" "}
                  {evidenceLink(e.kind, e.ref) ? (
                    <a
                      href={evidenceLink(e.kind, e.ref)}
                      target="_blank"
                      rel="noreferrer"
                      className="text-link hover:underline"
                    >
                      {e.ref}
                    </a>
                  ) : (
                    <span className="font-mono">{e.ref}</span>
                  )}
                </p>
              ))}
            </Section>
          )}

          <Section title="Task">
            <p className="text-sm leading-relaxed whitespace-pre-wrap text-foreground/88">
              {task.spec}
            </p>
          </Section>

          <Section title="Transcript">
            {dispatch ? (
              <Transcript
                key={dispatch.id}
                dispatchId={dispatch.id}
                live={live}
                worked={workedFor(dispatch)}
              />
            ) : (
              <p className="rounded-lg border border-dashed border-border-strong px-3 py-2.5 text-xs text-muted-foreground">
                No worker has started on it yet.
              </p>
            )}
          </Section>
        </StickToBottom.Content>
        <JumpToLatest />
      </StickToBottom>
    </>
  )
}

// workedFor is how long a worker that succeeded took; its transcript then folds under it. A failed or stopped
// worker's transcript stays open, as t3code leaves an interrupted or failed turn.
function workedFor(dispatch: Dispatch) {
  if (dispatch.status !== "succeeded" || !dispatch.started_at || !dispatch.settled_at)
    return undefined

  return span(dispatch.started_at, new Date(dispatch.settled_at).getTime())
}

// isLive says whether the Dispatch's worker may still be running.
function isLive(dispatch: Dispatch | undefined) {
  return !!dispatch && ["starting", "running", "stopping"].includes(dispatch.status)
}

function JumpToLatest() {
  const { isAtBottom, scrollToBottom } = useStickToBottomContext()
  if (isAtBottom) return null

  return (
    <span className="absolute bottom-3 left-1/2 -translate-x-1/2">
      <Button
        variant="outline"
        size="icon"
        aria-label="Jump to the latest"
        onClick={() => void scrollToBottom()}
      >
        <ArrowDown />
      </Button>
    </span>
  )
}

function statusText(card: Card) {
  if (card.question) return "Waiting for you"

  return {
    pending: "Waiting for the Tasks it needs",
    ready: "Ready",
    running: "Working",
    blocked: "Blocked",
    succeeded: "Succeeded",
    failed: "Failed",
    canceled: "Canceled",
  }[card.task.status]
}

function Chip({ children }: { children: ReactNode }) {
  return (
    <span className="inline-flex h-6 items-center rounded-md bg-foreground/5 px-2">{children}</span>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section>
      <h3 className="mb-1.5 text-xs font-medium text-muted-foreground">{title}</h3>
      {children}
    </section>
  )
}

function useRefresh() {
  const queryClient = useQueryClient()

  return () => {
    for (const queryKey of [boardKey, questionsKey, ["run"], ["task"]])
      void queryClient.invalidateQueries({ queryKey })
  }
}

// PiQuestion shows a question the worker put to pi. pi answers it; the owner can take it over, which hands it to the
// owner (POST /questions/{id}/forward), as a coordinator passes a question on.
function PiQuestion({ card }: { card: Card }) {
  const refresh = useRefresh()
  const take = useMutation({
    mutationFn: () => forwardQuestion({ path: { question_id: card.piQuestion?.id ?? "" } }),
    onSuccess: refresh,
  })

  return (
    <div className="rounded-xl border px-4 py-3">
      <p className="text-xs text-muted-foreground">
        Asked pi · {since(card.piQuestion?.created_at ?? "")}
      </p>
      <p className="my-1.5 text-sm leading-relaxed whitespace-pre-wrap">{card.piQuestion?.text}</p>
      <Button variant="outline" onClick={() => take.mutate()} disabled={take.isPending}>
        Answer it myself
      </Button>
    </div>
  )
}

// Actions are what the owner can do to a Task in its state: start a failed or blocked one again, or cancel one
// that has not ended.
function Actions({ card }: { card: Card }) {
  const refresh = useRefresh()
  const { task } = card
  const reopen = useMutation({
    mutationFn: () => reopenTask({ path: { task_id: task.id } }),
    onSuccess: refresh,
  })
  const cancel = useMutation({
    mutationFn: () => cancelTask({ path: { task_id: task.id } }),
    onSuccess: refresh,
  })

  const canReopen =
    card.run.status === "open" &&
    (task.status === "failed" ||
      (task.status === "blocked" && task.blocked_reason === "needs_auth"))
  const canCancel =
    !["succeeded", "failed", "canceled"].includes(task.status) && !task.cancel_requested_at
  const error = reopen.error ?? cancel.error

  if (!canReopen && !canCancel) return null

  return (
    <div className="flex flex-wrap items-center gap-2">
      {canReopen && (
        <Button onClick={() => reopen.mutate()} disabled={reopen.isPending}>
          Start again
        </Button>
      )}
      {canCancel && (
        <Button variant="destructive" onClick={() => cancel.mutate()} disabled={cancel.isPending}>
          Cancel Task
        </Button>
      )}
      {error && <span className="text-xs text-destructive">Not done: try again.</span>}
    </div>
  )
}
