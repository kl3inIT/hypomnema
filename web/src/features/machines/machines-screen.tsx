import { useState, type ReactNode } from "react"

import type { Node } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { PageHeader } from "@/features/shell/app-shell"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

import { AddMachine } from "./add-machine"
import { RemoveMachine } from "./machine-actions"
import { IndicatorDot, MachineProblem, StateText } from "./machine-cells"
import { columns, type Column } from "./machines-columns"
import { errorText, machineNote, machineState, summary } from "./machines-state"
import { useMachinePower, useMachines } from "./use-machines"

type Layout = "grid" | "table"

const layoutKey = "hypomnema.machines.layout"

function readLayout(): Layout {
  try {
    return window.localStorage.getItem(layoutKey) === "table" ? "table" : "grid"
  } catch {
    return "grid"
  }
}

// MachinesScreen lists the nodes, after Beszel's systems-table (MIT, Copyright (c) 2024 henrygd,
// https://github.com/henrygd/beszel; internal/site/src/components/systems-table/systems-table.tsx): a Grid of
// cards or a Table, both drawn from one column definition (machines-columns.tsx). Changed: the layout is chosen
// with the Board's segmented control instead of Beszel's view menu, and kept in this browser; no filter, sorting,
// column picker or pagination; a machine has no page of its own, so a card or row is not a link.
export function MachinesScreen() {
  const machines = useMachines()
  const [layout, setLayout] = useState<Layout>(readLayout)
  const nodes = machines.data ?? []

  function choose(next: Layout) {
    setLayout(next)

    try {
      window.localStorage.setItem(layoutKey, next)
    } catch {
      // Kept until the page closes.
    }
  }

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader title="Machines">
        {nodes.length > 0 && (
          <span className="text-xs whitespace-nowrap text-muted-foreground">{summary(nodes)}</span>
        )}
        <div className="ms-auto flex items-center gap-2">
          <fieldset className="m-0 inline-flex gap-0.5 rounded-lg border border-solid bg-muted p-0.5">
            <legend className="sr-only">Layout</legend>
            {(["grid", "table"] as const).map((l) => (
              <button
                key={l}
                type="button"
                aria-pressed={layout === l}
                onClick={() => choose(l)}
                className="rounded-md px-2.5 py-1 text-xs text-muted-foreground capitalize aria-pressed:bg-foreground/9 aria-pressed:text-foreground"
              >
                {l}
              </button>
            ))}
          </fieldset>
          <AddMachine />
        </div>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-3 pt-1 pb-8 sm:px-5">
        {machines.error ? (
          <p className="py-10 text-center text-sm text-destructive">
            The machines could not be read.
          </p>
        ) : machines.isPending ? (
          <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
        ) : nodes.length === 0 ? (
          <Empty />
        ) : layout === "table" ? (
          <MachinesTable nodes={nodes} />
        ) : (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {nodes.map((node) => (
              <MachineCard key={node.id} node={node} />
            ))}
          </div>
        )}
      </div>
    </main>
  )
}

