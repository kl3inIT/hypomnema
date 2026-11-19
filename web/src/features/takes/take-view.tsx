import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Clapperboard, Download, Film, Pause, Play, Trash2 } from "lucide-react"
import { useCallback, useEffect, useRef, useState, type CSSProperties } from "react"

import {
  deleteTakeMutation,
  getTakeQueryKey,
  listTakesQueryKey,
  listVideosOptions,
} from "@/api/generated/@tanstack/react-query.gen"
import type { Take } from "@/api/generated/types.gen"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { MakeVideo } from "@/features/studio/make-video"
import { errorText } from "@/lib/server-error"
import { cn } from "@/lib/utils"

import {
  archiveUrl,
  chapters,
  clock,
  locate,
  seekInSegment,
  segmentUrl,
  stateLine,
  takeLength,
  useTake,
} from "./take-state"

// TakeView is a take as a tab of a pane (issue #817, row 13): the player and, beside it or under it on a narrow
// pane, the take's steps. The player plays the segments one after the other on one timeline, with a mark for each
// step and a hatched mark where the owner's takeover paused the take; a step, a chapter or a place of the timeline
// pressed seeks there, as autodemo's share page seeks its video from its chapters and transcript
// (src/share.js:68-78). The bar above has the take's download, a zip of its segments and take.json, and its
// delete, which asks first. The bar is the phone view's (../device/phone-view.tsx), in the pane's shape. Where the
// place showing it opens videos (onOpenVideo), an available take has "Make video" (issue #818, row 12), and "Video"
// once it has one.
export function TakeView({
  id,
  onClose,
  onOpenVideo,
}: {
  id: string
  onClose: () => void
  onOpenVideo?: (video: string) => void
}) {
  const read = useTake(id)
  const take = read.data
  const ready = take?.state === "available"
  // A take has at most one video (issue #818, row 1).
  const video = useQuery({
    ...listVideosOptions(),
    enabled: ready && onOpenVideo !== undefined,
    select: (list) => list.data.find((v) => v.take === id),
  }).data
  const [deleting, setDeleting] = useState(false)
  const queryClient = useQueryClient()
  const remove = useMutation({
    ...deleteTakeMutation(),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: listTakesQueryKey() })
      queryClient.removeQueries({ queryKey: getTakeQueryKey({ path: { take_id: id } }) })
      onClose()
    },
  })

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col" data-testid="take-view">
      <div className="flex h-11 shrink-0 items-center gap-2 border-b px-3">
        <Clapperboard className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <p className="min-w-0 flex-1 truncate text-sm">
          <span className="font-medium">{take?.title ?? "Take"}</span>
          {take && (
            <span className="ms-2 text-muted-foreground">
              {take.agent_name ?? take.agent} · {clock(take.duration_ms)}
            </span>
          )}
        </p>
        {ready && onOpenVideo && video && (
          <Button variant="ghost" size="sm" onClick={() => onOpenVideo(video.id)}>
            <Film />
            Video
          </Button>
        )}
        {ready && onOpenVideo && (
          <MakeVideo take={id} again={video !== undefined} onMade={(v) => onOpenVideo(v.id)} />
        )}
        {take?.state === "available" && (
          <Button
            variant="ghost"
            size="sm"
            nativeButton={false}
            render={
              <a href={archiveUrl(id)} download={`${id}.zip`} aria-label="Download the take" />
            }
          >
            <Download />
            Download
          </Button>
        )}
        {take && (
          <Button variant="ghost" size="sm" onClick={() => setDeleting(true)}>
            <Trash2 />
            Delete
          </Button>
        )}
      </div>
      {take?.state === "available" ? (
        <TakePlayer take={take} />
      ) : (
        <div className="flex flex-1 items-center justify-center bg-muted px-6 py-10">
          <div
            role={take?.state === "failed" || read.isError ? "alert" : "status"}
            className="flex max-w-sm flex-col items-center gap-3 text-center"
          >
            <div className="grid size-12 place-items-center rounded-xl border bg-background">
              <Clapperboard className="size-6 text-muted-foreground" aria-hidden />
            </div>
            <p className="text-sm font-medium">{take?.title ?? "Take"}</p>
            <p className="flex items-center gap-2 text-xs text-muted-foreground">
              {take && take.state !== "failed" && <Spinner />}
              {take ? stateLine(take) : read.isError ? errorText(read.error) : "Reading the take…"}
            </p>
          </div>
        </div>
      )}
      <AlertDialog open={deleting} onOpenChange={setDeleting}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete this take?</AlertDialogTitle>
            <AlertDialogDescription>
              Its video and steps are removed from the server, and from the phone's machine if it
              still holds them. This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          {remove.error && (
            <p role="alert" className="text-sm text-destructive">
              {errorText(remove.error)}
            </p>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel>Keep it</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={remove.isPending}
              onClick={(event) => {
                event.preventDefault()
                remove.mutate({ path: { take_id: id } })
              }}
            >
              Delete the take
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

// Pending is a seek to make once the segment's video has loaded.
type Pending = { to: number; ahead: number; play: boolean }

// TakePlayer plays an available take's segments on one timeline.
function TakePlayer({ take }: { take: Take }) {
  const video = useRef<HTMLVideoElement>(null)
  const [index, setIndex] = useState(0)
  const [time, setTime] = useState(0)
  const [playing, setPlaying] = useState(false)
  // How far the timeline is ahead of the video, in the still tail of a segment (take-state.ts, seekInSegment).
  const ahead = useRef(0)
  const pending = useRef<Pending | null>(null)
  const length = takeLength(take)
  const segment = take.segments[index]
  const marks = chapters(take.steps)

  const seek = useCallback(
    (ms: number, play?: boolean) => {
      const at = locate(take, ms)
      const v = video.current
      if (!at || !v) return

      const target = take.segments[at.index]
      if (!target) return

      const { to, ahead: lead } = seekInSegment(target, at.offset)
      const keepPlaying = play ?? !v.paused
      setTime(target.at_ms + at.offset)

      if (at.index !== index) {
        pending.current = { to, ahead: lead, play: keepPlaying }
        setIndex(at.index)

        return
      }

      ahead.current = lead
      v.currentTime = to / 1000
      if (keepPlaying) void v.play().catch(() => setPlaying(false))
    },
    [index, take],
  )

  // The segment's video loaded: the seek waiting for it is made.
  const loaded = () => {
    const v = video.current
    const next = pending.current
    pending.current = null
    if (!v || !next) return

    ahead.current = next.ahead
    v.currentTime = next.to / 1000
    if (next.play) void v.play().catch(() => setPlaying(false))
  }

  // The end of a segment goes on in the next one; the last one's end is the take's.
  const segmentEnded = () => {
    if (index + 1 < take.segments.length) {
      pending.current = { to: 0, ahead: 0, play: true }
      setIndex(index + 1)

      return
    }

    video.current?.pause()
    setPlaying(false)
    setTime(length)
  }

  const ticked = () => {
    const v = video.current
    if (!v || !segment || pending.current) return

    const at = segment.at_ms + v.currentTime * 1000 + ahead.current
    // In a still tail the video stands behind the timeline: its segment ends when the timeline reaches its end.
    if (ahead.current > 0 && at >= segment.at_ms + segment.length_ms) {
      segmentEnded()

      return
    }

    setTime(Math.min(at, length))
  }

  // While it plays the time moves on more often than the video's timeupdate says it.
  useEffect(() => {
    if (!playing) return

    let frame = requestAnimationFrame(function tick() {
      ticked()
      frame = requestAnimationFrame(tick)
    })

    return () => cancelAnimationFrame(frame)
  })

  const toggle = () => {
    const v = video.current
    if (!v) return
    if (v.paused) {
      if (time >= length) seek(0, true)
      else void v.play().catch(() => setPlaying(false))
    } else v.pause()
  }

  const percent = (ms: number) => `${length > 0 ? Math.min(Math.max(ms / length, 0), 1) * 100 : 0}%`
  const current = take.steps.findLastIndex((s) => s.at_ms <= time)

  return (
    // The container is the outer box: a container query styles what is inside it, not the container itself.
    <div className="@container/take flex min-h-0 min-w-0 flex-1">
      <div className="flex min-h-0 min-w-0 flex-1 flex-col @3xl/take:flex-row">
        <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-muted">
          <div className="flex min-h-0 flex-1 items-center justify-center p-3">
            {segment && (
              <video
                ref={video}
                key={segment.file}
                src={segmentUrl(take.id, segment.file)}
                aria-label={`The take ${take.title}`}
                data-testid="take-video"
                data-segment={segment.file}
                playsInline
                muted
                preload="metadata"
                className="max-h-full max-w-full rounded-lg bg-black shadow-lg"
                onLoadedMetadata={loaded}
                onTimeUpdate={ticked}
                onPlay={() => setPlaying(true)}
                onPause={() => setPlaying(false)}
                onEnded={segmentEnded}
              />
            )}
          </div>
          <div className="flex shrink-0 items-center gap-3 border-t bg-background px-3 py-2">
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={playing ? "Pause" : "Play"}
              onClick={toggle}
            >
              {playing ? <Pause /> : <Play />}
            </Button>
            <span
              className="shrink-0 text-xs text-muted-foreground tabular-nums"
              data-testid="take-time"
            >
              {clock(time)} / {clock(length)}
            </span>
            <div className="relative h-6 min-w-0 flex-1">
              <div className="pointer-events-none absolute inset-x-0 top-1/2 h-1.5 -translate-y-1/2 overflow-hidden rounded-full bg-border">
                <div className="h-full bg-primary" style={{ width: percent(time) }} />
              </div>
              {take.pauses.map((p) => (
                <span
                  key={`pause-${p.at_ms}`}
                  title={`Paused for you at ${clock(p.at_ms)}`}
                  data-testid="take-pause"
                  className="pointer-events-none absolute top-0 h-full w-1.5 -translate-x-1/2 rounded-sm border border-foreground/30"
                  style={{ left: percent(p.at_ms), ...hatch }}
                />
              ))}
              {take.steps.map((s, i) => (
                <span
                  // Two steps may share a time.
                  key={`step-${i}`}
                  data-testid="take-mark"
                  className="pointer-events-none absolute top-1/2 size-2 -translate-1/2 rounded-full bg-foreground ring-2 ring-background"
                  style={{ left: percent(s.at_ms) }}
                />
              ))}
              {/* The timeline is a range the owner drags or steps with the arrow keys, over the marks. */}
              <input
                type="range"
                aria-label="Where in the take"
                aria-valuetext={`${clock(time)} of ${clock(length)}`}
                min={0}
                max={length}
                step={100}
                value={Math.min(time, length)}
                onChange={(event) => seek(Number(event.currentTarget.value))}
                className="absolute inset-0 size-full cursor-pointer opacity-0"
              />
            </div>
          </div>
        </div>
        <aside
          aria-label="Steps"
          className="flex max-h-[45%] min-h-0 shrink-0 flex-col overflow-y-auto border-t @3xl/take:max-h-none @3xl/take:w-80 @3xl/take:border-s @3xl/take:border-t-0"
        >
          {marks.length > 1 && (
            <section className="border-b px-3 py-2">
              <h2 className="pb-1 text-xs font-medium text-muted-foreground">Chapters</h2>
              <ul className="flex flex-wrap gap-1">
                {marks.map((c) => (
                  <li key={`${c.at}-${c.title}`}>
                    <Button variant="outline" size="xs" onClick={() => seek(c.at)}>
                      <span className="text-muted-foreground tabular-nums">{clock(c.at)}</span>
                      {c.title}
                    </Button>
                  </li>
                ))}
              </ul>
            </section>
          )}
          <section className="px-1 py-2">
            <h2 className="px-2 pb-1 text-xs font-medium text-muted-foreground">
              {take.steps.length} {take.steps.length === 1 ? "step" : "steps"}
            </h2>
            {take.steps.length === 0 && (
              <p className="px-2 text-sm text-muted-foreground">The agent said no step.</p>
            )}
            <ol>
              {take.steps.map((s, i) => (
                <li key={`${s.at_ms}-${i}`}>
                  <button
                    type="button"
                    aria-current={i === current ? "step" : undefined}
                    onClick={() => seek(s.at_ms)}
                    className={cn(
                      "flex w-full items-start gap-2 rounded-md px-2 py-1.5 text-start text-sm hover:bg-accent focus-visible:bg-accent focus-visible:outline-none",
                      i === current && "bg-accent",
                    )}
                  >
                    <span className="shrink-0 pt-px text-xs text-muted-foreground tabular-nums">
                      {clock(s.at_ms)}
                    </span>
                    <span className="min-w-0">
                      {s.chapter && (
                        <span className="block text-xs font-medium text-muted-foreground">
                          {s.chapter}
                        </span>
                      )}
                      {s.text}
                    </span>
                  </button>
                </li>
              ))}
            </ol>
          </section>
        </aside>
      </div>
    </div>
  )
}

// hatch draws a pause's mark with stripes, as the prototype's timeline marks the time the owner held the phone.
const hatch: CSSProperties = {
  backgroundImage:
    "repeating-linear-gradient(135deg, var(--color-foreground) 0 1.5px, transparent 1.5px 4px)",
  opacity: 0.6,
}
