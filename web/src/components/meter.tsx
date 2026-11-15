import { Meter as BaseMeter } from "@base-ui/react/meter"

import { dimmed, meterTone, toneColors } from "@/lib/meter-tone"
import { cn } from "@/lib/utils"

// decimal is Beszel's rule for a meter's number: one decimal from 10, two below.
function decimal(value: number) {
  return value.toFixed(value >= 10 ? 1 : 2)
}

// Meter is Beszel's TableCellWithMeter (MIT, Copyright (c) 2024 henrygd, https://github.com/henrygd/beszel;
// internal/site/src/components/systems-table/systems-table-columns.tsx) on Base UI's Meter: the number on the left
// and a bar on the right that is green, then amber from 65 %, then red from 90 %.
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
