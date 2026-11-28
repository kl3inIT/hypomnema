import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import {
  ArrowDown,
  ArrowUp,
  CalendarCheck,
  Clock,
  ExternalLink,
  Inbox,
  Power,
  RotateCw,
  TrendingDown,
  type LucideIcon,
} from "lucide-react"
import { useState, type ReactNode } from "react"

import "@/api/client"
import {
  getListeningOptions,
  getListeningQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import { setListeningOrder, setListeningSource, testListeningSource } from "@/api/generated/sdk.gen"
import type { Listening, ListeningGroup, ListeningSource } from "@/api/generated/types.gen"
import { ErrorAlert } from "@/components/error-alert"
import { SettingsPage } from "@/components/settings-page"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { said } from "@/lib/said"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

import {
  backendName,
  backendState,
  codeLabel,
  failureShown,
  groupInfo,
  groupOrder,
  itemsLine,
  moved,
  needsYou,
  readLine,
  shownSources,
} from "./listening-state"

type Layout = "grid" | "table"

const layoutKey = "hypomnema.listening.layout"

function readLayout(): Layout {
  try {
    return window.localStorage.getItem(layoutKey) === "table" ? "table" : "grid"
  } catch {
    return "grid"
  }
}

// ListeningScreen is Settings → Gateway → Social listening (issue #1290): where agents read what people say. It
// starts from the Machines page (machines-screen.tsx): a Grid of cards or a Table, chosen with its segmented control
// and kept; and from the Web search and Muse pages for a row's switch, its order and its state badges. At the top,
// last30days' doctor's four groups, counted (lib/doctor.py:97-117), each pressed to show only its sources, as the
// Machines page's status filter. A card is a source: its backends in the order a search tries them, its last
// success, its items this week and its last error with the one step that fixes it (harken's source_metrics,
// BettaFish's crawling tasks, last30days' prescriptions.py). A broken source wears postiz's "!" badge, and a press
// anywhere on its card tests it again (launches.component.tsx, refreshNeeded: "Channel disconnected, click to
// reconnect"). A source switched off says what it would add (last30days quality_nudge.py).
export function ListeningScreen() {
  const listening = useQuery({ ...getListeningOptions(), refetchInterval: 30_000 })
  const [layout, setLayout] = useState<Layout>(readLayout)
  const [group, setGroup] = useState<ListeningGroup | null>(null)

  function choose(next: Layout) {
    setLayout(next)

    try {
      window.localStorage.setItem(layoutKey, next)
    } catch {
      // Kept until the page closes.
    }
  }

  return (
    <SettingsPage
      title="Social listening"
      description="Where agents read what people say: each source, how it last read, and its backends in the order a search tries them."
      wide
      actions={
        <fieldset className="m-0 inline-flex gap-0.5 rounded-lg border border-solid bg-muted p-0.5">
          <legend className="sr-only">Layout</legend>
          {(["grid", "table"] as const).map((l) => (
            <button
              key={l}
              type="button"
              aria-pressed={layout === l}
              onClick={() => choose(l)}
              className="rounded-md px-2.5 py-1 text-xs text-muted-foreground capitalize aria-pressed:bg-foreground/9 aria-pressed:text-foreground"
            >
              {l}
            </button>
          ))}
        </fieldset>
      }
    >
      <ErrorAlert error={listening.error} />
      {listening.isPending ? (
        <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
      ) : (
        listening.data && (
          <>
            <Counts listening={listening.data} chosen={group} onChoose={setGroup} />
            <Sources listening={listening.data} group={group} layout={layout} />
          </>
        )
      )}
    </SettingsPage>
  )
}

const dots: Record<ListeningGroup, string> = {
  working: "bg-done",
  unverified: "bg-needs-you",
  broken: "bg-failed",
  could_be_on: "bg-muted-foreground/50",
}

// Counts are the four groups, each with its number; pressing one shows only its sources, pressing it again all.
function Counts({
  listening,
  chosen,
  onChoose,
}: {
  listening: Listening
  chosen: ListeningGroup | null
  onChoose: (group: ListeningGroup | null) => void
}) {
  return (
    <fieldset className="m-0 grid min-w-0 grid-cols-2 gap-2 border-0 p-0 sm:grid-cols-4">
      <legend className="sr-only">Sources by state</legend>
      {groupOrder.map((g) => {
        const info = groupInfo(g)
        const n = listening.counts[g]

        return (
          <button
            key={g}
            type="button"
            aria-pressed={chosen === g}
            title={info.why}
            onClick={() => onChoose(chosen === g ? null : g)}
            className="flex flex-col items-start gap-1 rounded-xl border bg-card px-3.5 py-2.5 text-start hover:bg-muted/50 aria-pressed:border-ring aria-pressed:bg-muted"
          >
            <span className="flex items-center gap-2 text-xs text-muted-foreground">
              <span className={cn("size-2 rounded-full", dots[g])} aria-hidden />
              {info.label}
            </span>
            <span className="text-2xl leading-none font-semibold tabular-nums">{n}</span>
          </button>
        )
      })}
    </fieldset>
  )
}

function Sources({
  listening,
  group,
  layout,
}: {
  listening: Listening
  group: ListeningGroup | null
  layout: Layout
}) {
  const sources = shownSources(listening, group)

  if (listening.sources.length === 0) {
    return (
      <p className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">
        This control plane has no listening source.
      </p>
    )
  }

  if (sources.length === 0 && group) {
    return (
      <p className="py-12 text-center text-sm text-muted-foreground">
        No source is {groupInfo(group).label.toLowerCase()}.
      </p>
    )
  }

  if (layout === "table") return <SourcesTable sources={sources} />

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      {sources.map((source) => (
        <SourceCard key={source.name} source={source} />
      ))}
    </div>
  )
}

