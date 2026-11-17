import { Link, useNavigate } from "@tanstack/react-router"
import { Check, ChevronRight, CircleAlert, LoaderCircle, Search, Sparkles } from "lucide-react"
import { useEffect, useRef, useState } from "react"

import type { Meeting, MeetingDetail, MeetingItem } from "@/api/generated/types.gen"
import { deleteMeeting } from "@/api/generated/sdk.gen"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { ErrorAlert } from "@/components/error-alert"
import { PageHeader } from "@/components/page-header"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button, buttonVariants } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { toast } from "@/components/ui/toast"
import { cn } from "@/lib/utils"

import { PlayerProvider, TimeLink } from "./meeting-player"
import { usePlayingAt } from "./player-context"
import {
  clock,
  percent,
  providerName,
  speakerTone,
  stageOf,
  stepsOf,
  whenText,
  working,
  type Step,
} from "./meetings-state"
import { SpeakerPicker } from "./speaker-picker"
import { WordFix } from "./word-fix"
import {
  recordingUrl,
  useMarkDone,
  useMeeting,
  useRefreshMeetings,
  useWriteNotes,
} from "./use-meetings"

// MeetingScreen is one meeting (issue #721): the player on top, then Summary and Transcript as tabs with "Write
// again" on the tab row, as Otter, Fireflies and Nojoin lay a meeting out (player, tabs) and Granola, Fireflies and
// Otter regenerate. While the recording is heard or written up, the steps
// show where it stands (Nojoin's stage texts, Meetily, MemoryOS); a failure says why and where to fix it.
export function MeetingScreen({ id }: { id: string }) {
  const meeting = useMeeting(id)
  const d = meeting.data

  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader title={<Crumbs here={d?.meeting.title ?? "Meeting"} />} />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-10 sm:px-5">
        <div className="mx-auto flex w-full max-w-4xl flex-col gap-4 pt-2">
          {meeting.error ? (
            <Empty>
              <EmptyHeader>
                <EmptyTitle>This meeting could not be read</EmptyTitle>
                <EmptyDescription>It may have been deleted.</EmptyDescription>
              </EmptyHeader>
              <Link to="/meetings" className={buttonVariants({ variant: "outline", size: "sm" })}>
                Back to Meetings
              </Link>
            </Empty>
          ) : !d ? (
            <p className="py-10 text-center text-sm text-muted-foreground">Loading…</p>
          ) : (
            <PlayerProvider src={recordingUrl(id)} durationMs={d.meeting.duration_ms}>
              {(player) => (
                <>
                  <Head meeting={d.meeting} />
                  {player}
                  {working(d.meeting) || stageOf(d.meeting) !== "ready" ? (
                    <Progress meeting={d.meeting} />
                  ) : null}
                  {d.meeting.state === "transcribed" && <Body detail={d} />}
                </>
              )}
            </PlayerProvider>
          )}
        </div>
      </div>
    </main>
  )
}

function Crumbs({ here }: { here: string }) {
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <Link to="/meetings" className="text-muted-foreground hover:text-foreground">
        Meetings
      </Link>
      <ChevronRight className="size-3.5 shrink-0 text-muted-foreground/60" aria-hidden />
      <span className="truncate">{here}</span>
    </span>
  )
}

function Head({ meeting }: { meeting: Meeting }) {
  const navigate = useNavigate()
  const refresh = useRefreshMeetings()
  const speakers = meeting.speakers.map((s) => s.name).join(", ")

  return (
    <header className="flex items-start gap-3">
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <h2 className="text-2xl leading-tight font-semibold text-pretty">{meeting.title}</h2>
        <p className="text-sm text-muted-foreground">
          {whenText(meeting)}
          {speakers && ` · ${speakers}`}
        </p>
      </div>
      <ConfirmDialog
        trigger={
          <Button variant="outline" size="sm">
            Delete
          </Button>
        }
        title={`Delete ${meeting.title}?`}
        consequence="Its recording, transcript, notes and actions are deleted, and cannot be brought back."
        action="Delete meeting"
        run={() => deleteMeeting({ path: { meeting_id: meeting.id } })}
        onDone={async () => {
          await refresh()
          await navigate({ to: "/meetings" })
        }}
        done="Meeting deleted"
      />
    </header>
  )
}

const stepTone: Record<Step["state"], string> = {
  done: "text-done",
  now: "text-running",
  waiting: "text-muted-foreground",
  failed: "text-failed",
}

