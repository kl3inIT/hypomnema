import type { GatewayRequest, GatewayUsage, GatewayUsageCounts } from "@/api/generated/types.gen"

export type Period = "24h" | "7d" | "30d"

// The periods the page offers, as T3 Code's shortcuts (apps/web/src/components/usage/usageShortcuts.ts).
export const periods: { value: Period; label: string }[] = [
  { value: "24h", label: "24 hours" },
  { value: "7d", label: "7 days" },
  { value: "30d", label: "30 days" },
]

// tokens says a count short: 950, 12.3k, 4.56M.
export function tokens(n: number): string {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${trim(n / 1000)}k`
  if (n < 1_000_000_000) return `${trim(n / 1_000_000)}M`
  return `${trim(n / 1_000_000_000)}B`
}

function trim(n: number): string {
  const fixed = n >= 100 ? n.toFixed(0) : n >= 10 ? n.toFixed(1) : n.toFixed(2)
  // Only the zeros after a decimal point go: 750 stays 750, 1.50 is 1.5.
  return fixed.includes(".") ? fixed.replace(/\.?0+$/, "") : fixed
}

// share is a part of a whole as a percent, "" when there is no whole.
export function share(part: number, whole: number): string {
  if (whole <= 0) return ""
  return `${Math.round((part / whole) * 100)}%`
}

export type Bar = {
  start: string
  label: string
  uncached: number
  cached: number
  output: number
  requests: number
}

// bars are the chart's: the input not read from a cache, the input read from it, and the output, stacked a bar
// an hour or a day, as LiteLLM stacks its usage (components/shared/charts/StackedUsageChart.tsx).
export function bars(usage: GatewayUsage): Bar[] {
  const hourly = usage.bucket_seconds < 86400
  const hour = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" })
  const day = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" })

  return usage.buckets.map((b) => ({
    start: b.start,
    label: (hourly ? hour : day).format(new Date(b.start)),
    uncached: Math.max(b.counts.prompt_tokens - b.counts.cached_tokens, 0),
    cached: b.counts.cached_tokens,
    output: b.counts.completion_tokens,
    requests: b.counts.requests,
  }))
}

export type Tile = { label: string; value: string; detail?: string }

// tiles are the period's totals, as T3 Code's Totals row and Omnara's tiles.
export function tiles(c: GatewayUsageCounts): Tile[] {
  return [
    { label: "Requests", value: c.requests.toLocaleString() },
    {
      label: "Failed",
      value: c.failed.toLocaleString(),
      detail: c.failed > 0 ? share(c.failed, c.requests) : undefined,
    },
    { label: "Tokens in", value: tokens(c.prompt_tokens) },
    {
      label: "From cache",
      value: tokens(c.cached_tokens),
      detail: c.cached_tokens > 0 ? `${share(c.cached_tokens, c.prompt_tokens)} of in` : undefined,
    },
    { label: "Tokens out", value: tokens(c.completion_tokens) },
    { label: "Reasoning", value: tokens(c.reasoning_tokens) },
  ]
}

export type Tone = "success" | "warning" | "destructive" | "secondary"

// outcome says how a request ended, in words and a tone.
export function outcome(o: GatewayRequest["outcome"]): { label: string; tone: Tone } {
  switch (o) {
    case "ok":
      return { label: "OK", tone: "success" }
    case "upstream_error":
      return { label: "Refused", tone: "destructive" }
    case "transport_error":
      return { label: "No answer", tone: "destructive" }
    case "truncated":
      return { label: "Cut short", tone: "warning" }
    case "client_gone":
      return { label: "Client left", tone: "secondary" }
  }
}

// duration says milliseconds short: 640 ms, 2.4 s, 1 min 5 s.
export function duration(ms: number): string {
  if (ms < 1000) return `${ms} ms`
  if (ms < 60_000) return `${trim(ms / 1000)} s`
  const minutes = Math.floor(ms / 60_000)
  const seconds = Math.round((ms % 60_000) / 1000)
  return seconds ? `${minutes} min ${seconds} s` : `${minutes} min`
}

// runtimeKind is the provider kind whose mark an account of a runtime wears.
export function runtimeKind(runtime: string | undefined): string {
  return runtime ? `${runtime}_subscription` : ""
}

// providerKind guesses the kind of a built-in provider from its name, for its mark; an API provider has none.
export function providerKind(provider: string): string {
  if (provider.startsWith("claude")) return "claude_subscription"
  if (provider.startsWith("codex")) return "codex_subscription"
  if (provider.startsWith("antigravity")) return "antigravity_subscription"
  return ""
}
