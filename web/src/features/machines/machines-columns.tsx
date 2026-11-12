import {
  Bot,
  Cloud,
  Cpu,
  Gpu,
  HardDrive,
  Hourglass,
  ListChecks,
  MemoryStick,
  Server,
  Wifi,
  type LucideIcon,
} from "lucide-react"
import type { ReactNode } from "react"

import type { Node } from "@/api/generated/types.gen"
import { cn } from "@/lib/utils"
import { MachineActions } from "./machine-actions"
import { GpuRows, IndicatorDot, LoadDot, MachineProblem, Meter, StateText } from "./machine-cells"
import { RuntimeList } from "./runtime-list"
import {
  diskPercent,
  gpuTitle,
  loadPercent,
  memoryPercent,
  percent,
  usedOfTotal,
} from "./machines-state"

// The columns of the Machines page, after Beszel's systems-table (MIT, Copyright (c) 2024 henrygd,
// https://github.com/henrygd/beszel; internal/site/src/components/systems-table/systems-table-columns.tsx): one
// definition (an icon, a name and a cell) that the Table layout renders as columns and the Grid layout as a
// card's rows; the table hides a column the owner unticks in its View menu (columns-menu.tsx), as Beszel's. Changed:
// a plain array instead of TanStack Table's ColumnDef, no sorting or filtering; this
// app's status tokens instead of Beszel's colours; the columns a node reports (no GPU, net, temperature, battery,
// Wi-Fi, services, updates or uptime), plus its Dispatches and runtimes.

export type Column = {
  id: string
  name: string
  // hint says what the column measures, for whoever points at its name.
  hint?: string
  Icon?: LucideIcon
  cell: (node: Node) => ReactNode
  // has says whether the machine has this at all. Without it a card leaves the row out, and the table leaves the
  // column out when no machine has it: a machine without a GPU does not get empty GPU rows.
  has?: (node: Node) => boolean
  // enableHiding false keeps the column out of the View menu, as Beszel's name column (TanStack Table's option).
  enableHiding?: false
}

// hiddenColumns are hidden in the table until the owner shows them: with them the table is wider than a 1280 px
// screen beside the sidebar (issue #242). Each is the one least needed to choose a machine or act on it: the load
// average repeats what the CPU meter says, the agent's version and system matter only when updating, and where a
// machine comes from is shown on its card.
export const hiddenColumns = ["load", "agent", "source"] as const

// shownColumns are the columns worth showing for these machines.
export function shownColumns(nodes: Node[]) {
  return columns.filter((column) => !column.has || nodes.some(column.has))
}

const hasGpu = (node: Node) => (node.stats?.gpus?.length ?? 0) > 0

const none = <span className="text-muted-foreground">—</span>