// Progress is where the meeting stands: its steps, and why one failed with where to fix it.
function Progress({ meeting }: { meeting: Meeting }) {
  const stage = stageOf(meeting)
  const failure =
    stage === "failed" ? meeting.failure : stage === "notes_failed" ? meeting.notes.failure : ""
  const title =
    stage === "transcribing"
      ? "Transcribing…"
      : stage === "writing"
        ? "Writing notes…"
        : stage === "failed"
          ? "The recording could not be heard"
          : "The notes could not be written"

  return (
    <Alert
      variant={failure ? "destructive" : "default"}
      aria-label="Progress"
      data-testid="meeting-progress"
    >
      {failure ? <CircleAlert /> : <LoaderCircle className="animate-spin" />}
      <AlertTitle>{title}</AlertTitle>
      <AlertDescription>
        <div className="flex flex-col gap-1.5">
          <ol className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
            {stepsOf(meeting).map((s) => (
              <li key={s.name} className={cn("flex items-center gap-1", stepTone[s.state])}>
                {s.state === "done" ? (
                  <Check className="size-3" aria-hidden />
                ) : (
                  <span className="size-1.5 rounded-full bg-current" aria-hidden />
                )}
                {s.name}
              </li>
            ))}
          </ol>
          {failure && <p>{failure}</p>}
        </div>
      </AlertDescription>
    </Alert>
  )
}

type Tab = "summary" | "transcript"

function Body({ detail }: { detail: MeetingDetail }) {
  const [tab, setTab] = useState<Tab>("summary")
  // fixed: a word was fixed in the transcript since the page opened, and the notes are as they were (issue #747).
  const [fixed, setFixed] = useState(false)

  return (
    <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)}>
      <div className="flex flex-wrap items-center gap-2 border-b border-border/60 pb-2">
        <TabsList variant="line">
          <TabsTrigger value="summary">Summary</TabsTrigger>
          <TabsTrigger value="transcript">Transcript</TabsTrigger>
        </TabsList>
        <WriteAgain meeting={detail.meeting} />
      </div>
      {fixed && detail.meeting.notes.state !== "writing" && (
        <NotesBehind meeting={detail.meeting} onDone={() => setFixed(false)} />
      )}
      <TabsContent value="summary">
        <Summary detail={detail} />
      </TabsContent>
      <TabsContent value="transcript">
        <Transcript detail={detail} onFixed={() => setFixed(true)} />
      </TabsContent>
    </Tabs>
  )
}

// NotesBehind says the notes were written before a word was fixed and offers to write them again;
// the fix never rewrites them by itself, as MemoryOS keeps the minutes when a line is corrected.
function NotesBehind({ meeting, onDone }: { meeting: Meeting; onDone: () => void }) {
  const write = useWriteNotes(meeting.id)

  return (
    <div className="pt-3">
      <Alert data-testid="notes-behind">
        <Sparkles />
        <AlertTitle>The notes were written before your fix</AlertTitle>
        <AlertDescription>
          <div className="flex flex-wrap items-center gap-2">
            <span>Write them again to use the fixed words.</span>
            <Button
              size="sm"
              variant="outline"
              disabled={write.isPending}
              onClick={() =>
                write.mutate(
                  { path: { meeting_id: meeting.id } },
                  {
                    onSuccess: () => {
                      toast.add({ title: "Writing the notes again", type: "success" })
                      onDone()
                    },
                  },
                )
              }
            >
              Write again
            </Button>
            <Button size="sm" variant="ghost" onClick={onDone}>
              Not now
            </Button>
          </div>
          <ErrorAlert error={write.error} />
        </AlertDescription>
      </Alert>
    </div>
  )
}

// WriteAgain writes the notes again (Granola, Fireflies, Otter); they are sectioned by the meeting's topics (issue
// #771), so there is nothing to pick.
function WriteAgain({ meeting }: { meeting: Meeting }) {
  const write = useWriteNotes(meeting.id)
  const busy = meeting.notes.state === "writing" || write.isPending

  return (
    <div className="ms-auto flex items-center gap-2">
      <Button
        size="sm"
        variant="outline"
        disabled={busy}
        onClick={() =>
          write.mutate(
            { path: { meeting_id: meeting.id } },
            { onSuccess: () => toast.add({ title: "Writing the notes again", type: "success" }) },
          )
        }
      >
        <Sparkles data-icon="inline-start" />
        Write again
      </Button>
      {write.error && <span className="sr-only">{String(write.error)}</span>}
    </div>
  )
}

