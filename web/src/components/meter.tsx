import { Meter as BaseMeter } from "@base-ui/react/meter"

import { cn } from "@/lib/utils"

// A meter where a value is a share, after Beszel's systems-table (MIT, Copyright (c) 2024 henrygd,
// https://github.com/henrygd/beszel; internal/site/src/components/systems-table/systems-table-columns.tsx).

export type MeterTone = "good" | "warn" | "crit"

// meterTone is Beszel's getMeterStateByThresholds (systems-table-columns.tsx): a meter warns from 65 % and is
// critical from 90 %.
export function meterTone(value: number, warn = 65, crit = 90): MeterTone {
  return value >= crit ? "crit" : value >= warn ? "warn" : "good"
}

// decimal is Beszel's rule for a meter's number: one decimal from 10, two below.
function decimal(value: number) {
  return value.toFixed(value >= 10 ? 1 : 2)
}

export const toneColors: Record<MeterTone, string> = {
  good: "bg-done",
  warn: "bg-needs-you",
  crit: "bg-failed",
}

// A machine that is not connected shows its last measurement dimmed, as Beszel dims a paused or down system.
export const dimmed = "bg-muted-foreground/40"

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
