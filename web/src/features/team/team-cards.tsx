import { memo } from "react"

import type { Node } from "@/api/generated/types.gen"
import { AgentAvatar } from "@/features/agents/agent-avatar"
import { ModelMark } from "@/features/agents/model-mark"
import { RuntimeMark } from "@/features/agents/runtime-mark"
import { modelLine } from "@/features/bots/bots-state"
import { IndicatorDot, StateText } from "@/features/machines/machine-cells"
import { machineState } from "@/features/machines/machines-state"
import { elapsed } from "@/lib/time"
import { cn } from "@/lib/utils"

import {
  CARD_HEIGHT,
  CARD_WIDTH,
  LINE_HEIGHT,
  MACHINE_HEAD,
  MACHINE_WIDTH,
  machineHeight,
} from "./team-layout"
import { teamMapStatus, type MachineLine, type TeamBot, type TeamMapStatus } from "./team-map"

// The Team page's cards. A Bot's card and a machine's line of work are pressed to open something, so they are
// their own design rather than a Button (docs/conventions.md, web client: what is its own design stays its own).

const toneDots: Record<TeamMapStatus["tone"], string> = {
  warning: "bg-needs-you",
  danger: "bg-failed",
  running: "bg-running",
  idle: "bg-muted-foreground/40",
}

// BotCard is adapted from OpenMausBot's BotCard (Apache License 2.0, https://github.com/milind-soni/OpenMausBot,
// src/components/TeamCanvas.tsx): the face, the name, the model with its maker's mark, and the status as a dot
// and words, none when idle. Changed: the whole card opens the Bot's chat, OpenMausBot's chat button, since the
// Bot's settings have their own page; the model is not a button; a line under the status says why (issue #262).
export const BotCard = memo(function BotCard({
  bot,
  x,
  y,
  faded,
  onOpen,
}: {
  bot: TeamBot
  x: number
  y: number
  faded: boolean
  onOpen: (bot: TeamBot) => void
}) {
  const status = teamMapStatus(bot)

  return (
    <button
      type="button"
      data-testid="team-bot"
      aria-label={`${bot.name}${status.label ? `, ${status.label}` : ""}. Open its chat`}
      onClick={() => onOpen(bot)}
      className={cn(
        "absolute flex flex-col gap-2.5 rounded-xl border bg-card p-3 text-left text-card-foreground shadow-xs transition-[opacity,border-color] outline-none hover:border-border-strong focus-visible:ring-2 focus-visible:ring-ring",
        faded && "opacity-35",
      )}
      style={{ left: x, top: y, width: CARD_WIDTH, height: CARD_HEIGHT }}
    >
      <span className="flex min-w-0 items-center gap-2.5">
        <AgentAvatar name={bot.name} className="size-9 shrink-0" />
        <span className="flex min-w-0 flex-col gap-0.5">
          <span className="truncate text-sm font-semibold">{bot.name}</span>
          <span className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
            <ModelMark name={bot.model ?? ""} className="size-3.5 shrink-0" />
            <span className="truncate">{modelLine(bot)}</span>
          </span>
        </span>
      </span>
      {status.label && (
        <span className="flex min-w-0 flex-col gap-0.5">
          <span className="flex items-center gap-2 text-xs font-medium">
            <span
              className={cn("size-2 shrink-0 rounded-full", toneDots[status.tone])}
              aria-hidden
            />
            {status.label}
          </span>
          {bot.reason && (
            <span className="truncate ps-4 text-xs text-muted-foreground">{bot.reason}</span>
          )}
        </span>
      )}
    </button>
  )
})

// MachineCard is adapted from OpenMausBot's ComputerNode (Apache License 2.0, src/components/ComputerNode.tsx) and
// the Machines page's card (features/machines, after Beszel): the state dot and the name in the head, then, where
// OpenMausBot shows a computer's live screen with its activity line over it, a line a live Dispatch: its runtime,
// its Task's first line and what its worker does now. A machine with nothing running says "–".
export const MachineCard = memo(function MachineCard({
  node,
  lines,
  x,
  y,
  faded,
  selected,
  now,
  onOpenTask,
}: {
  node: Node
  lines: MachineLine[]
  x: number
  y: number
  faded: boolean
  selected?: string
  now: number
  onOpenTask: (taskId: string) => void
}) {
  const online = machineState(node) === "online"

  return (
    <article
      data-testid="team-machine"
      data-node-id={node.id}
      aria-label={node.name}
      className={cn(
        "absolute overflow-hidden rounded-xl border bg-card text-card-foreground shadow-xs transition-opacity",
        faded && "opacity-35",
        !node.connected && "text-foreground/70",
      )}
      style={{ left: x, top: y, width: MACHINE_WIDTH, height: machineHeight(lines.length) }}
    >
      <header
        className="flex items-center gap-2 border-b border-border/60 bg-muted/30 ps-3.5 pe-3"
        style={{ height: MACHINE_HEAD }}
      >
        <h3 className="flex min-w-0 flex-1 items-center gap-2.5 text-sm font-semibold">
          <IndicatorDot node={node} />
          <span className="truncate">{node.name}</span>
        </h3>
        {online ? (
          <span className="text-xs whitespace-nowrap text-muted-foreground tabular-nums">
            {node.active} of {node.max_dispatches}
          </span>
        ) : (
          <StateText node={node} />
        )}
      </header>
      {lines.length === 0 ? (
        <p
          className="flex items-center px-3.5 text-sm text-muted-foreground"
          style={{ height: 36 }}
        >
          –
        </p>
      ) : (
        lines.map((line) => (
          <WorkLine
            key={line.taskId}
            line={line}
            now={now}
            selected={selected === line.taskId}
            onOpen={onOpenTask}
          />
        ))
      )}
    </article>
  )
})

const statusWords: Partial<Record<MachineLine["status"], string>> = {
  starting: "Starting",
  running: "Running",
  stopping: "Stopping",
}

// WorkLine is one live Dispatch on a machine; it opens its Task's panel (OpenMausBot's onOpenComputer, here the
// Task as Omnigent opens a session over its canvas).
function WorkLine({
  line,
  now,
  selected,
  onOpen,
}: {
  line: MachineLine
  now: number
  selected: boolean
  onOpen: (taskId: string) => void
}) {
  return (
    <button
      type="button"
      data-testid="team-work"
      onClick={() => onOpen(line.taskId)}
      aria-current={selected || undefined}
      className="flex w-full flex-col justify-center gap-1 border-b border-border/60 px-3.5 text-left outline-none last:border-b-0 hover:bg-accent/50 focus-visible:bg-accent/60 aria-current:bg-accent/60"
      style={{ height: LINE_HEIGHT }}
    >
      <span className="flex min-w-0 items-center gap-2 text-sm">
        <RuntimeMark runtime={line.runtime} className="size-4.5" />
        <span className="truncate">{line.title}</span>
      </span>
      {line.waitingSince ? (
        <span className="flex items-center gap-1.5 ps-6.5 text-xs text-needs-you">
          <span className="size-1.5 shrink-0 rounded-full bg-needs-you" aria-hidden />
          Waiting for an answer · {elapsed(line.waitingSince, now)}
        </span>
      ) : line.activity ? (
        <span className="truncate ps-6.5 font-mono text-xs text-done">{line.activity}</span>
      ) : (
        <span className="ps-6.5 text-xs text-muted-foreground">
          {statusWords[line.status] ?? "–"}
        </span>
      )}
    </button>
  )
}
