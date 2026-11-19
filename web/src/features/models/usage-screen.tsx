import { ChevronLeft, ChevronRight, ChartColumn, Info } from "lucide-react"
import { useState } from "react"
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts"

import "@/api/client"
import type {
  GatewayPerformance,
  GatewayRequest,
  GatewayUsage,
  GatewayUsageGroup,
} from "@/api/generated/types.gen"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  type ChartConfig,
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
} from "@/components/ui/chart"
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty"
import { Label } from "@/components/ui/label"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { ProviderMark } from "@/components/icons/model-mark"
import { RequestMark } from "@/components/icons/provider-marks"
import { ErrorAlert } from "@/components/error-alert"
import { SettingsPage } from "@/components/settings-page"
import { modelName } from "@/lib/model-name"

import {
  bars,
  bucketWord,
  cacheShare,
  cost,
  duration,
  money,
  ms,
  outcome,
  percent,
  periods,
  providerKind,
  rate,
  requestPeriod,
  runtimeKind,
  share,
  speed,
  successTone,
  tiles,
  tokens,
  units,
  unitsOf,
  type Period,
  type RequestPeriod,
} from "./usage-state"
import { useGatewayRequests, useGatewayUsage } from "./use-models"

const pageSize = 50

// The chart's three stacks, bottom first, in the app's chart colors (index.css), light to dark.
const chartConfig = {
  uncached: { label: "In, other", color: "var(--chart-1)" },
  cached: { label: "In, from cache", color: "var(--chart-3)" },
  output: { label: "Out", color: "var(--chart-5)" },
} satisfies ChartConfig

