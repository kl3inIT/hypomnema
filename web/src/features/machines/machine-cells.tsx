import { Meter as BaseMeter } from "@base-ui/react/meter"

import type { Node } from "@/api/generated/types.gen"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

import {
  decimal,
  machineProblem,
  machineState,
  meterTone,
  stateNames,
  type MachineState,
  type MeterTone,
} from "./machines-state"

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

const toneColors: Record<MeterTone, string> = {
  good: "bg-done",
  warn: "bg-needs-you",
  crit: "bg-failed",
}

// A machine that is not connected shows its last measurement dimmed, as Beszel dims a paused or down system.
const dimmed = "bg-muted-foreground/40"

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

// Meter is Beszel's TableCellWithMeter on Base UI's Meter: the number on the left and a bar on the right that is
// green, then amber from 65 %, then red from 90 %.
export function Meter({
  label,
  value,
  connected,
  title,
}: {
  label: string
  value: number
  connected: boolean
  title?: string
}) {
  const tone = meterTone(value)

  return (
    <BaseMeter.Root
      value={value}
      aria-label={label}
      data-tone={connected ? tone : "off"}
      title={title}
      className="flex w-full min-w-0 items-center gap-2 tracking-tight tabular-nums"
    >
      <span className="min-w-8 shrink-0">{decimal(value)}%</span>
      <BaseMeter.Track className="block h-[1em] min-w-8 flex-1 overflow-hidden rounded-sm bg-muted">
        <BaseMeter.Indicator
          className={cn("block h-full", connected ? toneColors[tone] : dimmed)}
        />
      </BaseMeter.Track>
    </BaseMeter.Root>
  )
}
