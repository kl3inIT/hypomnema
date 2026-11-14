import { useMutation } from "@tanstack/react-query"

import "@/api/client"
import { compactChat } from "@/api/generated/sdk.gen"
import type { ChatContextUsage } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import {
  Popover,
  PopoverContent,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from "@/components/ui/popover"
import { errorText } from "@/features/agents/profiles-state"
import { cn } from "@/lib/utils"

import { meterLabel, meterLine, meterOf, type Meter } from "./context-meter-state"

// The chat's context meter in the composer's footer (issue #384): a ring that fills as pi's context does, and on
// hover or tap a card with "used / window (percent)", where pi compacts on its own, and Compact. The ring is
// adapted from assistant-ui's ContextDisplay (MIT, Copyright (c) 2026 AgentbaseAI Inc.,
// https://github.com/assistant-ui/assistant-ui; packages/ui/src/components/react/assistant-ui/elements/
// context-display.radix.tsx, RingVisual) and AI Elements' ContextIcon (Apache License 2.0,
// https://github.com/vercel/ai-elements; packages/elements/src/context.tsx), which opens its card on hover; the
// tones and the tick at auto-compaction are t3code's ThreadDetailsContext (MIT, Copyright (c) 2026 T3 Tools Inc.,
// apps/web/src/components/chat/ThreadDetailsUsage.tsx). Compact is t3code's (ChatView.tsx onCompactContext,
// disabled while the thread works) and OpenMausBot's "compact now" (Apache License 2.0; src/components/
// ContextWarning.tsx: "Compacting…" while it is asked, disabled while the Bot is busy). No meter shows before pi
// reported one, as in Orca.
export function ContextMeter({
  chatId,
  usage,
  working,
}: {
  chatId: string
  usage: ChatContextUsage | undefined
  // working is pi working on a turn of the chat, a compaction included: Compact waits for its end.
  working: boolean
}) {
  const compact = useMutation({
    mutationFn: () => compactChat({ path: { chat_id: chatId } }),
  })
  const meter = meterOf(usage)
  if (!meter) return null

  return (
    <Popover>
      <PopoverTrigger
        openOnHover
        delay={150}
        render={
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label={meterLabel(meter)}
            data-tone={meter.tone}
          />
        }
      >
        <Ring meter={meter} />
      </PopoverTrigger>
      <PopoverContent side="top" align="end">
        <PopoverHeader>
          <PopoverTitle>Context window</PopoverTitle>
          <p
            className={cn(
              "font-mono text-sm text-foreground tabular-nums",
              meter.tone === "warning" && "text-needs-you",
              meter.tone === "critical" && "text-destructive",
            )}
          >
            {meterLine(meter)}
          </p>
        </PopoverHeader>
        <p className="text-xs text-muted-foreground">
          {meter.used === null
            ? "Compacted: pi measures it again with its next answer."
            : meter.autoCompact === null
              ? "pi compacts on its own when it is full."
              : `pi compacts on its own at about ${meter.autoCompact.toLocaleString("en-US")} tokens.`}
        </p>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={working || compact.isPending}
          title={working ? "pi is working; compact when it is done" : undefined}
          onClick={() => compact.mutate()}
        >
          {compact.isPending ? "Compacting…" : "Compact"}
        </Button>
        {compact.error && (
          <p role="alert" className="text-xs text-destructive">
            Not compacted: {errorText(compact.error)}
          </p>
        )}
      </PopoverContent>
    </Popover>
  )
}

const size = 18
const stroke = 2.5
const radius = (size - stroke) / 2
const circumference = 2 * Math.PI * radius

// Ring is the meter drawn as a ring from the top, clockwise; without a measure only its track and a dash show.
function Ring({ meter }: { meter: Meter }) {
  const filled = meter.percent ?? 0
  // The tick crosses the ring where pi compacts; the drawing is turned a quarter, so 0 is at the top.
  const angle = meter.tick === null ? null : (meter.tick / 100) * 2 * Math.PI
  const c = size / 2

  return (
    <span className="relative inline-flex size-4.5 items-center justify-center">
      <svg
        aria-hidden="true"
        width={size}
        height={size}
        viewBox={`0 0 ${size} ${size}`}
        className="size-4.5 -rotate-90"
      >
        <circle
          cx={c}
          cy={c}
          r={radius}
          fill="none"
          strokeWidth={stroke}
          className="stroke-border"
        />
        {meter.percent !== null && (
          <circle
            cx={c}
            cy={c}
            r={radius}
            fill="none"
            strokeWidth={stroke}
            strokeLinecap="round"
            strokeDasharray={circumference}
            strokeDashoffset={circumference - (filled / 100) * circumference}
            className={cn(
              "transition-[stroke-dashoffset,stroke] duration-300",
              meter.tone === "normal" && "stroke-foreground/70",
              meter.tone === "warning" && "stroke-needs-you",
              meter.tone === "critical" && "stroke-destructive",
            )}
          />
        )}
        {angle !== null && (
          <line
            x1={c + (radius - stroke) * Math.cos(angle)}
            y1={c + (radius - stroke) * Math.sin(angle)}
            x2={c + (radius + stroke / 2) * Math.cos(angle)}
            y2={c + (radius + stroke / 2) * Math.sin(angle)}
            strokeWidth={1.25}
            className="stroke-muted-foreground"
          />
        )}
      </svg>
      {meter.percent === null && (
        <span aria-hidden="true" className="absolute text-2xs leading-none text-muted-foreground">
          –
        </span>
      )}
    </span>
  )
}