// UsageScreen is what the model gateway served (issue #199, step 4): a period, its totals, the tokens a bar an
// hour or a day, a table by model, key or account, and the requests themselves. The period and the totals are
// T3 Code's and Omnara's (UsagePage.tsx, UsageReport.tsx), the stacked bars LiteLLM's (StackedUsageChart.tsx, MIT),
// the tables by model, key and account 9router's (UsageStats.js, MIT), and the requests a log with a status badge, a
// page at a time and a drawer with a failed request's message, as LiteLLM's (RequestLogsTableColumns.tsx,
// LogDetailsDrawer) and 9router's (RequestDetailsTab.js). Neither a prompt nor an answer is kept, so none is shown.
// Each row leads with its provider's or its model's mark, says what it used that is not tokens in its own unit, and
// a failed one its error under its outcome (issue #723, decisions 3.2 to 3.6). The Performance table compares the
// text models' providers and accounts by success rate, time to first token, latency and tokens a second (issue
// #856), as new-api's perf metrics, 9router-go's usage by provider and OmniRoute's speed ranking; 90 days and 12
// months are read from the daily ledger, which outlives the requests, as LiteLLM's daily spend.
export function UsageScreen() {
  const [period, setPeriod] = useState<Period>("24h")
  const usage = useGatewayUsage(period)

  return (
    <SettingsPage
      wide
      title="Usage"
      description="What the model gateway served: requests, tokens, web searches, audio and images, what they would cost at the API, how fast and how reliably each provider answered, and the requests that failed."
      actions={
        <ToggleGroup
          aria-label="Period"
          value={[period]}
          onValueChange={(next: string[]) => next[0] && setPeriod(next[0] as Period)}
          variant="choice"
          size="sm"
          spacing={1}
        >
          {periods.map((p) => (
            <ToggleGroupItem key={p.value} value={p.value}>
              {p.label}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
      }
    >
      {usage.isPending && (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner />
          Loading…
        </p>
      )}
      <ErrorAlert error={usage.error} />
      {usage.data && usage.data.totals.requests === 0 && (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <ChartColumn />
            </EmptyMedia>
            <EmptyTitle>Nothing served in this period</EmptyTitle>
            <EmptyDescription>
              Requests to the gateway, from workers, Bots or a tool with a gateway key, show here.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}
      {usage.data && usage.data.totals.requests > 0 && (
        <>
          <Totals usage={usage.data} />
          {usage.data.totals.estimated_requests > 0 && (
            <p className="text-sm text-muted-foreground">
              <Tooltip>
                <TooltipTrigger>
                  <span className="inline-flex cursor-help items-center gap-1.5">
                    <span>
                      {usage.data.totals.estimated_requests.toLocaleString()}{" "}
                      {usage.data.totals.estimated_requests === 1 ? "request" : "requests"}{" "}
                      estimated, {tokens(usage.data.totals.estimated_prompt_tokens)} input tokens
                    </span>
                    <Info aria-hidden="true" className="size-3.5 shrink-0" />
                  </span>
                </TooltipTrigger>
                <TooltipContent>
                  <span className="block max-w-64 text-pretty">
                    When the provider leaves out token counts, tokens are estimated from the request
                    or response size. Estimates are included in totals, but not in the cache share.
                  </span>
                </TooltipContent>
              </Tooltip>
            </p>
          )}
          {usage.data.ledger && (
            <p className="text-sm text-muted-foreground">
              Read from the daily totals, kept after the requests themselves are deleted at 31 days:
              days are UTC days, your keys count together, and the speed is an average.
            </p>
          )}
          <TokensChart usage={usage.data} />
          <Breakdown usage={usage.data} />
          <Performance usage={usage.data} />
          <RequestsOf period={period} />
        </>
      )}
    </SettingsPage>
  )
}

function Totals({ usage }: { usage: GatewayUsage }) {
  return (
    <dl
      aria-label="Totals"
      className="flex flex-wrap gap-px overflow-hidden rounded-xl border bg-border"
    >
      {tiles(usage.totals).map((t) => (
        <div key={t.label} className="flex min-w-36 flex-1 flex-col gap-1 bg-card p-4">
          <dt className="text-sm text-muted-foreground">{t.label}</dt>
          <dd className="flex flex-wrap items-baseline gap-x-2">
            <span className="text-2xl font-semibold tabular-nums">{t.value}</span>
            {t.detail && <span className="text-xs text-muted-foreground">{t.detail}</span>}
          </dd>
        </div>
      ))}
    </dl>
  )
}

function TokensChart({ usage }: { usage: GatewayUsage }) {
  const data = bars(usage)

  return (
    <section
      aria-labelledby="usage-tokens"
      className="flex flex-col gap-3 rounded-xl border bg-card p-4"
    >
      <h2 id="usage-tokens" className="text-sm font-medium">
        Tokens {bucketWord(usage)}
      </h2>
      <div className="h-64 w-full [&>[data-slot=chart]]:aspect-auto [&>[data-slot=chart]]:h-full">
        <ChartContainer config={chartConfig}>
          <BarChart data={data} margin={{ left: 0, right: 8 }} barCategoryGap="15%" maxBarSize={48}>
            <CartesianGrid vertical={false} />
            <XAxis dataKey="label" tickLine={false} axisLine={false} minTickGap={24} />
            <YAxis
              tickLine={false}
              axisLine={false}
              width={48}
              tickFormatter={(value) => tokens(Number(value))}
            />
            <ChartTooltip
              content={
                <ChartTooltipContent
                  formatter={(value, name, item) => (
                    <div className="flex w-full items-center gap-2">
                      <span
                        className="size-2.5 shrink-0 rounded-[2px]"
                        style={{ backgroundColor: item.color }}
                      />
                      <span className="min-w-0 flex-1 text-muted-foreground">
                        {chartConfig[name as keyof typeof chartConfig]?.label ?? name}
                      </span>
                      <span className="ms-3 font-medium tabular-nums">{tokens(Number(value))}</span>
                    </div>
                  )}
                />
              }
            />
            <ChartLegend content={<ChartLegendContent />} />
            {(["uncached", "cached", "output"] as const).map((key) => (
              <Bar
                key={key}
                dataKey={key}
                stackId="tokens"
                fill={`var(--color-${key})`}
                isAnimationActive={false}
              />
            ))}
          </BarChart>
        </ChartContainer>
      </div>
    </section>
  )
}

function Breakdown({ usage }: { usage: GatewayUsage }) {
  return (
    <Tabs defaultValue="model">
      <TabsList aria-label="Group by">
        <TabsTrigger value="model">By model</TabsTrigger>
        <TabsTrigger value="key">By Bot and key</TabsTrigger>
        <TabsTrigger value="account">By account</TabsTrigger>
      </TabsList>
      <TabsContent value="model">
        <GroupTable
          label="Usage by model"
          head="Model"
          groups={usage.by_model}
          total={usage.totals.prompt_tokens + usage.totals.completion_tokens}
          name={(g) => (
            <span className="inline-flex items-center gap-2" title={g.name}>
              <RequestMark model={g.name} provider="" className="size-4 shrink-0" />
              <span className="font-medium">{modelName(g.name)}</span>
            </span>
          )}
        />
      </TabsContent>
      <TabsContent value="key">
        <GroupTable
          label="Usage by Bot and key"
          head="Bot or key"
          groups={usage.by_key}
          total={usage.totals.prompt_tokens + usage.totals.completion_tokens}
          name={(g) => <span className="font-medium">{g.name}</span>}
        />
      </TabsContent>
      <TabsContent value="account">
        <GroupTable
          label="Usage by account"
          head="Account"
          groups={usage.by_account}
          total={usage.totals.prompt_tokens + usage.totals.completion_tokens}
          empty="No request in this period went to a subscription account."
          name={(g) => (
            <span className="inline-flex items-center gap-2">
              <ProviderMark kind={runtimeKind(g.runtime)} className="size-4 shrink-0" />
              <span className="font-medium">{g.name || "A removed account"}</span>
            </span>
          )}
        />
      </TabsContent>
    </Tabs>
  )
}

function GroupTable({
  label,
  head,
  groups,
  total,
  name,
  empty,
}: {
  label: string
  head: string
  groups: GatewayUsageGroup[]
  total: number
  name: (g: GatewayUsageGroup) => React.ReactNode
  empty?: string
}) {
  return (
    <div className="min-w-0 overflow-x-auto rounded-xl border bg-card">
      <Table aria-label={label}>
        <TableHeader>
          <TableRow>
            <TableHead>{head}</TableHead>
            <TableHead>
              <span className="block text-end">Requests</span>
            </TableHead>
            <TableHead>
              <span className="block text-end">Failed</span>
            </TableHead>
            <TableHead>
              <span className="block text-end">In</span>
            </TableHead>
            <TableHead>
              <span className="block text-end">From cache</span>
            </TableHead>
            <TableHead>
              <span className="block text-end">Out</span>
            </TableHead>
            <TableHead>
              <span className="block text-end">Reasoning</span>
            </TableHead>
            <TableHead>Other use</TableHead>
            <TableHead>
              <span className="block text-end">API cost</span>
            </TableHead>
            <TableHead>
              <span className="block text-end">Share</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {groups.map((g) => (
            <TableRow key={g.id} aria-label={g.name || g.id}>
              <TableCell>{name(g)}</TableCell>
              <TableCell>
                <span className="block text-end tabular-nums">
                  {g.counts.requests.toLocaleString()}
                </span>
              </TableCell>
              <TableCell>
                <span className="block text-end tabular-nums">
                  {g.counts.failed > 0 ? (
                    <span className="text-destructive">{g.counts.failed.toLocaleString()}</span>
                  ) : (
                    <span className="text-muted-foreground">0</span>
                  )}
                </span>
              </TableCell>
              <TableCell>
                <span className="block text-end tabular-nums">
                  {tokens(g.counts.prompt_tokens)}
                </span>
              </TableCell>
              <TableCell>
                <span className="block text-end text-muted-foreground tabular-nums">
                  {tokens(g.counts.cached_tokens)}
                </span>
                <span className="block text-end text-xs text-muted-foreground">
                  {cacheShare(g.counts)}
                </span>
              </TableCell>
              <TableCell>
                <span className="block text-end tabular-nums">
                  {tokens(g.counts.completion_tokens)}
                </span>
              </TableCell>
              <TableCell>
                <span className="block text-end text-muted-foreground tabular-nums">
                  {tokens(g.counts.reasoning_tokens)}
                </span>
              </TableCell>
              <TableCell>
                <span className="block whitespace-nowrap tabular-nums">{unitsOf(g.counts)}</span>
              </TableCell>
              <TableCell>
                <span className="block text-end tabular-nums">{cost(g.counts)}</span>
              </TableCell>
              <TableCell>
                <span className="block text-end text-muted-foreground tabular-nums">
                  {share(g.counts.prompt_tokens + g.counts.completion_tokens, total)}
                </span>
              </TableCell>
            </TableRow>
          ))}
          {groups.length === 0 && (
            <TableRow>
              <TableCell colSpan={10}>
                <p className="py-6 text-center text-muted-foreground">{empty ?? "Nothing yet."}</p>
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
    </div>
  )
}

const timeFormat = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
})

// RequestsOf lists the requests of a period they are still kept for, and says why a longer one has none.
function RequestsOf({ period }: { period: Period }) {
  const kept = requestPeriod(period)

  if (kept) return <Requests period={kept} />

  return (
    <section aria-labelledby="usage-requests" className="flex flex-col gap-2">
      <h2 id="usage-requests" className="text-base font-semibold">
        Requests
      </h2>
      <p className="text-sm text-muted-foreground">
        Each request is kept 31 days. Choose 30 days or less to see them.
      </p>
    </section>
  )
}

function Requests({ period }: { period: RequestPeriod }) {
  const [failedOnly, setFailedOnly] = useState(false)
  const [offset, setOffset] = useState(0)
  const [open, setOpen] = useState<GatewayRequest | null>(null)
  const requests = useGatewayRequests(period, failedOnly, offset, pageSize)
  const rows = requests.data?.data ?? []

  return (
    <section aria-labelledby="usage-requests" className="flex min-w-0 flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 id="usage-requests" className="text-base font-semibold">
          Requests
        </h2>
        <div className="flex items-center gap-2">
          <Switch
            id="failed-only"
            checked={failedOnly}
            onCheckedChange={(checked: boolean) => {
              setFailedOnly(checked)
              setOffset(0)
            }}
          />
          <Label htmlFor="failed-only">Failed only</Label>
        </div>
      </div>
      <ErrorAlert error={requests.error} />
      <div className="min-w-0 overflow-x-auto rounded-xl border bg-card">
        <Table aria-label="Requests">
          <TableHeader>
            <TableRow>
              <TableHead>Time</TableHead>
              <TableHead>Model</TableHead>
              <TableHead>Served by</TableHead>
              <TableHead>Key</TableHead>
              <TableHead>Outcome</TableHead>
              <TableHead>
                <span className="block text-end">In</span>
              </TableHead>
              <TableHead>
                <span className="block text-end">From cache</span>
              </TableHead>
              <TableHead>
                <span className="block text-end">Out</span>
              </TableHead>
              <TableHead>
                <span className="block text-end">API cost</span>
              </TableHead>
              <TableHead>
                <span className="block text-end">Took</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => {
              const o = outcome(r.outcome)

              return (
                <TableRow
                  key={r.id}
                  aria-label={`${modelName(r.model)} at ${timeFormat.format(new Date(r.created_at))}`}
                  tabIndex={0}
                  onClick={() => setOpen(r)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault()
                      setOpen(r)
                    }
                  }}
                >
                  <TableCell>
                    <span className="block whitespace-nowrap text-muted-foreground">
                      <time dateTime={r.created_at}>
                        {timeFormat.format(new Date(r.created_at))}
                      </time>
                    </span>
                  </TableCell>
                  <TableCell>
                    <span className="flex flex-col gap-0.5">
                      <span
                        className="inline-flex items-center gap-2 whitespace-nowrap"
                        title={r.model}
                      >
                        <RequestMark
                          model={r.model}
                          provider={r.provider}
                          className="size-4 shrink-0"
                        />
                        {modelName(r.model)}
                      </span>
                      {units(r) && (
                        <span className="ps-6 text-xs whitespace-nowrap text-muted-foreground tabular-nums">
                          {units(r)}
                        </span>
                      )}
                    </span>
                  </TableCell>
                  <TableCell>
                    <ServedBy request={r} />
                  </TableCell>
                  <TableCell>
                    <span className="block whitespace-nowrap">{r.key_name}</span>
                  </TableCell>
                  <TableCell>
                    <span className="flex flex-col items-start gap-1">
                      <Badge variant={o.tone}>{o.label}</Badge>
                      {r.error && (
                        <span
                          className="block max-w-48 truncate text-xs text-destructive"
                          title={r.error}
                        >
                          {r.error}
                        </span>
                      )}
                    </span>
                  </TableCell>
                  <TableCell>
                    <span className="block text-end tabular-nums">{tokens(r.prompt_tokens)}</span>
                  </TableCell>
                  <TableCell>
                    <span className="block text-end text-muted-foreground tabular-nums">
                      {tokens(r.cached_tokens)}
                    </span>
                  </TableCell>
                  <TableCell>
                    <span className="block text-end tabular-nums">
                      {tokens(r.completion_tokens)}
                    </span>
                  </TableCell>
                  <TableCell>
                    <span className="block text-end text-muted-foreground tabular-nums">
                      {r.cost_micros === undefined ? "—" : money(r.cost_micros)}
                    </span>
                  </TableCell>
                  <TableCell>
                    <span className="block text-end whitespace-nowrap text-muted-foreground tabular-nums">
                      {duration(r.duration_ms)}
                    </span>
                  </TableCell>
                </TableRow>
              )
            })}
            {requests.data && rows.length === 0 && (
              <TableRow>
                <TableCell colSpan={10}>
                  <p className="py-6 text-center text-muted-foreground">
                    {failedOnly
                      ? "No request failed in this period."
                      : "No request in this period."}
                  </p>
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
      {(offset > 0 || requests.data?.has_more) && (
        <div className="flex items-center justify-end gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={offset === 0}
            onClick={() => setOffset(Math.max(offset - pageSize, 0))}
          >
            <ChevronLeft data-icon="inline-start" />
            Newer
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={!requests.data?.has_more}
            onClick={() => setOffset(offset + pageSize)}
          >
            Older
            <ChevronRight data-icon="inline-end" />
          </Button>
        </div>
      )}
      <RequestSheet request={open} onClose={() => setOpen(null)} />
    </section>
  )
}

// ServedBy is where a request went: the subscription account, or the provider and its model.
function ServedBy({ request }: { request: GatewayRequest }) {
  const kind = providerKind(request.provider)

  return (
    <span className="inline-flex items-center gap-2 whitespace-nowrap">
      {kind && <ProviderMark kind={kind} className="size-4 shrink-0" />}
      <span>{request.account_label ?? request.provider}</span>
    </span>
  )
}

function RequestSheet({
  request,
  onClose,
}: {
  request: GatewayRequest | null
  onClose: () => void
}) {
  const o = request ? outcome(request.outcome) : null

  return (
    <Sheet open={request !== null} onOpenChange={(next: boolean) => !next && onClose()}>
      <SheetContent>
        {request && o && (
          <>
            <SheetHeader>
              <SheetTitle>
                <span className="flex items-center gap-2">
                  <RequestMark
                    model={request.model}
                    provider={request.provider}
                    className="size-4 shrink-0"
                  />
                  {modelName(request.model)}
                </span>
              </SheetTitle>
              <SheetDescription>
                {timeFormat.format(new Date(request.created_at))} ·{" "}
                <Badge variant={o.tone}>{o.label}</Badge>
              </SheetDescription>
            </SheetHeader>
            <div className="flex flex-col gap-4 overflow-y-auto px-4 pb-4">
              {request.error && (
                <div
                  role="alert"
                  className="rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm"
                >
                  <p className="font-medium text-destructive">
                    {request.status > 0 ? `The provider answered ${request.status}` : o.label}
                  </p>
                  <p className="mt-1 break-words whitespace-pre-wrap">{request.error}</p>
                </div>
              )}
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
                <Detail label="Asked for" value={request.model} mono />
                <Detail label="Provider" value={request.provider} />
                <Detail label="Its model" value={request.upstream_model} mono />
                {request.account_label && <Detail label="Account" value={request.account_label} />}
                <Detail label="Key" value={request.key_name} />
                <Detail
                  label="Status"
                  value={request.status > 0 ? String(request.status) : "None"}
                />
                <Detail
                  label="Targets tried"
                  value={request.attempts === 1 ? "1" : `${request.attempts}, the last served`}
                />
                <Detail label="Stream" value={request.stream ? "Yes" : "No"} />
                <Detail label="Took" value={duration(request.duration_ms)} />
                {request.ttft_ms !== undefined && (
                  <Detail label="First token" value={duration(request.ttft_ms)} />
                )}
                {request.tokens_per_second !== undefined && (
                  <Detail
                    label="Speed"
                    value={`${rate(request.tokens_per_second)}, after the first token`}
                  />
                )}
                <Detail label="Tokens in" value={request.prompt_tokens.toLocaleString()} />
                <Detail label="From cache" value={request.cached_tokens.toLocaleString()} />
                <Detail label="Tokens out" value={request.completion_tokens.toLocaleString()} />
                <Detail label="Reasoning" value={request.reasoning_tokens.toLocaleString()} />
                {units(request) && <Detail label="Other use" value={units(request)} />}
                <Detail
                  label="Written to cache"
                  value={
                    request.cache_write_1h_tokens > 0
                      ? `${request.cache_write_tokens.toLocaleString()}, ${request.cache_write_1h_tokens.toLocaleString()} for an hour`
                      : request.cache_write_tokens.toLocaleString()
                  }
                />
                <Detail
                  label="API cost"
                  value={
                    request.cost_micros === undefined
                      ? request.prompt_tokens + request.completion_tokens === 0 && !units(request)
                        ? "None: nothing priced was used"
                        : "No price for this model or provider"
                      : `${money(request.cost_micros)} as ${request.priced_model ?? request.upstream_model}`
                  }
                />
              </dl>
              {request.estimated && (
                <p className="text-sm text-muted-foreground">
                  The provider reported no usage: the counts are the sizes divided by four.
                </p>
              )}
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}

function Detail({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={mono ? "font-mono text-xs break-all" : "break-words"}>{value}</dd>
    </>
  )
}

// Performance compares the text models' providers and accounts (issue #856): how many requests, how many were
// answered, the time to the first token and of the whole call (p50, with p95 under it), the median tokens a
// second after the first token, and the cost. A client that left counts neither way.
function Performance({ usage }: { usage: GatewayUsage }) {
  return (
    <section aria-labelledby="usage-performance" className="flex min-w-0 flex-col gap-3">
      <div className="flex flex-col gap-1">
        <h2 id="usage-performance" className="text-base font-semibold">
          Performance
        </h2>
        <p className="text-sm text-muted-foreground">
          {usage.ledger
            ? "How fast and how reliably the text models answered, on average."
            : "How fast and how reliably the text models answered: the median, and under it the slowest 5%."}
        </p>
      </div>
      <Tabs defaultValue="provider">
        <TabsList aria-label="Compare by">
          <TabsTrigger value="provider">By provider and model</TabsTrigger>
          <TabsTrigger value="account">By subscription account</TabsTrigger>
        </TabsList>
        <TabsContent value="provider">
          <PerformanceTable
            label="Performance by provider and model"
            head="Provider and model"
            rows={usage.by_provider}
            empty="No text model answered in this period."
            name={(p) => (
              <span className="inline-flex items-center gap-2" title={`${p.provider} · ${p.model}`}>
                <RequestMark model={p.model} provider={p.provider} className="size-4 shrink-0" />
                <span className="flex min-w-0 flex-col">
                  <span className="font-medium">{modelName(p.model)}</span>
                  <span className="text-xs text-muted-foreground">{p.provider}</span>
                </span>
              </span>
            )}
          />
        </TabsContent>
        <TabsContent value="account">
          <PerformanceTable
            label="Performance by account"
            head="Account"
            rows={usage.speed_by_account}
            empty="No request in this period went to a subscription account."
            name={(p) => (
              <span className="inline-flex items-center gap-2">
                <ProviderMark kind={runtimeKind(p.runtime)} className="size-4 shrink-0" />
                <span className="font-medium">{p.account_label ?? "A removed account"}</span>
              </span>
            )}
          />
        </TabsContent>
      </Tabs>
    </section>
  )
}

function PerformanceTable({
  label,
  head,
  rows,
  name,
  empty,
}: {
  label: string
  head: string
  rows: GatewayPerformance[]
  name: (p: GatewayPerformance) => React.ReactNode
  empty: string
}) {
  return (
    <div className="min-w-0 overflow-x-auto rounded-xl border bg-card">
      <Table aria-label={label}>
        <TableHeader>
          <TableRow>
            <TableHead>{head}</TableHead>
            <TableHead>
              <span className="block text-end">Requests</span>
            </TableHead>
            <TableHead>
              <span className="block text-end">Answered</span>
            </TableHead>
            <TableHead>
              <span className="block text-end">First token</span>
            </TableHead>
            <TableHead>
              <span className="block text-end">Whole call</span>
            </TableHead>
            <TableHead>
              <span className="block text-end">Speed</span>
            </TableHead>
            <TableHead>
              <span className="block text-end">API cost</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((p) => {
            const s = speed(p)

            return (
              <TableRow
                key={`${p.provider}/${p.model}/${p.account_id ?? ""}`}
                aria-label={p.account_label ?? (p.model ? `${p.provider} ${p.model}` : p.provider)}
              >
                <TableCell>{name(p)}</TableCell>
                <TableCell>
                  <span className="block text-end tabular-nums">{p.requests.toLocaleString()}</span>
                </TableCell>
                <TableCell>
                  <span className="flex flex-col items-end gap-0.5">
                    <Badge variant={successTone(p.success_rate)}>{percent(p.success_rate)}</Badge>
                    {p.failed > 0 && (
                      <span className="text-xs whitespace-nowrap text-destructive tabular-nums">
                        {p.failed.toLocaleString()} failed
                      </span>
                    )}
                  </span>
                </TableCell>
                <TableCell>
                  <span className="flex flex-col items-end tabular-nums">
                    <span>{s.ttft}</span>
                    {p.exact && (
                      <span className="text-xs whitespace-nowrap text-muted-foreground">
                        p95 {ms(p.ttft_p95_ms)}
                      </span>
                    )}
                  </span>
                </TableCell>
                <TableCell>
                  <span className="flex flex-col items-end tabular-nums">
                    <span>{s.latency}</span>
                    {p.exact && (
                      <span className="text-xs whitespace-nowrap text-muted-foreground">
                        p95 {ms(p.latency_p95_ms)}
                      </span>
                    )}
                  </span>
                </TableCell>
                <TableCell>
                  <span className="block text-end whitespace-nowrap tabular-nums">{s.rate}</span>
                </TableCell>
                <TableCell>
                  <span className="block text-end text-muted-foreground tabular-nums">
                    {money(p.cost_micros)}
                  </span>
                </TableCell>
              </TableRow>
            )
          })}
          {rows.length === 0 && (
            <TableRow>
              <TableCell colSpan={7}>
                <p className="py-6 text-center text-muted-foreground">{empty}</p>
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
    </div>
  )
}
