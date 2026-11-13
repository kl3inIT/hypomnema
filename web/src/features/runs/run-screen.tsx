import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { ChevronRight, GitPullRequest, MessageCircleQuestion } from "lucide-react"

import "@/api/client"
import { cancelRun, finishRun, getRun, listQuestions } from "@/api/generated/sdk.gen"
import type { Question, RunDetail, TaskDetail } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { QuestionForm } from "@/features/questions/question-form"
import { PageHeader } from "@/features/shell/app-shell"
import { firstLine, pullRequest, repoLabel } from "@/features/work/work-state"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

// RunScreen is one Run: its objective, the questions its workers put to the owner (answered in place, as t3code docks
// a pending question), its Tasks as rows that open in the Task panel, and the questions already answered, folded to
// one line each (t3code's "Answered questions" work row).
export function RunScreen({
  runId,
  onOpenTask,
}: {
  runId: string
  onOpenTask: (taskId: string) => void
}) {
  const queryClient = useQueryClient()
  const run = useQuery({
    queryKey: ["run", runId],
    queryFn: async () => (await getRun({ path: { run_id: runId } })).data,
  })
  const questions = useQuery({
    queryKey: ["questions", "run", runId],
    queryFn: async () => (await listQuestions({ query: { run_id: runId } })).data.data,
  })

  const refresh = () => {
    for (const queryKey of [["run"], ["runs"], ["board"]])
      void queryClient.invalidateQueries({ queryKey })
  }
  const finish = useMutation({
    mutationFn: () => finishRun({ path: { run_id: runId } }),
    onSuccess: refresh,
  })
  const cancel = useMutation({
    mutationFn: () => cancelRun({ path: { run_id: runId } }),
    onSuccess: refresh,
  })

  const r = run.data
  const open = (questions.data ?? []).filter(
    (q) => q.status === "open" && q.addressed_to === "owner",
  )
  const answered = (questions.data ?? []).filter((q) => q.status === "answered")
  const allEnded =
    r?.tasks.every((t) => ["succeeded", "failed", "canceled"].includes(t.status)) ?? false

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader
        title={
          <span className="flex min-w-0 items-center gap-1.5">
            <Link to="/runs" className="text-muted-foreground hover:text-foreground">
              Runs
            </Link>
            <span className="text-muted-foreground/50" aria-hidden>
              /
            </span>
            <span className="truncate">{r ? firstLine(r.objective, 80) : ""}</span>
          </span>
        }
      >
        {r?.status === "open" && (
          <div className="ms-auto flex gap-1.5">
            <Button
              variant="outline"
              disabled={!allEnded || finish.isPending}
              title={allEnded ? undefined : "Every Task must end first"}
              onClick={() => finish.mutate()}
            >
              Finish Run
            </Button>
            <Button
              variant="destructive"
              disabled={cancel.isPending}
              onClick={() => cancel.mutate()}
            >
              Cancel
            </Button>
          </div>
        )}
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {run.isError ? (
          <p className="py-10 text-center text-sm text-destructive">This Run could not be read.</p>
        ) : !r ? (
          <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
        ) : (
          <div className="mx-auto flex w-full max-w-(--chat-content-max-width) flex-col gap-6 px-3 pt-2 pb-12 sm:px-6">
            <Overview run={r} />
            {open.map((q) => (
              <section key={q.id} aria-label={`Question from Task ${position(r, q.task_id)}`}>
                <p className="mb-1.5 text-xs text-muted-foreground">
                  Task {position(r, q.task_id)} ·{" "}
                  {firstLine(r.tasks.find((t) => t.id === q.task_id)?.spec ?? "", 80)}
                </p>
                <QuestionForm question={q} />
              </section>
            ))}
            <Tasks run={r} asking={new Set(open.map((q) => q.task_id))} onOpen={onOpenTask} />
            {answered.length > 0 && <Answered run={r} questions={answered} />}
          </div>
        )}
      </div>
    </main>
  )
}

function position(run: RunDetail, taskId: string) {
  return run.tasks.find((t) => t.id === taskId)?.position ?? "?"
}

const runTone = {
  open: "text-running",
  finished: "text-done",
  canceled: "text-muted-foreground",
}

