import type { ChatContextUsage } from "@/api/generated/types.gen"

// How full a chat's context window is (issue #384), from pi's own report kept with the chat. The tones, the tick and
// the wording follow t3code's ThreadDetailsContext (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code; apps/web/src/components/chat/ThreadDetailsUsage.tsx, lib/contextWindow.ts,
// chat/compaction.logic.ts): amber from 80 %, red from 90 %, a tick where the agent compacts on its own. Orca
// shows no meter before the agent reported one (main/pi/rpc/rpc-context-usage.ts), and pi reports no tokens right
// after a compaction, until its next answer (agent-session.ts getContextUsage).

// compactReserve is what pi keeps free: it compacts on its own once the context passes the window less this
// (pi settings-defaults.ts reserveTokens, compaction.ts shouldCompact).
export const compactReserve = 16384

export type MeterTone = "normal" | "warning" | "critical"

export type Meter = {
  window: number
  // used and percent are null right after a compaction, until pi's next answer measures them.
  used: number | null
  percent: number | null
  tone: MeterTone
  // autoCompact is where pi compacts on its own, in tokens, and tick the same as a share of the window, 0 to 100.
  autoCompact: number | null
  tick: number | null
}

// meterOf is the meter of a chat's usage, or null when pi has not reported one.
export function meterOf(usage: ChatContextUsage | undefined): Meter | null {
  if (!usage || !(usage.context_window > 0)) return null

  const window = usage.context_window
  const autoCompact = window > compactReserve ? window - compactReserve : null
  const tick = autoCompact === null ? null : (autoCompact / window) * 100

  if (usage.tokens === undefined) {
    return { window, used: null, percent: null, tone: "normal", autoCompact, tick }
  }

  const share = usage.percent ?? (usage.tokens / window) * 100
  const percent = Math.round(Math.max(0, Math.min(100, share)))

  return { window, used: usage.tokens, percent, tone: toneOf(percent), autoCompact, tick }
}

function toneOf(percent: number): MeterTone {
  if (percent >= 90) return "critical"
  if (percent >= 80) return "warning"
  return "normal"
}

// formatTokens is a count of tokens as t3code writes it: 950, 1.5k, 51k, 1.2m.
export function formatTokens(value: number): string {
  if (!Number.isFinite(value)) return "0"
  if (value < 1_000) return `${Math.round(value)}`
  if (value < 10_000) return `${(value / 1_000).toFixed(1).replace(/\.0$/, "")}k`
  if (value < 999_500) return `${Math.round(value / 1_000)}k`
  return `${(value / 1_000_000).toFixed(1).replace(/\.0$/, "")}m`
}

// meterLine is "used / window (percent)", with "–" for what pi has not measured since it compacted.
export function meterLine(m: Meter): string {
  if (m.used === null || m.percent === null) return `– / ${formatTokens(m.window)}`
  return `${formatTokens(m.used)} / ${formatTokens(m.window)} (${m.percent}%)`
}

// meterLabel names the meter's button for a screen reader.
export function meterLabel(m: Meter): string {
  if (m.percent === null) return "Context: not measured since it was compacted"
  return `Context: ${m.percent}% used`
}