// MachinesTable is Beszel's AllSystemsTable: it scrolls inside its box.
function MachinesTable({ nodes }: { nodes: Node[] }) {
  return (
    <div className="relative max-w-full overflow-auto rounded-lg border">
      <table className="w-full text-sm">
        <thead className="border-b text-muted-foreground">
          <tr>
            {columns.map((column) => (
              <th
                key={column.id}
                scope="col"
                className="h-10 px-3 text-start text-xs font-medium whitespace-nowrap"
              >
                {column.id === "actions" ? (
                  <span className="sr-only">{column.name}</span>
                ) : (
                  <span className="flex items-center gap-2">
                    {column.Icon && <column.Icon className="size-4" aria-hidden />}
                    <Hinted column={column}>{column.name}</Hinted>
                  </span>
                )}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {nodes.map((node) => (
            <tr
              key={node.id}
              data-testid="machine"
              className={cn("border-b last:border-b-0", !node.connected && "text-foreground/70")}
            >
              {columns.map((column) => (
                <td
                  key={column.id}
                  className={cn(
                    "h-14 px-3 py-0",
                    ["cpu", "memory", "disk"].includes(column.id) && "min-w-32",
                  )}
                >
                  {column.cell(node)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// MachineCard is Beszel's SystemCard: a header with the status dot, the name and the actions, then one row a
// column: its icon, its name and its value.
function MachineCard({ node }: { node: Node }) {
  const actions = columns.find((c) => c.id === "actions")
  const note = machineNote(node)

  return (
    <article
      data-testid="machine"
      aria-label={node.name}
      className="w-full overflow-hidden rounded-xl border bg-card text-card-foreground"
    >
      <header className="flex items-center gap-2 border-b border-border/60 bg-muted/30 py-1 ps-4 pe-2">
        <h2 className="flex min-w-0 flex-1 items-center gap-2.5 text-sm font-semibold">
          <IndicatorDot node={node} />
          <span className="truncate">{node.name}</span>
        </h2>
        <StateText node={node} />
        {actions?.cell(node)}
      </header>
      {note ? <NotReady node={node} note={note} /> : <Measurements node={node} />}
    </article>
  )
}

// NotReady is the card of a provider's machine that has nothing to measure yet: why it failed, what happens next,
// what was asked for and when.
function NotReady({ node, note }: { node: Node; note: string }) {
  const [removing, setRemoving] = useState(false)
  const power = useMachinePower(node)
  const state = machineState(node)
  const failed = state === "create_failed"

  return (
    <div className="flex flex-col gap-2 px-5 pt-3.5 pb-4 text-sm">
      <MachineProblem node={node} className="text-sm break-words" />
      <p className="text-muted-foreground">{note}</p>
      {node.machine && (
        <p className="text-xs text-muted-foreground">
          {node.machine.size} in {node.machine.region}
          {(state === "creating" || failed) && ` · asked for ${since(node.created_at)}`}
        </p>
      )}
      {state === "stopped" && (
        <div className="flex items-center gap-2 pt-1">
          <Button size="sm" disabled={power.isPending} onClick={() => power.mutate(true)}>
            {power.isPending ? "Starting…" : "Start"}
          </Button>
          {power.error && (
            <span role="alert" className="min-w-0 truncate text-xs text-destructive">
              {errorText(power.error)}
            </span>
          )}
        </div>
      )}
      {failed && (
        <div className="pt-1">
          <Button variant="outline" size="sm" onClick={() => setRemoving(true)}>
            Remove
          </Button>
          <RemoveMachine node={node} open={removing} onOpenChange={setRemoving} />
        </div>
      )}
    </div>
  )
}

// Hinted is a column's name that says what the column measures when pointed at (owner, 2026-11-10).
function Hinted({ column, children }: { column: Column; children: ReactNode }) {
  if (!column.hint) return children

  return (
    <Tooltip>
      <TooltipTrigger render={<span className="cursor-help" />}>{children}</TooltipTrigger>
      <TooltipContent>
        <span className="block max-w-64 text-pretty">{column.hint}</span>
      </TooltipContent>
    </Tooltip>
  )
}

// Measurements is the card's rows: one a column, with its icon, its name and its value.
function Measurements({ node }: { node: Node }) {
  return (
    <>
      <dl
        className="grid gap-2.5 px-5 pt-3.5 pb-4 text-sm"
        style={{ gridTemplateColumns: "24px minmax(80px, max-content) minmax(0, 1fr)" }}
      >
        {columns.map((column) => {
          if (column.id === "machine" || column.id === "actions") return null

          return (
            <div key={column.id} className="col-span-3 grid grid-cols-subgrid items-center">
              <span className="flex items-center">
                {column.Icon && (
                  <column.Icon className="size-4 text-muted-foreground" aria-hidden />
                )}
              </span>
              <dt className="pe-3 text-muted-foreground">
                <Hinted column={column}>{column.name}:</Hinted>
              </dt>
              <dd className="flex min-w-0 items-center">{column.cell(node)}</dd>
            </div>
          )
        })}
      </dl>
    </>
  )
}

function Empty() {
  return (
    <div className="mx-auto flex max-w-sm flex-col items-center gap-4 py-16 text-center">
      <p className="text-sm text-muted-foreground">
        No machines yet. Add one, then run its install line on the machine that will do the work.
      </p>
      <AddMachine />
    </div>
  )
}
