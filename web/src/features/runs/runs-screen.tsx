import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"

import "@/api/client"
import { listRuns } from "@/api/generated/sdk.gen"
import type { Run } from "@/api/generated/types.gen"
import { firstLine, repoLabel, type Card } from "@/features/board/board-state"
import { useBoard } from "@/features/board/use-board"
import { PageHeader, PageHeading } from "@/features/shell/app-shell"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

// RunsScreen lists the Runs in groups, after t3code's pull request list (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code; pullRequest/PullRequestListGroups.tsx, PullRequestListRow.tsx): a header
// with a count and a rule, then two-line rows. Runs with work for the owner come first, as Orca groups its crew.
export function RunsScreen() {
  const runs = useQuery({
    queryKey: ["runs"],
    queryFn: async () => (await listRuns({ query: { limit: 100 } })).data,
  })
  const { cards } = useBoard()

  const byRun = new Map<string, Card[]>()
  for (const c of cards) byRun.set(c.run.id, [...(byRun.get(c.run.id) ?? []), c])

  const needs = (r: Run) => (byRun.get(r.id) ?? []).some((c) => c.column === "you")
  const all = runs.data?.data ?? []
  const groups: { name: string; tone: string; runs: Run[] }[] = [
    {
      name: "Needs you",
      tone: "bg-needs-you",
      runs: all.filter((r) => r.status === "open" && needs(r)),
    },
    {
      name: "Open",
      tone: "bg-running",
      runs: all.filter((r) => r.status === "open" && !needs(r)),
    },
    { name: "Finished", tone: "bg-done", runs: all.filter((r) => r.status === "finished") },
    {
      name: "Canceled",
      tone: "bg-muted-foreground",
      runs: all.filter((r) => r.status === "canceled"),
    },
  ]

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-4xl flex-col gap-6 px-3 pt-2 pb-10 sm:px-6">
          <PageHeading
            title="Runs"
            description="Each Run is one objective pi works on, split into Tasks. The Runs that need you come first."
          />
          {runs.isPending && (
            <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
          )}
          {runs.data && all.length === 0 && (
            <p className="py-16 text-center text-sm text-muted-foreground">
              No Runs yet. Ask pi in a chat to start one.
            </p>
          )}
          {groups
            .filter((g) => g.runs.length > 0)
            .map((g) => (
              <section key={g.name} aria-label={g.name}>
                <h2 className="flex items-center gap-2 px-3 pb-1 text-xs font-medium text-muted-foreground/75">
                  <span className={cn("size-[7px] rounded-full", g.tone)} aria-hidden />
                  {g.name}
                  <span className="text-muted-foreground/50 tabular-nums">{g.runs.length}</span>
                  <span className="h-px min-w-2 flex-1 bg-border" aria-hidden />
                </h2>
                <div className="flex flex-col gap-0.5">
                  {g.runs.map((r) => (
                    <RunRow key={r.id} run={r} cards={byRun.get(r.id)} />
                  ))}
                </div>
              </section>
            ))}
        </div>
      </div>
    </main>
  )
}

function RunRow({ run, cards }: { run: Run; cards?: Card[] }) {
  const done = cards?.filter((c) => c.task.status === "succeeded").length ?? 0
  const asking = cards?.find((c) => c.question)

  return (
    <Link
      to="/runs/$runId"
      params={{ runId: run.id }}
      className="flex flex-col gap-0.5 rounded-lg px-3 py-2 hover:bg-accent"
    >
      <span className="truncate text-sm text-foreground/90">{firstLine(run.objective)}</span>
      <span className="flex min-w-0 items-center gap-1.5 text-2xs text-muted-foreground">
        {run.repo && <span className="truncate font-mono">{repoLabel(run)}</span>}
        {cards && cards.length > 0 && (
          <span className="shrink-0">
            · {done}/{cards.length} Tasks done
          </span>
        )}
        {asking && <span className="truncate text-needs-you">· asks: {asking.question?.text}</span>}
        <span className="ms-auto shrink-0 tabular-nums">{since(run.updated_at)}</span>
      </span>
    </Link>
  )
}