function Summary({ detail }: { detail: MeetingDetail }) {
  const notes = detail.meeting.notes
  const decisions = detail.items.filter((i) => i.kind === "decision")
  const actions = detail.items.filter((i) => i.kind === "action")
  const open = actions.filter((a) => !a.done).length

  if (notes.state !== "ready" && !notes.summary) {
    return (
      <p className="py-6 text-center text-sm text-muted-foreground">
        {notes.state === "failed" ? "No notes yet." : "The notes are being written."}
      </p>
    )
  }

  return (
    <div className="flex flex-col gap-3">
      <Card title="Summary">
        <p className="text-sm leading-relaxed text-pretty">{notes.summary || "—"}</p>
      </Card>
      {decisions.length > 0 && (
        <Card title="Decisions">
          <ul className="flex flex-col gap-1.5 text-sm">
            {decisions.map((d) => (
              <li key={d.id} className="flex flex-wrap items-baseline gap-x-2">
                <span>• {d.text}</span>
                {d.start_ms != null && <TimeLink ms={d.start_ms} />}
              </li>
            ))}
          </ul>
        </Card>
      )}
      <Card title={actions.length ? `Actions · ${open} open` : "Actions"}>
        {actions.length === 0 ? (
          <p className="text-sm text-muted-foreground">No actions were handed out.</p>
        ) : (
          <ul className="flex flex-col divide-y divide-border/60">
            {actions.map((a) => (
              <ActionRow key={a.id} meetingId={detail.meeting.id} action={a} />
            ))}
          </ul>
        )}
      </Card>
      {notes.sections
        .filter((s) => s.points.length > 0)
        .map((s) => (
          <Card key={s.title} title={s.title}>
            <ul className="flex list-disc flex-col gap-1 ps-5 text-sm">
              {s.points.map((p) => (
                <li key={p}>{p}</li>
              ))}
            </ul>
          </Card>
        ))}
      {notes.model && <p className="text-xs text-muted-foreground">Written by {notes.model}.</p>}
    </div>
  )
}

function Card({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section aria-label={title} className="rounded-xl border bg-card px-4 py-3">
      <h2 className="mb-2 text-xs font-semibold tracking-wide text-muted-foreground uppercase">
        {title}
      </h2>
      {children}
    </section>
  )
}

function ActionRow({ meetingId, action }: { meetingId: string; action: MeetingItem }) {
  const mark = useMarkDone()
  const id = `action-${action.id}`

  return (
    <li className="flex items-start gap-3 py-2">
      <span className="flex pt-0.5">
        <Checkbox
          id={id}
          checked={action.done}
          disabled={mark.isPending}
          onCheckedChange={(done) =>
            mark.mutate({ path: { meeting_id: meetingId, item_id: action.id }, body: { done } })
          }
        />
      </span>
      <div className="flex min-w-0 flex-col gap-0.5">
        <Label htmlFor={id}>
          <span className={cn("font-normal", action.done && "text-muted-foreground line-through")}>
            {action.owner && <span className="font-semibold">{action.owner} · </span>}
            {action.text}
          </span>
        </Label>
        <span className="flex flex-wrap items-center gap-x-1.5 text-xs text-muted-foreground">
          {action.due ? `Due ${action.due}` : "No date"}
          {action.start_ms != null && (
            <>
              <span aria-hidden>·</span>
              <TimeLink ms={action.start_ms} />
            </>
          )}
        </span>
        {mark.error && <ErrorAlert error={mark.error} />}
      </div>
    </li>
  )
}