// useShowListening shows the sources the server answered a change with.
function useShowListening() {
  const queryClient = useQueryClient()

  return (listening: Listening) => queryClient.setQueryData(getListeningQueryKey(), listening)
}

function useChange<T>(change: (input: T) => Promise<Listening>, done: (input: T) => string) {
  const show = useShowListening()

  return useMutation({
    mutationFn: change,
    onSuccess: (listening, input) => {
      show(listening)
      said(done(input))
    },
  })
}

function useSwitch(source: ListeningSource) {
  return useChange(
    async (on: boolean) =>
      (await setListeningSource({ path: { source: source.name }, body: { on } })).data,
    (on) => (on ? `${source.label} is on` : `${source.label} is off`),
  )
}

function useTest(source: ListeningSource) {
  return useChange(
    async () => (await testListeningSource({ path: { source: source.name } })).data,
    () => `${source.label} was tested`,
  )
}

// SourceCard is a source, after the Machines page's card: a head with its state, its name and its switch, then its
// facts, its last error with its fix, and its backends.
function SourceCard({ source }: { source: ListeningSource }) {
  const info = groupInfo(source.group)
  const toggle = useSwitch(source)
  const test = useTest(source)
  const broken = needsYou(source)

  return (
    <article
      aria-label={source.label}
      data-group={source.group}
      className={cn(
        "relative flex w-full min-w-0 flex-col overflow-hidden rounded-xl border bg-card text-card-foreground",
        broken && "cursor-pointer border-destructive/40 hover:bg-muted/30",
        !source.on && "opacity-80",
      )}
    >
      <header className="flex items-center gap-2.5 border-b border-border/60 bg-muted/30 py-1.5 ps-4 pe-2">
        <span className="relative flex size-7 shrink-0 items-center justify-center rounded-md border bg-background text-xs font-semibold">
          {source.label.slice(0, 1)}
          {broken && (
            <span
              aria-label="Needs you"
              className="absolute -start-1.5 -top-1.5 flex size-4 items-center justify-center rounded-full bg-destructive text-[10px] font-bold text-white"
            >
              !
            </span>
          )}
        </span>
        <h2 className="min-w-0 flex-1 truncate text-sm font-semibold">{source.label}</h2>
        <Badge variant={info.tone} title={info.why}>
          {info.label}
        </Badge>
        {source.regressed && (
          <Badge variant="destructive" title="It passed on an earlier day and fails today.">
            <TrendingDown aria-hidden />
            Regressed
          </Badge>
        )}
        <span className="relative z-20">
          <InUse source={source} toggle={toggle} />
        </span>
      </header>
      <div className="flex flex-col gap-3 px-4 pt-3 pb-4 text-sm">
        <p className="text-xs text-muted-foreground">
          {source.on ? source.adds : `Switch it on to add: ${source.adds}`}
        </p>
        {source.on && <Facts source={source} />}
        {source.page && (
          <Link
            to={source.page}
            className={cn(
              buttonVariants({ variant: "link", size: "sm" }),
              "relative z-20 self-start px-0",
            )}
          >
            <ExternalLink data-icon="inline-start" />
            Its accounts
          </Link>
        )}
        {source.on && failureShown(source) && <LastError source={source} />}
        {source.on && (broken || source.group === "unverified") && (
          <div className="relative z-20 flex items-center gap-2">
            <Button
              variant={broken ? "default" : "outline"}
              size="sm"
              focusableWhenDisabled={test.isPending}
              disabled={test.isPending}
              onClick={() => test.mutate()}
            >
              {test.isPending ? (
                <Spinner data-icon="inline-start" />
              ) : (
                <RotateCw data-icon="inline-start" />
              )}
              Test again
            </Button>
          </div>
        )}
        <ErrorAlert error={toggle.error ?? test.error} />
        <BackendList source={source} />
      </div>
      {/* The whole card of a broken source tests it again (postiz: "Channel disconnected, click to reconnect"): a
          button laid over the card, under its own controls. */}
      {broken && (
        <button
          type="button"
          aria-label={`Test ${source.label} again`}
          title="Not working: press the card to test it again"
          disabled={test.isPending}
          onClick={() => test.mutate()}
          className="absolute inset-0 z-10 cursor-pointer rounded-xl disabled:cursor-progress"
        />
      )}
    </article>
  )
}