export const columns: Column[] = [
  {
    id: "machine",
    name: "Machine",
    Icon: Server,
    enableHiding: false,
    cell: (node) => (
      <span className="flex items-center gap-2 text-sm font-medium text-nowrap">
        <IndicatorDot node={node} />
        <span className="max-w-48 truncate" title={node.name}>
          {node.name}
        </span>
        <StateText node={node} />
        <MachineProblem node={node} className="max-w-64 truncate" />
      </span>
    ),
  },
  {
    id: "cpu",
    name: "CPU",
    hint: "The share of all CPUs in use since the last heartbeat.",
    Icon: Cpu,
    cell: (node) =>
      node.stats ? (
        <Meter label="CPU" value={node.stats.cpu_percent} connected={node.connected} />
      ) : (
        none
      ),
  },
  {
    id: "memory",
    name: "Memory",
    hint: "Memory in use, of the machine's total.",
    Icon: MemoryStick,
    cell: (node) => {
      const value = memoryPercent(node)
      if (value === undefined) return none

      return (
        <Meter
          label="Memory"
          value={value}
          connected={node.connected}
          title={usedOfTotal(node.stats?.memory_used_bytes, node.system?.memory_bytes)}
        />
      )
    },
  },
  {
    id: "disk",
    name: "Disk",
    hint: "Space used on the volume that holds the worktrees.",
    Icon: HardDrive,
    cell: (node) => {
      const value = diskPercent(node)
      if (value === undefined) return none

      return (
        <Meter
          label="Disk"
          value={value}
          connected={node.connected}
          title={usedOfTotal(node.stats?.disk_used_bytes, node.system?.disk_bytes)}
        />
      )
    },
  },
  {
    id: "gpu",
    name: "GPU",
    Icon: Gpu,
    has: hasGpu,
    cell: (node) => (
      <GpuRows node={node}>
        {(gpu) => ({
          meter: (
            <Meter
              label={`GPU ${gpu.index}`}
              value={gpu.usage_percent}
              connected={node.connected}
              title={gpuTitle(gpu)}
            />
          ),
          after: gpu.temperature_celsius !== undefined && (
            <span
              className={cn(
                "w-9 shrink-0 text-end text-xs whitespace-nowrap tabular-nums",
                node.connected && gpu.temperature_celsius >= 85
                  ? "text-destructive"
                  : "text-muted-foreground",
              )}
            >
              {Math.round(gpu.temperature_celsius)} °C
            </span>
          ),
        })}
      </GpuRows>
    ),
  },
  {
    id: "gpu_memory",
    name: "GPU Mem",
    Icon: MemoryStick,
    has: hasGpu,
    cell: (node) => (
      <GpuRows node={node}>
        {(gpu) => ({
          meter: (
            <Meter
              label={`GPU ${gpu.index} memory`}
              value={percent(gpu.memory_used_bytes, gpu.memory_total_bytes) ?? 0}
              connected={node.connected}
              title={usedOfTotal(gpu.memory_used_bytes, gpu.memory_total_bytes)}
            />
          ),
        })}
      </GpuRows>
    ),
  },
  {
    id: "load",
    name: "Load Avg",
    hint: "The one-minute load average: how many processes are running or waiting for a CPU. Its colour compares it with the number of CPUs.",
    Icon: Hourglass,
    cell: (node) => {
      const load = loadPercent(node)
      if (!node.stats || load === undefined) return none

      return (
        <span className="flex w-full items-center gap-[.35em] tracking-tight tabular-nums">
          <LoadDot percent={load} connected={node.connected} />
          {node.stats.load1.toFixed(node.stats.load1 >= 10 ? 1 : 2)}
        </span>
      )
    },
  },
  {
    id: "dispatches",
    name: "Dispatches",
    hint: "Work running on this machine now, of the most it takes at once.",
    Icon: ListChecks,
    cell: (node) => (
      <span className="whitespace-nowrap tabular-nums">
        {node.active} of {node.max_dispatches}
      </span>
    ),
  },
  {
    id: "runtimes",
    name: "Runtimes",
    hint: "The agent CLIs found on this machine. Work for a runtime goes only to a machine that has it.",
    Icon: Bot,
    cell: (node) => (node.runtimes.length > 0 ? <RuntimeList runtimes={node.runtimes} /> : none),
  },
  {
    id: "agent",
    name: "Agent",
    hint: "The version of the Hypomnema node running on this machine, and its system.",
    Icon: Wifi,
    cell: (node) => {
      const { daemon_version, os, arch } = node.system ?? {}
      const platform = [os, arch].filter(Boolean).join("/")
      const text = [daemon_version, platform].filter(Boolean).join(" · ")

      return text ? <span className="whitespace-nowrap tabular-nums">{text}</span> : none
    },
  },
  {
    id: "source",
    name: "Source",
    hint: "Your own machine, or one created at a provider.",
    Icon: Cloud,
    cell: (node) =>
      node.machine ? (
        <span
          className="flex flex-wrap items-baseline gap-x-1.5"
          title={`Created at a provider: ${node.machine.size} in ${node.machine.region}`}
        >
          <span className="whitespace-nowrap">{node.machine.size}</span>
          <span className="text-xs whitespace-nowrap text-muted-foreground">
            {node.machine.region}
          </span>
        </span>
      ) : (
        <span className="whitespace-nowrap text-muted-foreground">Your own</span>
      ),
  },
  {
    id: "actions",
    name: "Actions",
    cell: (node) => <MachineActions node={node} />,
  },
]
