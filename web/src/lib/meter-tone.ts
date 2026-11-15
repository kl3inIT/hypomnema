// A meter's tone, after Beszel's systems-table (MIT, Copyright (c) 2024 henrygd, https://github.com/henrygd/beszel;
// internal/site/src/components/systems-table/systems-table-columns.tsx). The Meter and the Machines page's load dot
// are coloured by it.

export type MeterTone = "good" | "warn" | "crit"

// meterTone is Beszel's getMeterStateByThresholds (systems-table-columns.tsx): a meter warns from 65 % and is
// critical from 90 %.
export function meterTone(value: number, warn = 65, crit = 90): MeterTone {
  return value >= crit ? "crit" : value >= warn ? "warn" : "good"
}

export const toneColors: Record<MeterTone, string> = {
  good: "bg-done",
  warn: "bg-needs-you",
  crit: "bg-failed",
}

// A machine that is not connected shows its last measurement dimmed, as Beszel dims a paused or down system.
export const dimmed = "bg-muted-foreground/40"
