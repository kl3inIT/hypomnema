import { useContext, type ReactNode } from "react"

import type { Node, NodeGpu } from "@/api/generated/types.gen"
import { Meter } from "@/components/meter"
import { dimmed, meterTone, toneColors } from "@/lib/meter-tone"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

import {
  amountOf,
  diskPercent,
  gib,
  machineProblem,
  machineState,
  memoryNotLimited,
  memoryPercent,
  stateNames,
  tempIsolationOff,
  usedOfTotal,
  type MachineState,
} from "./machines-state"
import { OnCard } from "./on-card"

// What the Machines page's cells are made of, after Beszel's systems-table (MIT, Copyright (c) 2024 henrygd,
// https://github.com/henrygd/beszel; internal/site/src/components/systems-table/systems-table-columns.tsx).

// Beszel's STATUS_COLORS: up, pending (warn), down, paused.
const stateDots: Record<MachineState, string> = {
  online: "bg-done",
  drained: "bg-needs-you",
  offline: "bg-failed",
  creating: "bg-running animate-pulse",
  create_failed: "bg-failed",
  removing: "bg-queued animate-pulse",
  remove_failed: "bg-failed",
  stopping: "bg-queued animate-pulse",
  stopped: "bg-muted-foreground/50",
  starting: "bg-running animate-pulse",
}

// IndicatorDot is Beszel's status dot.
export function IndicatorDot({ node }: { node: Node }) {
  return (
    <span
      className={cn("size-2 shrink-0 rounded-full", stateDots[machineState(node)])}
      aria-hidden
    />
  )
}

// MemoryCell is the memory in use of the machine's total, and, for a node whose workers run without a memory
// ceiling, why (issue #1313).
export function MemoryCell({ node }: { node: Node }) {
  const value = memoryPercent(node)
  const notLimited = memoryNotLimited(node)
  if (value === undefined && notLimited === undefined) {
    return <span className="text-muted-foreground">—</span>
  }

  return (
    <div className="min-w-0 space-y-1 text-xs">
      {value !== undefined && (
        <WithAmount used={node.stats?.memory_used_bytes} total={node.system?.memory_bytes}>
          <Meter
            label="Memory"
            value={value}
            connected={node.connected}
            title={usedOfTotal(node.stats?.memory_used_bytes, node.system?.memory_bytes)}
          />
        </WithAmount>
      )}
      {notLimited !== undefined && (
        <div className="truncate text-needs-you" title={notLimited}>
          Memory not limited: {notLimited}
        </div>
      )}
    </div>
  )
}

export function DiskCell({ node }: { node: Node }) {
  const value = diskPercent(node)
  const { disk } = node
  const free = disk?.free_bytes
  const blocked = disk !== undefined && free !== undefined && free < disk.floor_bytes
  const trimming = disk !== undefined && free !== undefined && free < disk.trim_bytes
  const age = disk?.age_ms
  const isolationOff = tempIsolationOff(node)
  const tone = !disk
    ? undefined
    : free === undefined
      ? "off"
      : blocked
        ? "crit"
        : trimming
          ? "warn"
          : "good"

  return (
    <div className="min-w-0 space-y-1 text-xs">
      {value !== undefined && (
        <WithAmount used={node.stats?.disk_used_bytes} total={node.system?.disk_bytes}>
          <Meter
            label="Disk"
            value={value}
            connected={node.connected}
            tone={tone}
            title={usedOfTotal(node.stats?.disk_used_bytes, node.system?.disk_bytes)}
          />
        </WithAmount>
      )}
      <div
        className={cn("font-medium", blocked ? "text-destructive" : trimming && "text-needs-you")}
      >
        {free === undefined ? "Not measured" : `${gib(free)} free`}
        {blocked ? " · Takes no new work" : trimming ? " · Low disk" : ""}
      </div>
      {disk && disk.floor_bytes > 0 && (
        <div className="text-muted-foreground" title={disk.at}>
          Floor {gib(disk.floor_bytes)} · Trim {gib(disk.trim_bytes)}
          {age !== undefined && ` · ${Math.floor(age / 1000)}s ago`}
        </div>
      )}
      {isolationOff !== undefined && (
        <div className="truncate text-needs-you" title={isolationOff}>
          Temp isolation off: {isolationOff}
        </div>
      )}
    </div>
  )
}

// StateText says the machine's state, and for an offline one how long ago it was seen.
export function StateText({ node }: { node: Node }) {
  const state = machineState(node)

  return (
    <span className="text-xs font-normal whitespace-nowrap text-muted-foreground">
      {stateNames[state]}
      {state === "offline" &&
        (node.last_seen_at ? ` · seen ${since(node.last_seen_at)}` : " · never seen")}
    </span>
  )
}

// MachineProblem says why a provider's machine was not created or not destroyed; nothing for any other machine.
export function MachineProblem({ node, className }: { node: Node; className?: string }) {
  const problem = machineProblem(node)
  if (!problem) return null

  return (
    <span className={cn("text-xs font-normal text-destructive", className)} title={problem}>
      {problem}
    </span>
  )
}

// LoadDot colours the load average by the load against the machine's CPUs, as Beszel's load cell.
export function LoadDot({ percent, connected }: { percent: number; connected: boolean }) {
  return (
    <span
      className={cn(
        "me-0.5 inline-block size-2 rounded-full",
        connected ? toneColors[meterTone(percent)] : dimmed,
      )}
      aria-hidden
    />
  )
}

// GpuRows is one line a GPU: its index when the machine has several, its meter, and what `after` gives.
export function GpuRows({
  node,
  children,
}: {
  node: Node
  children: (gpu: NodeGpu) => { meter: ReactNode; after?: ReactNode }
}) {
  const gpus = node.stats?.gpus ?? []
  if (gpus.length === 0) return <span className="text-muted-foreground">—</span>

  return (
    <span className="flex w-full min-w-0 flex-col gap-1.5 py-1">
      {gpus.map((gpu) => {
        const { meter, after } = children(gpu)

        return (
          <span key={gpu.index} className={rowWithAmount}>
            {gpus.length > 1 && (
              <span className="w-2 shrink-0 text-xs text-muted-foreground tabular-nums">
                {gpu.index}
              </span>
            )}
            <span className="min-w-22 flex-1">{meter}</span>
            {after}
          </span>
        )
      })}
    </span>
  )
}

// rowWithAmount is a meter's row with what comes after it: on one line while the meter keeps 5.5rem, else what comes
// after goes under it, at the end.
const rowWithAmount = "flex w-full min-w-0 flex-wrap items-center justify-end gap-x-2 gap-y-0.5"

// WithAmount is a meter with its Amount after it.
function WithAmount({
  used,
  total,
  children,
}: {
  used: number | undefined
  total: number | undefined
  children: ReactNode
}) {
  return (
    <span className={rowWithAmount}>
      <span className="min-w-22 flex-1">{children}</span>
      <Amount used={used} total={total} />
    </span>
  )
}

// Amount is how much is used of how much, after a meter on a card (issue #563), as Coder's agent metadata reads
// `coder stat mem` and `coder stat disk` (used/total GiB). Its slot has one width, so the bars of a card line up.
// The table keeps it in the meter's title, as Beszel's table keeps its numbers to the system's page.
export function Amount({ used, total }: { used: number | undefined; total: number | undefined }) {
  const onCard = useContext(OnCard)
  const amount = amountOf(used, total)
  if (!onCard || amount === undefined) return null

  return (
    <span className="w-[6.5rem] shrink-0 text-end text-xs whitespace-nowrap text-muted-foreground tabular-nums">
      {amount}
    </span>
  )
}
