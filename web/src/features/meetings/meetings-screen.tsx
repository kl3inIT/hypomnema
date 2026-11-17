import { Link } from "@tanstack/react-router"
import { tableFeatures, useTable, type ColumnDef } from "@tanstack/react-table"
import { CalendarClock, ListChecks, Mic, Search, Upload, UsersRound } from "lucide-react"
import { useDeferredValue, useState, type ReactNode } from "react"

import type { Meeting } from "@/api/generated/types.gen"
import { PageHeader, PageHeading } from "@/components/page-header"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { cn } from "@/lib/utils"

import { ActionsTab } from "./actions-tab"
import {
  actionsText,
  byDay,
  fromText,
  speakersText,
  stageNames,
  stageOf,
  whenText,
  type Stage,
} from "./meetings-state"
import { UploadMeeting } from "./upload-meeting"
import { FormatsTab } from "./formats-tab"
import { useMeetingActions, useMeetings } from "./use-meetings"

type Layout = "grid" | "table"

const layoutKey = "hypomnema.meetings.layout"

function readLayout(): Layout {
  try {
    return window.localStorage.getItem(layoutKey) === "table" ? "table" : "grid"
  } catch {
    return "grid"
  }
}

// MeetingsScreen lists the meetings (issue #721), built from the Machines page (machines-screen.tsx, after Beszel's
// systems-table: cards with labelled rows, or a table, chosen with a segmented control), its cards' rows the first
// prototype's (When, Speakers, Actions, From; owner, 2026-11-18), grouped by day as Granola, Otter, Hyprnote and
// MemoryOS list them. A search finds titles and what was said. The Actions tab lists the actions of every meeting,
// as Fireflies's Tasks, Otter and Nojoin do. The Formats tab holds the notes formats, as Hyprnote keeps its templates
// a page of the main sidebar (issue #762).
export function MeetingsScreen() {
  const [tab, setTab] = useState("meetings")
  const open = useMeetingActions("", "open").data?.data.length ?? 0

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pt-1 pb-8 sm:px-5">
        <PageHeading
          title="Meetings"
          description="Recordings turned into transcripts, notes and actions."
          className="mb-4"
        >
          <UploadMeeting />
        </PageHeading>
        <Tabs value={tab} onValueChange={(v) => setTab(String(v))}>
          <TabsList>
            <TabsTrigger value="meetings">Meetings</TabsTrigger>
            <TabsTrigger value="actions">
              Actions{open > 0 && <span className="text-muted-foreground">· {open} open</span>}
            </TabsTrigger>
            <TabsTrigger value="formats">Formats</TabsTrigger>
          </TabsList>
          <TabsContent value="meetings">
            <MeetingsTab />
          </TabsContent>
          <TabsContent value="actions">
            <ActionsTab />
          </TabsContent>
          <TabsContent value="formats">
            <FormatsTab />
          </TabsContent>
        </Tabs>
      </div>
    </main>
  )
}

function MeetingsTab() {
  const [query, setQuery] = useState("")
  const q = useDeferredValue(query.trim())
  const meetings = useMeetings(q)
  const [layout, setLayout] = useState<Layout>(readLayout)
  const all = meetings.data ?? []

  function choose(next: Layout) {
    setLayout(next)

    try {
      window.localStorage.setItem(layoutKey, next)
    } catch {
      // Kept until the page closes.
    }
  }

  if (meetings.isPending) {
    return <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
  }

  if (meetings.error) {
    return (
      <p className="py-10 text-center text-sm text-destructive">The meetings could not be read.</p>
    )
  }

  if (all.length === 0 && !q) return <NoMeetings />

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center gap-2">
        <div className="min-w-0 flex-1">
          <InputGroup>
            <InputGroupAddon>
              <Search />
            </InputGroupAddon>
            <InputGroupInput
              type="search"
              aria-label="Search titles and transcripts"
              placeholder="Search titles and transcripts…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
          </InputGroup>
        </div>
        {/* The Machines page's own segmented control (machines-screen.tsx): two plain choices, pressed or not. */}
        <fieldset className="m-0 inline-flex shrink-0 gap-0.5 rounded-lg border border-solid bg-muted p-0.5">
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
      </div>
      {all.length === 0 ? (
        <p className="py-16 text-center text-sm text-muted-foreground">No meeting holds “{q}”.</p>
      ) : layout === "table" ? (
        <MeetingsTable meetings={all} />
      ) : (
        byDay(all).map((group) => (
          <section key={group.day} aria-label={group.day} className="flex flex-col gap-2">
            <h2 className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
              {group.day}
            </h2>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
              {group.meetings.map((m) => (
                <MeetingCard key={m.id} meeting={m} />
              ))}
            </div>
          </section>
        ))
      )}
    </div>
  )
}

// stageDots are the Machines page's status dots for a meeting's stage.
const stageDots: Record<Stage, string> = {
  transcribing: "bg-running animate-pulse",
  writing: "bg-running animate-pulse",
  ready: "bg-done",
  failed: "bg-failed",
  notes_failed: "bg-needs-you",
}

