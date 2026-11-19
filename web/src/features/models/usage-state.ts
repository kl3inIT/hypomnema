import type {
  GatewayPerformance,
  GatewayRequest,
  GatewayUsage,
  GatewayUsageCounts,
  WorkUsage,
} from "@/api/generated/types.gen"

export type Period = "24h" | "7d" | "30d" | "90d" | "12m"

// The periods whose requests are still kept (31 days); a longer one is read from the daily ledger (issue #856).
export type RequestPeriod = "24h" | "7d" | "30d"

// The periods the page offers, as T3 Code's shortcuts (apps/web/src/components/usage/usageShortcuts.ts), and the
// longer ones from the daily ledger, as LiteLLM's usage pages read its daily spend tables.
export const periods: { value: Period; label: string }[] = [
  { value: "24h", label: "24 hours" },
  { value: "7d", label: "7 days" },
  { value: "30d", label: "30 days" },
  { value: "90d", label: "90 days" },
  { value: "12m", label: "12 months" },
]

// requestPeriod is the period whose requests are listed: none for one longer than they are kept.
export function requestPeriod(p: Period): RequestPeriod | undefined {
  return p === "24h" || p === "7d" || p === "30d" ? p : undefined
}

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

export function cacheShare(counts: GatewayUsageCounts): string | undefined {
  const ratio = share(counts.cached_tokens, counts.measured_prompt_tokens)
  return ratio ? `${ratio} of measured input` : undefined
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
  const format = new Intl.DateTimeFormat(
    undefined,
    usage.bucket_seconds < 86400
      ? { hour: "2-digit", minute: "2-digit" }
      : usage.bucket_seconds < 28 * 86400
        ? // The ledger's days are UTC days.
          { month: "short", day: "numeric", timeZone: usage.ledger ? "UTC" : undefined }
        : { month: "short", year: "2-digit", timeZone: "UTC" },
  )

  return usage.buckets.map((b) => ({
    start: b.start,
    label: format.format(new Date(b.start)),
    uncached: Math.max(b.counts.prompt_tokens - b.counts.cached_tokens, 0),
    cached: b.counts.cached_tokens,
    output: b.counts.completion_tokens,
    requests: b.counts.requests,
  }))
}

// money says micro-dollars as dollars: $1,234, $12.34, $0.0123, as 9router's ~Cost and Coder's spend.
export function money(micros: number): string {
  const dollars = micros / 1_000_000
  if (dollars === 0) return "$0"
  if (dollars >= 100) return `$${Math.round(dollars).toLocaleString("en-US")}`
  if (dollars >= 1) return `$${dollars.toFixed(2)}`
  if (dollars >= 0.0001) return `$${dollars.toFixed(4).replace(/0+$/, "")}`
  return "<$0.0001"
}

// cost says a set's cost, and that some of it is unpriced when the price book lacks a model (Coder's
// has_unpriced_usage).
export function cost(c: GatewayUsageCounts): string {
  if (c.cost_micros === 0 && c.unpriced > 0) return "No price"
  return c.unpriced > 0 ? `${money(c.cost_micros)}+` : money(c.cost_micros)
}

// counted says a number with its unit: 1 search, 3 searches, 1,200 characters.
function counted(n: number, one: string, many: string): string {
  return `${n.toLocaleString("en-US")} ${n === 1 ? one : many}`
}

// units says what a request used that is not tokens, each kind in its own unit (issue #723, decision 3.2), as
// OmniRoute bills a second, a character and an image and LiteLLM a query: a search, a page read, the seconds a
// transcription heard, the characters speech spoke, the images made, and a model's own searches beside its
// tokens. Nothing for a model that searched nothing, or a call that failed before any was used.
export function units(
  r: Pick<
    GatewayRequest,
    "model" | "outcome" | "web_searches" | "audio_seconds" | "characters" | "images"
  >,
): string {
  const searches = r.web_searches ?? 0

  if (r.model === "web/search") return searches > 0 ? counted(searches, "search", "searches") : ""
  if (r.model === "web/fetch") return r.outcome === "ok" ? "1 page" : ""
  if (r.model.startsWith("audio/")) {
    if (r.audio_seconds) return `${duration(Math.round(r.audio_seconds * 1000))} heard`
    if (r.characters) return `${counted(r.characters, "character", "characters")} spoken`
    return ""
  }
  if (r.model.startsWith("image/")) return r.images ? counted(r.images, "image", "images") : ""

  return searches > 0 ? `+${counted(searches, "search", "searches")}` : ""
}

// unitsOf says what a set of requests used that is not tokens, each kind that is there in its unit.
export function unitsOf(c: GatewayUsageCounts): string {
  const out: string[] = []

  if (c.web_searches > 0) out.push(counted(c.web_searches, "search", "searches"))
  if (c.audio_seconds > 0) out.push(`${duration(Math.round(c.audio_seconds * 1000))} heard`)
  if (c.characters > 0) out.push(`${counted(c.characters, "character", "characters")} spoken`)
  if (c.images > 0) out.push(counted(c.images, "image", "images"))

  return out.join(", ")
}