function Overview({ run }: { run: RunDetail }) {
  return (
    <section className="flex flex-col gap-2.5">
      <p className="text-[15px] leading-relaxed whitespace-pre-wrap text-foreground/90">
        {run.objective}
      </p>
      <div className="flex flex-wrap gap-1.5 text-xs text-muted-foreground">
        <span
          className={cn(
            "inline-flex h-6 items-center rounded-md bg-foreground/5 px-2 capitalize",
            runTone[run.status],
          )}
        >
          {run.status}
        </span>
        {run.repo && (
          <span className="inline-flex h-6 items-center rounded-md bg-foreground/5 px-2">
            {repoLabel(run)}
          </span>
        )}
        {run.base && (
          <span className="inline-flex h-6 items-center rounded-md bg-foreground/5 px-2">
            from {run.base}
          </span>
        )}
        {run.chat_id && (
          <Link
            to="/chats/$chatId"
            params={{ chatId: run.chat_id }}
            className="inline-flex h-6 items-center rounded-md bg-foreground/5 px-2 hover:text-foreground"
          >
            Chat with pi
          </Link>
        )}
        <span className="inline-flex h-6 items-center rounded-md bg-foreground/5 px-2">
          Started {since(run.created_at)}
        </span>
      </div>
    </section>
  )
}

const taskTone: Record<TaskDetail["status"], string> = {
  pending: "text-muted-foreground",
  ready: "text-muted-foreground",
  running: "text-running",
  blocked: "text-needs-you",
  succeeded: "text-done",
  failed: "text-failed",
  canceled: "text-muted-foreground",
}

function Tasks({
  run,
  asking,
  onOpen,
}: {
  run: RunDetail
  asking: Set<string>
  onOpen: (id: string) => void
}) {
  return (
    <section aria-label="Tasks">
      <h2 className="flex items-center gap-2 px-1 pb-1.5 text-xs font-medium text-muted-foreground/75">
        Tasks <span className="text-muted-foreground/50 tabular-nums">{run.tasks.length}</span>
        <span className="h-px flex-1 bg-border" aria-hidden />
      </h2>
      {run.tasks.length === 0 && (
        <p className="px-1 py-3 text-sm text-muted-foreground">No Tasks yet.</p>
      )}
      <div className="flex flex-col">
        {run.tasks.map((t) => {
          const pr = pullRequest(t)

          return (
            <button
              key={t.id}
              type="button"
              onClick={() => onOpen(t.id)}
              className={cn(
                "flex w-full flex-col gap-0.5 rounded-lg px-2.5 py-2 text-left hover:bg-accent",
                asking.has(t.id) && "bg-needs-you/6",
              )}
            >
              <span className="flex min-w-0 items-center gap-2.5">
                <span className="w-5 shrink-0 text-xs text-muted-foreground tabular-nums">
                  {t.position}
                </span>
                <span className="min-w-0 flex-1 truncate text-sm text-foreground/90">
                  {firstLine(t.spec)}
                </span>
                {pr && (
                  <span className="inline-flex shrink-0 items-center gap-0.5 text-2xs text-done">
                    <GitPullRequest className="size-3" aria-hidden />
                    {pr.label}
                  </span>
                )}
                <span
                  className={cn(
                    "shrink-0 text-xs font-medium",
                    asking.has(t.id) ? "text-needs-you" : taskTone[t.status],
                  )}
                >
                  {asking.has(t.id) ? "Waiting for you" : t.status}
                </span>
                <ChevronRight className="size-3.5 shrink-0 text-icon-muted" aria-hidden />
              </span>
              {(t.latest_dispatch?.result?.summary || t.result) && (
                <span className="ms-7.5 line-clamp-1 text-xs text-secondary-label">
                  {t.latest_dispatch?.result?.summary || t.result}
                </span>
              )}
            </button>
          )
        })}
      </div>
    </section>
  )
}

function answerText(q: Question) {
  const label = q.answer?.option !== undefined ? q.options[q.answer.option]?.label : undefined

  return [label, q.answer?.text].filter(Boolean).join(" — ")
}

function Answered({ run, questions }: { run: RunDetail; questions: Question[] }) {
  return (
    <section aria-label="Answered questions">
      <h2 className="flex items-center gap-2 px-1 pb-1.5 text-xs font-medium text-muted-foreground/75">
        Answered <span className="text-muted-foreground/50 tabular-nums">{questions.length}</span>
        <span className="h-px flex-1 bg-border" aria-hidden />
      </h2>
      {questions.map((q) => (
        <details key={q.id} className="group rounded-lg px-2.5 py-1 open:bg-accent/50">
          <summary className="flex min-h-7 cursor-pointer list-none items-center gap-2 text-sm text-secondary-label">
            <MessageCircleQuestion className="size-3.5 shrink-0" aria-hidden />
            <span className="min-w-0 flex-1 truncate">
              Task {position(run, q.task_id)}: {firstLine(q.text, 80)}
              <span className="text-foreground"> → {answerText(q)}</span>
            </span>
            <span className="shrink-0 text-xs">
              {q.answered_by === "coordinator" ? "pi" : "you"}
            </span>
          </summary>
          <div className="ms-5.5 space-y-1.5 pt-1 pb-2 text-sm">
            <p className="whitespace-pre-wrap text-muted-foreground">{q.text}</p>
            <p className="whitespace-pre-wrap">{answerText(q)}</p>
          </div>
        </details>
      ))}
    </section>
  )
}