function Facts({ source }: { source: ListeningSource }) {
  return (
    <span className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
      <Fact icon={Clock}>{readLine(source)}</Fact>
      <Fact icon={CalendarCheck}>
        {source.last_success_at
          ? `Last success ${since(source.last_success_at)}`
          : "No success yet"}
      </Fact>
      <Fact icon={Inbox}>{itemsLine(source)}</Fact>
    </span>
  )
}

// LastError is the last error, where and when, and the one step that fixes it (last30days' prescriptions).
function LastError({ source }: { source: ListeningSource }) {
  const error = source.last_error
  if (!error) return null

  return (
    <div
      role="note"
      aria-label="Last error"
      className="flex flex-col gap-1 rounded-lg border border-destructive/25 bg-destructive/5 px-3 py-2 text-xs"
    >
      <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
        <span className="font-medium text-destructive">{codeLabel(error.code)}</span>
        {error.backend && (
          <span className="text-muted-foreground">{backendName(error.backend)}</span>
        )}
        <span className="text-muted-foreground">{since(error.at)}</span>
      </span>
      {error.detail && <span className="break-words text-muted-foreground">{error.detail}</span>}
      <span className="text-foreground">{error.fix}</span>
    </div>
  )
}

// BackendList is the source's backends in the order a search tries them, each with its state and its last answer,
// moved up or down as the Muse page moves an account.
function BackendList({ source }: { source: ListeningSource }) {
  const order = source.backends.map((b) => b.name)
  const reorder = useChange(
    async (backends: string[]) =>
      (await setListeningOrder({ path: { source: source.name }, body: { backends } })).data,
    (backends) => `${backendName(backends[0] ?? "")} is asked first`,
  )

  return (
    <div className="flex flex-col gap-1.5">
      <h3 className="text-xs font-medium text-muted-foreground">
        Backends, in the order a search tries them
      </h3>
      <ol className="flex flex-col gap-1.5" aria-label={`${source.label}'s backends`}>
        {source.backends.map((b, i) => {
          const state = backendState(b)

          return (
            <li
              key={b.name}
              aria-label={backendName(b.name)}
              className="flex items-center gap-2 rounded-md border px-2.5 py-1.5 text-xs"
            >
              <span className="text-muted-foreground tabular-nums">#{i + 1}</span>
              <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                  <span className="truncate font-medium text-foreground">
                    {backendName(b.name)}
                  </span>
                  <Badge variant={state.tone} title={state.why}>
                    {state.label}
                  </Badge>
                  {b.opt_in && <Badge variant="outline">Only when pinned</Badge>}
                </span>
                <span className="text-muted-foreground">
                  {b.last_ok_at ? `Answered ${since(b.last_ok_at)}` : "Not answered yet"}
                  {b.last_error &&
                    ` · ${codeLabel(b.last_error.code)} ${since(b.last_error.at)}${b.last_error.detail ? `: ${b.last_error.detail}` : ""}`}
                </span>
              </span>
              {source.backends.length > 1 && (
                <span className="relative z-20 flex items-center">
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`Move ${backendName(b.name)} up`}
                    disabled={i === 0 || reorder.isPending}
                    onClick={() => reorder.mutate(moved(order, b.name, "up"))}
                  >
                    <ArrowUp />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`Move ${backendName(b.name)} down`}
                    disabled={i === order.length - 1 || reorder.isPending}
                    onClick={() => reorder.mutate(moved(order, b.name, "down"))}
                  >
                    <ArrowDown />
                  </Button>
                </span>
              )}
            </li>
          )
        })}
      </ol>
      <ErrorAlert error={reorder.error} />
    </div>
  )
}

