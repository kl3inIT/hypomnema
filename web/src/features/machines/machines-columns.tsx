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
// card's rows. Changed: a plain array instead of TanStack Table's ColumnDef, no sorting, filtering or hiding; this
// app's status tokens instead of Beszel's colours; the columns a node reports (no GPU, net, temperature, battery,
// Wi-Fi, services, updates or uptime), plus its Dispatches and runtimes.

export type Column = {
  id: string
  name: string
  Icon?: LucideIcon
  cell: (node: Node) => ReactNode
  // has says whether the machine has this at all. Without it a card leaves the row out, and the table leaves the
  // column out when no machine has it: a machine without a GPU does not get empty GPU rows.
  has?: (node: Node) => boolean
}

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
    Icon: Bot,
    cell: (node) =>
      node.runtimes.length > 0 ? (
        <span
          className="truncate"
          title={node.runtimes.map((r) => `${r.runtime} ${r.version}`.trim()).join(", ")}
        >
          {node.runtimes.map((r) => r.runtime).join(", ")}
        </span>
      ) : (
        none
      ),
  },
  {
    id: "agent",
    name: "Agent",
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
