import { Link } from "@tanstack/react-router"
import { Clapperboard, Film, Globe, Search, Smartphone } from "lucide-react"
import { useDeferredValue, useState } from "react"

import type { StudioItem } from "@/api/generated/types.gen"
import { PageHeader, PageHeading } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { Spinner } from "@/components/ui/spinner"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { clock, sourceName } from "@/features/takes/take-state"
import { since } from "@/lib/time"
import { cn } from "@/lib/utils"

import {
  filming,
  groupNames,
  groups,
  inProgress,
  stateName,
  stateTone,
  useStudio,
  type Group,
  type Kind,
} from "./studio-state"

type Layout = "grid" | "table"

const layoutKey = "hypomnema.studio.layout"

function readLayout(): Layout {
  try {
    return window.localStorage.getItem(layoutKey) === "table" ? "table" : "grid"
  } catch {
    return "grid"
  }
}

// StudioScreen is the Studio (issue #818, row 12): the takes agents filmed of the phone or the browser (issue #1189)
// and the videos made of them, in one library, built like the Machines page (machines-screen.tsx, after Beszel's systems-table): a Grid of cards
// or a Table, chosen with the same segmented control, here with the kind and state filters as chips (the
// Repositories page's, ../settings/repositories-screen.tsx) and a search of titles and Bots, all made by the server
// before it cuts a page (GET /studio/items), as Cap's My Caps pages its caps (caps-page-data.tsx; layout learned,
// nothing copied). A card shows the first frame, the kind and the state, the length, the Bot and when; a take
// filmed now shows a red dot in its place. A card opens the take or the video on its own page.
export function StudioScreen() {
  const [kind, setKind] = useState<Kind | undefined>()
  const [group, setGroup] = useState<Group | undefined>()
  const [query, setQuery] = useState("")
  const q = useDeferredValue(query.trim())
  const studio = useStudio({ kind, state: group, q })
  const [layout, setLayout] = useState<Layout>(readLayout)
  const items = studio.data?.pages.flatMap((p) => p.data) ?? []
  const filtered = kind !== undefined || group !== undefined || q !== ""

  function choose(next: Layout) {
    setLayout(next)

    try {
      window.localStorage.setItem(layoutKey, next)
    } catch {
      // Kept until the page closes.
    }
  }

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader />

      <div className="min-h-0 flex-1 overflow-y-auto px-3 pt-1 pb-8 sm:px-5">
        <PageHeading
          title="Studio"
          description="What agents filmed of the phone or the browser, and the videos made of it. Open a take to make a video; open a video to review it."
          className="mb-4"
        >
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
        </PageHeading>
        <div className="mb-4 flex flex-col gap-3">
          <InputGroup>
            <InputGroupAddon>
              <Search />
            </InputGroupAddon>
            <InputGroupInput
              type="search"
              aria-label="Search titles and Bots"
              placeholder="Search titles and Bots…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
          </InputGroup>
          <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
            <ToggleGroup
              aria-label="Kind"
              value={[kind ?? "all"]}
              onValueChange={(next: string[]) =>
                next[0] && setKind(next[0] === "all" ? undefined : (next[0] as Kind))
              }
              variant="choice"
              size="sm"
              spacing={1}
            >
              <ToggleGroupItem value="all">All</ToggleGroupItem>
              <ToggleGroupItem value="take">Takes</ToggleGroupItem>
              <ToggleGroupItem value="video">Videos</ToggleGroupItem>
            </ToggleGroup>
            <ToggleGroup
              aria-label="State"
              value={[group ?? "all"]}
              onValueChange={(next: string[]) =>
                next[0] && setGroup(next[0] === "all" ? undefined : (next[0] as Group))
              }
              variant="choice"
              size="sm"
              spacing={1}
            >
              <ToggleGroupItem value="all">Any state</ToggleGroupItem>
              {groups.map((g) => (
                <ToggleGroupItem key={g} value={g}>
                  {groupNames[g]}
                </ToggleGroupItem>
              ))}
            </ToggleGroup>
          </div>
        </div>
        {studio.error ? (
          <p className="py-10 text-center text-sm text-destructive">
            The Studio could not be read.
          </p>
        ) : studio.isPending ? (
          <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
        ) : items.length === 0 ? (
          <p className="mx-auto max-w-sm py-16 text-center text-sm text-muted-foreground">
            {filtered
              ? "Nothing matches."
              : "Nothing yet. When an agent films the phone or a page of the browser with its video tools, the take shows here."}
          </p>
        ) : layout === "table" ? (
          <StudioTable items={items} />
        ) : (
          <div className="grid grid-cols-2 gap-3 sm:gap-4 md:grid-cols-3 xl:grid-cols-4">
            {items.map((item) => (
              <StudioCard key={`${item.kind}:${item.id}`} item={item} />
            ))}
          </div>
        )}
        {studio.hasNextPage && (
          <div className="flex justify-center pt-6">
            <Button
              variant="outline"
              disabled={studio.isFetchingNextPage}
              onClick={() => void studio.fetchNextPage()}
            >
              {studio.isFetchingNextPage && <Spinner data-icon="inline-start" />}
              Show more
            </Button>
          </div>
        )}
      </div>
    </main>
  )
}

// itemLink is where an item opens: the take's or the video's own page.
function ItemLink({
  item,
  className,
  children,
}: {
  item: StudioItem
  className?: string
  children: React.ReactNode
}) {
  return item.kind === "take" ? (
    <Link to="/studio/takes/$takeId" params={{ takeId: item.id }} className={className}>
      {children}
    </Link>
  ) : (
    <Link to="/studio/videos/$videoId" params={{ videoId: item.id }} className={className}>
      {children}
    </Link>
  )
}