// SourcesTable is the Machines page's Table: one row a source.
function SourcesTable({ sources }: { sources: ListeningSource[] }) {
  return (
    <div className="relative max-w-full overflow-auto rounded-lg border">
      <table className="w-full text-sm">
        <thead className="border-b text-muted-foreground">
          <tr>
            {[
              "Source",
              "State",
              "Last read",
              "Last success",
              "This week",
              "Backends",
              "Last error",
              "In use",
            ].map((name) => (
              <th
                key={name}
                scope="col"
                className="h-10 px-3 text-start text-xs font-medium whitespace-nowrap"
              >
                {name}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {sources.map((source) => (
            <SourceRow key={source.name} source={source} />
          ))}
        </tbody>
      </table>
    </div>
  )
}

function SourceRow({ source }: { source: ListeningSource }) {
  const info = groupInfo(source.group)
  const toggle = useSwitch(source)
  const test = useTest(source)
  const broken = needsYou(source)

  return (
    <tr aria-label={source.label} className="border-b align-top last:border-b-0">
      <td className="px-3 py-2.5 font-medium whitespace-nowrap">
        {broken && (
          <span
            aria-label="Needs you"
            className="me-1.5 inline-flex size-4 items-center justify-center rounded-full bg-destructive text-[10px] font-bold text-white"
          >
            !
          </span>
        )}
        {source.label}
      </td>
      <td className="px-3 py-2.5">
        <span className="flex flex-wrap gap-1">
          <Badge variant={info.tone} title={info.why}>
            {info.label}
          </Badge>
          {source.regressed && <Badge variant="destructive">Regressed</Badge>}
        </span>
      </td>
      <td className="px-3 py-2.5 text-xs whitespace-nowrap text-muted-foreground">
        {readLine(source)}
      </td>
      <td className="px-3 py-2.5 text-xs whitespace-nowrap text-muted-foreground">
        {source.last_success_at ? since(source.last_success_at) : "—"}
      </td>
      <td className="px-3 py-2.5 text-xs tabular-nums">{source.items_this_week}</td>
      <td className="px-3 py-2.5 text-xs">
        <ol className="flex flex-col gap-0.5">
          {source.backends.map((b, i) => (
            <li key={b.name} className="whitespace-nowrap">
              <span className="text-muted-foreground">#{i + 1}</span> {backendName(b.name)}
              {b.state === "resting" && (
                <span className="text-needs-you"> · {backendState(b).label}</span>
              )}
            </li>
          ))}
        </ol>
      </td>
      <td className="max-w-72 px-3 py-2.5 text-xs">
        {source.on && failureShown(source) && source.last_error ? (
          <span className="flex flex-col gap-1">
            <span className="text-destructive">{codeLabel(source.last_error.code)}</span>
            <span className="text-muted-foreground">{source.last_error.fix}</span>
            {(broken || source.group === "unverified") && (
              <span>
                <Button
                  variant="outline"
                  size="sm"
                  focusableWhenDisabled={test.isPending}
                  disabled={test.isPending}
                  onClick={() => test.mutate()}
                >
                  {test.isPending ? (
                    <Spinner data-icon="inline-start" />
                  ) : (
                    <RotateCw data-icon="inline-start" />
                  )}
                  Test again
                </Button>
              </span>
            )}
          </span>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
        <ErrorAlert error={toggle.error ?? test.error} />
      </td>
      <td className="px-3 py-2.5">
        <InUse source={source} toggle={toggle} />
      </td>
    </tr>
  )
}

// InUse is the owner's switch of a source, as the Web search page's.
function InUse({
  source,
  toggle,
}: {
  source: ListeningSource
  toggle: ReturnType<typeof useSwitch>
}) {
  const id = `listen-${source.name}`

  return (
    <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
      <label htmlFor={id} className="inline-flex items-center gap-1 whitespace-nowrap">
        <Power className="size-3" aria-hidden />
        In use
      </label>
      <Switch
        id={id}
        size="sm"
        checked={source.on}
        disabled={toggle.isPending}
        onCheckedChange={(on) => toggle.mutate(on)}
      />
    </span>
  )
}

function Fact({ icon: Icon, children }: { icon: LucideIcon; children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap">
      <Icon className="size-3" aria-hidden />
      {children}
    </span>
  )
}
