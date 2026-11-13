import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"
import { PageHeader, PageHeading } from "@/features/shell/app-shell"
import { useWork } from "@/features/work/use-work"
import type { Card } from "@/features/work/work-state"

import { byColumn, columns, lanes, repos, type Group } from "./board-state"
import { TaskCard } from "./task-card"

const dots: Record<string, string> = {
  you: "bg-needs-you",
  work: "bg-running",
  done: "bg-done",
}

// BoardScreen is the work in flight: three columns by status (Orca's AgentKanbanBoard, MIT, Copyright (c) 2026
// Lovecast Inc., https://github.com/stablyai/orca; components/dashboard-popout/AgentKanbanBoard.tsx), optionally in
// rows per Run or per repository, filtered to one repository. On a phone the columns stack, Needs you first.
export function BoardScreen({
  group,
  repo,
  onChange,
  onOpen,
}: {
  group: Group
  repo: string
  onChange: (next: { group?: Group; repo?: string }) => void
  onOpen: (taskId: string) => void
}) {
  const { cards, loading, error } = useWork()
  const rows = lanes(cards, group, repo)
  const known = repos(cards)

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader />

      <div className="min-h-0 flex-1 overflow-y-auto px-3 pt-1 pb-8 sm:px-5">
        <PageHeading
          title="Board"
          description="Every Task in flight: what needs you, what is working and what is done. Click a card to open it."
          className="mb-5"
        >
          <span className="text-xs text-muted-foreground max-sm:hidden">Group by</span>
          {/* The same switch as the Machines page's Grid and Table: kept by hand, as that one is, because the
              standard toggle group does not show which one is on as clearly. */}
          <fieldset className="m-0 inline-flex gap-0.5 rounded-lg border border-solid bg-muted p-0.5">
            <legend className="sr-only">Group by</legend>
            {(["status", "run", "repo"] as const).map((g) => (
              <button
                key={g}
                type="button"
                aria-pressed={group === g}
                onClick={() => onChange({ group: g })}
                className="rounded-md px-2.5 py-1 text-xs text-muted-foreground capitalize aria-pressed:bg-foreground/9 aria-pressed:text-foreground"
              >
                {g}
              </button>
            ))}
          </fieldset>
          <Select
            value={repo}
            onValueChange={(value) => onChange({ repo: value ?? "" })}
            items={[
              { value: "", label: "All repos" },
              ...known.map((r) => ({ value: r, label: r })),
            ]}
          >
            <SelectTrigger aria-label="Repository" size="sm">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">All repos</SelectItem>
              {known.map((r) => (
                <SelectItem key={r} value={r}>
                  {r}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </PageHeading>

        {error ? (
          <p className="py-10 text-center text-sm text-destructive">The board could not be read.</p>
        ) : loading ? (
          <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
        ) : cards.length === 0 ? (
          <Empty />
        ) : (
          rows.map((lane) => (
            <section key={lane.key} className={cn(group !== "status" && "mb-6")}>
              {group !== "status" && (
                <div className="flex items-center gap-2 px-0.5 pb-2 text-xs text-muted-foreground">
                  <h2 className="truncate text-[13px] font-medium text-foreground">{lane.title}</h2>
                  {lane.subtitle && <span className="truncate max-sm:hidden">{lane.subtitle}</span>}
                  <span
                    className="h-1 w-16 shrink-0 overflow-hidden rounded-full bg-border"
                    aria-hidden
                  >
                    <span
                      className="block h-full bg-done"
                      style={{ width: `${(100 * lane.done) / lane.cards.length}%` }}
                    />
                  </span>
                  <span className="tabular-nums">
                    {lane.done}/{lane.cards.length}
                  </span>
                </div>
              )}
              <Columns cards={lane.cards} showRun={group !== "run"} onOpen={onOpen} />
            </section>
          ))
        )}
      </div>
    </main>
  )
}

function Columns({
  cards,
  showRun,
  onOpen,
}: {
  cards: Card[]
  showRun: boolean
  onOpen: (id: string) => void
}) {
  const split = byColumn(cards)

  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
      {columns.map((col) => {
        const list = split[col.key]

        return (
          <section
            key={col.key}
            aria-label={col.name}
            className={cn(
              "flex min-w-0 flex-col md:rounded-xl md:border md:border-border/60 md:bg-muted",
              list.length === 0 && "max-md:hidden",
            )}
          >
            <h3 className="flex items-center gap-1.5 px-0.5 py-1.5 text-2xs font-semibold tracking-[0.05em] text-muted-foreground uppercase md:px-3 md:pt-2.5">
              <span className={cn("size-[7px] rounded-full", dots[col.key])} aria-hidden />
              {col.name}
              <span className="rounded-full bg-background px-1.5 tabular-nums">{list.length}</span>
              {col.hint && (
                <span className="ms-auto font-normal tracking-normal normal-case">{col.hint}</span>
              )}
            </h3>
            <div className="flex flex-col gap-2 pb-1 md:px-2 md:pb-2.5">
              {list.length === 0 ? (
                <p className="py-3 text-center text-xs text-muted-foreground/60">None</p>
              ) : (
                list.map((c) => (
                  <TaskCard
                    key={c.task.id}
                    card={c}
                    showRun={showRun}
                    onOpen={() => onOpen(c.task.id)}
                  />
                ))
              )}
            </div>
          </section>
        )
      })}
    </div>
  )
}

function Empty() {
  return (
    <div className="mx-auto max-w-sm py-16 text-center">
      <p className="text-sm text-foreground">Nothing in flight.</p>
      <p className="mt-1 text-sm text-muted-foreground">
        Ask pi in a chat to start a Run; its workers show here while they work and for a day after.
      </p>
    </div>
  )
}