export type Tile = { label: string; value: string; detail?: string }

// tiles are the period's totals, as T3 Code's Totals row and Omnara's tiles; the units that are not tokens each
// have a tile when the period used any (issue #723, decision 3.2).
export function tiles(c: GatewayUsageCounts): Tile[] {
  const used: Tile[] = []

  if (c.web_searches > 0)
    used.push({ label: "Web searches", value: c.web_searches.toLocaleString() })
  if (c.audio_seconds > 0) {
    used.push({ label: "Audio heard", value: duration(Math.round(c.audio_seconds * 1000)) })
  }
  if (c.characters > 0) used.push({ label: "Characters spoken", value: tokens(c.characters) })
  if (c.images > 0) used.push({ label: "Images", value: c.images.toLocaleString() })

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
      detail: cacheShare(c),
    },
    { label: "Tokens out", value: tokens(c.completion_tokens) },
    { label: "Reasoning", value: tokens(c.reasoning_tokens) },
    ...used,
    {
      label: "API cost",
      value: cost(c),
      detail: c.unpriced > 0 ? `${c.unpriced.toLocaleString()} without a price` : undefined,
    },
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
  if (runtime === "opencode_go") return "opencode_go"
  return runtime ? `${runtime}_subscription` : ""
}

// providerKind guesses the kind of a built-in provider from its name, for its mark; an API provider has none.
export function providerKind(provider: string): string {
  if (provider.startsWith("claude")) return "claude_subscription"
  if (provider.startsWith("codex")) return "codex_subscription"
  if (provider.startsWith("antigravity")) return "antigravity_subscription"
  if (provider.startsWith("opencode-go")) return "opencode_go"
  return ""
}

// bucketWord says a bar's size: an hour, a day or a month.
export function bucketWord(usage: GatewayUsage): string {
  if (usage.bucket_seconds < 86400) return "an hour"
  if (usage.bucket_seconds < 28 * 86400) return "a day"
  return "a month"
}

// rate says tokens a second short: 84 tok/s, 7.5 tok/s.
export function rate(perSecond: number | undefined): string {
  if (perSecond === undefined) return "—"
  return `${perSecond >= 100 ? Math.round(perSecond) : trim(perSecond)} tok/s`
}

// percent says a share from 0 to 1 as a percent with a decimal under 100: 99.2%, 100%.
export function percent(share: number | undefined): string {
  if (share === undefined) return "—"
  const p = share * 100
  return p >= 99.95 ? "100%" : `${p.toFixed(1)}%`
}

// ms says milliseconds, or a dash when there are none.
export function ms(value: number | undefined): string {
  return value === undefined ? "—" : duration(value)
}

export type Speed = { ttft: string; latency: string; rate: string }

// speed is a row's speed in words: the medians from the requests themselves, the averages from the ledger, which
// keeps sums only (issue #856).
export function speed(p: GatewayPerformance): Speed {
  return p.exact
    ? { ttft: ms(p.ttft_p50_ms), latency: ms(p.latency_p50_ms), rate: rate(p.tokens_per_second) }
    : { ttft: ms(p.ttft_avg_ms), latency: ms(p.latency_avg_ms), rate: rate(p.tokens_per_second) }
}

// successTone says how a success rate reads: good from 99%, a warning from 95%, bad below.
export function successTone(
  share: number | undefined,
): "success" | "warning" | "destructive" | "secondary" {
  if (share === undefined) return "secondary"
  if (share >= 0.99) return "success"
  if (share >= 0.95) return "warning"
  return "destructive"
}

// workUsage says what a Task's or a Run's workers used (issue #856): the gateway's tokens and their API cost, and
// the tokens a runtime that did not go through the gateway reported, which have no cost. Nothing when none was
// counted.
export function workUsage(u: WorkUsage | undefined): string | undefined {
  if (!u) return undefined

  const parts: string[] = []

  if (u.requests > 0) {
    const priced = u.unpriced > 0 ? `${money(u.cost_micros)}+` : money(u.cost_micros)
    parts.push(
      `${priced} at API prices · ${tokens(u.prompt_tokens)} in, ${tokens(u.completion_tokens)} out · ${counted(u.requests, "request", "requests")}`,
    )
  }

  const r = u.reported
  const reportedIn = r.input + r.cache_read + r.cache_write
  if (reportedIn + r.output > 0) {
    parts.push(`${tokens(reportedIn)} in, ${tokens(r.output)} out as its runtime reported`)
  }

  return parts.length > 0 ? parts.join(" · ") : undefined
}