function KindIcon({ kind, className }: { kind: Kind; className?: string }) {
  return kind === "take" ? (
    <Clapperboard className={className} aria-hidden />
  ) : (
    <Film className={className} aria-hidden />
  )
}

// SourceIcon is where a take, or a video's take, was filmed (issue #1189): the phone or the browser.
function SourceIcon({ source, className }: { source: "phone" | "browser"; className?: string }) {
  return source === "browser" ? (
    <Globe className={className} aria-hidden />
  ) : (
    <Smartphone className={className} aria-hidden />
  )
}

// StateChip is an item's state, in the app's status tones; a state that still changes turns.
function StateChip({ item }: { item: StudioItem }) {
  return (
    <Badge variant={stateTone(item.kind, item.state)}>
      {inProgress(item) && !filming(item) && <Spinner />}
      {stateName(item.kind, item.state)}
    </Badge>
  )
}

// Thumb is an item's first frame; a take filmed now, or an item with no frame yet, a placeholder.
function Thumb({ item }: { item: StudioItem }) {
  const [broken, setBroken] = useState(false)

  return (
    <div className="relative aspect-[4/5] overflow-hidden rounded-t-xl bg-muted">
      {item.thumbnail && !broken ? (
        <img
          src={item.thumbnail}
          alt=""
          loading="lazy"
          onError={() => setBroken(true)}
          className="size-full bg-black object-contain"
        />
      ) : (
        <div className="flex size-full items-center justify-center">
          <KindIcon kind={item.kind} className="size-8 text-muted-foreground/60" />
        </div>
      )}
      {filming(item) && (
        <span
          className="absolute top-2 left-2 flex items-center gap-1 rounded-full bg-background/90 px-2 py-0.5 text-2xs font-semibold"
          data-testid="filming-dot"
        >
          <span
            className={cn(
              "size-2 rounded-full bg-destructive",
              item.state === "recording" && "animate-pulse",
            )}
            aria-hidden
          />
          {item.state === "recording" ? "REC" : "PAUSED"}
        </span>
      )}
      {item.length_ms > 0 && (
        <span className="absolute right-2 bottom-2 rounded bg-black/75 px-1.5 py-0.5 text-2xs font-medium text-white tabular-nums">
          {clock(item.length_ms)}
        </span>
      )}
    </div>
  )
}

// StudioCard is a take or a video as a card, as Cap's cap card is: the picture, then the title and what it is.
function StudioCard({ item }: { item: StudioItem }) {
  return (
    <article
      data-testid="studio-item"
      data-id={`${item.kind}:${item.id}`}
      aria-label={item.title}
      className="relative flex flex-col rounded-xl border bg-card text-card-foreground transition-colors hover:border-foreground/30"
    >
      <Thumb item={item} />
      <div className="flex min-w-0 flex-col gap-1.5 p-2.5 sm:p-3">
        <h2 className="truncate text-sm font-semibold">
          <ItemLink item={item} className="outline-none after:absolute after:inset-0">
            {item.title}
          </ItemLink>
        </h2>
        <div className="flex flex-wrap items-center gap-1">
          <Badge variant="outline">
            <KindIcon kind={item.kind} />
            {item.kind === "take" ? "Take" : `Video${item.rev ? ` · rev ${item.rev}` : ""}`}
          </Badge>
          {item.source && (
            <Badge variant="outline">
              <SourceIcon source={item.source} />
              {sourceName(item.source)}
            </Badge>
          )}
          <StateChip item={item} />
        </div>
        <p className="truncate text-xs text-muted-foreground">
          {item.bot} · {since(item.created_at)}
        </p>
      </div>
    </article>
  )
}

// StudioTable is the same items as rows, as the Machines page's table.
function StudioTable({ items }: { items: StudioItem[] }) {
  return (
    <div className="relative max-w-full overflow-auto rounded-lg border">
      <table className="w-full text-sm">
        <thead className="border-b text-muted-foreground">
          <tr>
            {["Title", "Kind", "State", "Length", "Bot", "Made"].map((h) => (
              <th
                key={h}
                scope="col"
                className="h-10 px-3 text-start text-xs font-medium whitespace-nowrap"
              >
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {items.map((item) => (
            <tr
              key={`${item.kind}:${item.id}`}
              data-testid="studio-item"
              className="border-b last:border-b-0"
            >
              <td className="h-12 max-w-64 px-3">
                <ItemLink item={item} className="block truncate font-medium hover:underline">
                  {item.title}
                </ItemLink>
              </td>
              <td className="px-3 whitespace-nowrap">
                <span className="flex items-center gap-1.5">
                  <KindIcon kind={item.kind} className="size-3.5 text-muted-foreground" />
                  {item.kind === "take" ? "Take" : `Video · rev ${item.rev ?? 1}`}
                  {item.source && (
                    <span className="text-muted-foreground">· {sourceName(item.source)}</span>
                  )}
                </span>
              </td>
              <td className="px-3">
                <StateChip item={item} />
              </td>
              <td className="px-3 text-muted-foreground tabular-nums">
                {item.length_ms > 0 ? clock(item.length_ms) : "—"}
              </td>
              <td className="max-w-48 truncate px-3 text-muted-foreground">{item.bot}</td>
              <td className="px-3 whitespace-nowrap text-muted-foreground">
                {since(item.created_at)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