// Transcript is the lines with their voices' colors and their share of the talk (Otter's %), each voice's name a
// picker that says who spoke the line (speaker-picker.tsx), each time a way to play from there; the line being
// played is marked. Each line has a way to fix a word heard wrong (word-fix.tsx), opened with what the owner selected
// in that line.
function Transcript({ detail, onFixed }: { detail: MeetingDetail; onFixed: () => void }) {
  const [find, setFind] = useState("")
  const picked = usePickedWords()
  const speakers = detail.meeting.speakers
  const at = usePlayingAt()
  const needle = find.trim().toLowerCase()
  const lines = needle
    ? detail.lines.filter((l) => l.text.toLowerCase().includes(needle))
    : detail.lines

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <ul aria-label="Speakers" className="flex flex-wrap gap-1.5">
          {speakers.map((s) => {
            const suggestion = detail.suggestions.find((g) => g.label === s.label)

            return (
              <li
                key={s.label}
                className="flex items-center gap-1.5 rounded-lg border bg-card px-2 py-1 text-xs"
              >
                <span
                  className={cn("size-2 rounded-full", speakerTone(speakers, s.label).dot)}
                  aria-hidden
                />
                <span className="font-medium">{s.name}</span>
                <span className="text-muted-foreground tabular-nums">{percent(s.share)}</span>
                {suggestion && (
                  <span className="rounded-full border border-running/40 px-1.5 text-running">
                    {suggestion.name}?
                  </span>
                )}
              </li>
            )
          })}
        </ul>
        <div className="ms-auto w-full sm:w-56">
          <InputGroup>
            <InputGroupAddon>
              <Search />
            </InputGroupAddon>
            <InputGroupInput
              type="search"
              aria-label="Find in transcript"
              placeholder="Find in transcript…"
              value={find}
              onChange={(e) => setFind(e.target.value)}
            />
          </InputGroup>
        </div>
      </div>
      <ol aria-label="Transcript" className="flex flex-col gap-1 rounded-xl border bg-card p-2">
        {lines.length === 0 && (
          <li className="py-6 text-center text-sm text-muted-foreground">
            {needle ? "No line holds this." : "Nothing was heard."}
          </li>
        )}
        {lines.map((l) => {
          const speaker = speakers.find((s) => s.label === l.speaker)
          const now = at >= l.start_ms && at < l.end_ms

          return (
            <li
              key={l.n}
              data-testid="transcript-line"
              aria-current={now || undefined}
              className={cn(
                "grid grid-cols-1 gap-x-4 gap-y-0.5 rounded-lg px-2 py-1.5 sm:grid-cols-[9rem_minmax(0,1fr)]",
                now && "bg-muted",
              )}
            >
              <div className="flex items-center gap-2 sm:flex-col sm:items-start sm:gap-0">
                <SpeakerPicker meeting={detail} line={l}>
                  {/* Its own design, not a Button: the line's speaker in the voice's color, underlined with dots to
                      say it is changed here, as MemoryOS's and the prototype's transcripts show it. */}
                  <button
                    type="button"
                    className={cn(
                      "rounded-sm text-start text-sm font-semibold underline decoration-dotted underline-offset-4 outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
                      speakerTone(speakers, l.speaker).text,
                    )}
                    aria-label={`${speaker?.name ?? "Speaker"} said line ${l.n}; change`}
                  >
                    {speaker?.name ?? "Speaker"}
                  </button>
                </SpeakerPicker>
                <TimeLink ms={l.start_ms} quiet />
              </div>
              <div className="flex min-w-0 items-start gap-1">
                <p className="min-w-0 flex-1 text-sm leading-relaxed" data-line={l.n}>
                  {l.text}
                </p>
                <WordFix meeting={detail} line={l} selected={() => picked(l.n)} onFixed={onFixed} />
              </div>
            </li>
          )
        })}
      </ol>
      <p className="text-xs text-muted-foreground">
        {detail.lines.length} lines · {clock(detail.meeting.duration_ms)} heard by{" "}
        {providerName(detail.meeting.provider ?? "")}
      </p>
    </div>
  )
}

// usePickedWords keeps the last words the owner selected inside one line of the transcript, as the selection is gone
// by the time the line's fix button is pressed on some browsers; picked(n) gives them once for line n, else "".
function usePickedWords() {
  const last = useRef<{ line: number; text: string } | null>(null)

  useEffect(() => {
    const keep = () => {
      const sel = document.getSelection()
      const text = sel?.toString().trim() ?? ""
      if (!sel || !text || sel.rangeCount === 0) return

      const start = sel.anchorNode?.parentElement?.closest("[data-line]")
      const end = sel.focusNode?.parentElement?.closest("[data-line]")
      if (start && start === end)
        last.current = { line: Number(start.getAttribute("data-line")), text }
    }

    document.addEventListener("selectionchange", keep)

    return () => document.removeEventListener("selectionchange", keep)
  }, [])

  return (n: number) => {
    const text = last.current?.line === n ? last.current.text.slice(0, 200) : ""
    last.current = null

    return text
  }
}