function StageText({ meeting }: { meeting: Meeting }) {
  const stage = stageOf(meeting)

  return (
    <span className="flex items-center gap-1.5 text-xs whitespace-nowrap text-muted-foreground">
      <span className={cn("size-2 shrink-0 rounded-full", stageDots[stage])} aria-hidden />
      {stageNames[stage]}
    </span>
  )
}

// MeetingCard is the Machines page's card: a head with the title and its state, then a row a fact, an icon, a
// label and a value. The whole card opens the meeting: a card that is pressed is its own design (conventions).
function MeetingCard({ meeting }: { meeting: Meeting }) {
  return (
    <Link
      to="/meetings/$meetingId"
      params={{ meetingId: meeting.id }}
      data-testid="meeting"
      aria-label={meeting.title}
      className="block w-full overflow-hidden rounded-xl border bg-card text-card-foreground outline-none hover:border-border-strong focus-visible:ring-3 focus-visible:ring-ring/50"
    >
      <header className="flex items-center gap-2 border-b border-border/60 bg-muted/30 py-2 ps-4 pe-3">
        <h3 className="min-w-0 flex-1 truncate text-sm font-semibold">{meeting.title}</h3>
        <StageText meeting={meeting} />
      </header>
      <dl
        className="grid gap-2.5 px-5 pt-3.5 pb-4 text-sm"
        style={{ gridTemplateColumns: "24px minmax(70px, max-content) minmax(0, 1fr)" }}
      >
        <Row icon={<CalendarClock />} label="When">
          {whenText(meeting)}
        </Row>
        <Row icon={<UsersRound />} label="Speakers">
          <span className="truncate" title={speakersText(meeting)}>
            {speakersText(meeting) || "—"}
          </span>
        </Row>
        <Row icon={<ListChecks />} label="Actions">
          {stageOf(meeting) === "ready" ? actionsText(meeting) : "—"}
        </Row>
        <Row icon={<Upload />} label="From">
          {fromText(meeting)}
        </Row>
      </dl>
    </Link>
  )
}

function Row({ icon, label, children }: { icon: ReactNode; label: string; children: ReactNode }) {
  return (
    <div className="col-span-3 grid grid-cols-subgrid items-center">
      <span className="flex items-center text-muted-foreground [&_svg]:size-4" aria-hidden>
        {icon}
      </span>
      <dt className="pe-3 text-muted-foreground">{label}:</dt>
      <dd className="flex min-w-0 items-center">{children}</dd>
    </div>
  )
}

const features = tableFeatures({})

const columns: ColumnDef<typeof features, Meeting>[] = [
  {
    id: "title",
    header: "Meeting",
    cell: ({ row }) => (
      <Link
        to="/meetings/$meetingId"
        params={{ meetingId: row.original.id }}
        className="block max-w-72 truncate font-medium hover:underline"
      >
        {row.original.title}
      </Link>
    ),
  },
  { id: "state", header: "State", cell: ({ row }) => <StageText meeting={row.original} /> },
  { id: "when", header: "When", cell: ({ row }) => whenText(row.original) },
  {
    id: "speakers",
    header: "Speakers",
    cell: ({ row }) => (
      <span className="block max-w-56 truncate" title={speakersText(row.original)}>
        {speakersText(row.original) || "—"}
      </span>
    ),
  },
  {
    id: "actions",
    header: "Actions",
    cell: ({ row }) => (stageOf(row.original) === "ready" ? actionsText(row.original) : "—"),
  },
  { id: "from", header: "From", cell: ({ row }) => fromText(row.original) },
]

function MeetingsTable({ meetings }: { meetings: Meeting[] }) {
  const table = useTable({ features, columns, data: meetings, getRowId: (m) => m.id })

  return (
    <div className="min-w-0 overflow-x-auto rounded-xl border bg-card">
      <Table aria-label="Meetings">
        <TableHeader>
          {table.getHeaderGroups().map((group) => (
            <TableRow key={group.id}>
              {group.headers.map((header) => (
                <TableHead key={header.id}>
                  <table.FlexRender header={header} />
                </TableHead>
              ))}
            </TableRow>
          ))}
        </TableHeader>
        <TableBody>
          {table.getRowModel().rows.map((row) => (
            <TableRow key={row.id} data-testid="meeting">
              {row.getAllCells().map((cell) => (
                <TableCell key={cell.id}>
                  <table.FlexRender cell={cell} />
                </TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

function NoMeetings() {
  return (
    <Empty>
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <Mic />
        </EmptyMedia>
        <EmptyTitle>No meetings yet</EmptyTitle>
        <EmptyDescription>
          Upload a recording: it is transcribed with who spoke when, and written up as notes and
          actions.
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <UploadMeeting />
      </EmptyContent>
    </Empty>
  )
}
