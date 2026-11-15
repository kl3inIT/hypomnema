import type { ReactNode } from "react"

import type { Node, NodeGpu } from "@/api/generated/types.gen"
import { dimmed, meterTone, toneColors } from "@/lib/meter-tone"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

import { machineProblem, machineState, stateNames, type MachineState } from "./machines-state"

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
          <span key={gpu.index} className="flex w-full min-w-0 items-center gap-2">
            {gpus.length > 1 && (
              <span className="w-2 shrink-0 text-xs text-muted-foreground tabular-nums">
                {gpu.index}
              </span>
            )}
            {meter}
            {after}
          </span>
        )
      })}
    </span>
  )
}
