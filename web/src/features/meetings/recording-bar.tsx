import { useQueryClient } from "@tanstack/react-query"
import { CircleAlert, Pause, Play, Square } from "lucide-react"
import { useState } from "react"

import type { MeetingKind, MeetingLiveTrack } from "@/api/generated/types.gen"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { toast } from "@/components/ui/toast"
import { cn } from "@/lib/utils"

import type { MeetingRecorder } from "./meeting-recorder"
import { endMeeting, useRecorderValue } from "./meeting-session"
import { clock } from "./meetings-state"

// RecordingBar is the live controls over a meeting being recorded (issue #820), adapted from MemoryOS's
// recording-bar.tsx without its mute and bookmark: the time recorded, a level meter a track, a warning when the
// shared tab has been silent for 20 seconds (Nojoin) or is no longer shared, Pause and Resume beside Stop (issue
// #1153, MemoryOS's bar and Meetily's RecordingControls.tsx), and Stop, asked first, paused or not, after which the
// bar says the last words are being kept until every track is finished. The clock and each meter follow the recorder
// on their own, so a level reading repaints a meter and nothing else.
export function RecordingBar({
  meetingId,
  recorder,
  kind,
}: {
  meetingId: string
  recorder: MeetingRecorder
  kind: MeetingKind
}) {
  const phase = useRecorderValue(recorder, (s) => s.phase)
  const reconnecting = useRecorderValue(recorder, (s) => s.tracks.some((t) => t.reconnecting))
  const tracks = useRecorderValue(recorder, (s) => s.tracks.map((t) => t.track).join(","))
  const quiet = useRecorderValue(recorder, (s) => s.tracks.some((t) => t.quiet))
  const tabEnded = useRecorderValue(recorder, (s) => s.tracks.some((t) => t.ended))
  const saving = phase === "stopping"
  const paused = phase === "paused"
  const state = saving
    ? "Saving the last words…"
    : paused
      ? "Paused"
      : reconnecting
        ? "Reconnecting…"
        : "Recording"

  return (
    <section
      aria-label="Recording"
      className="sticky top-0 z-10 flex flex-col gap-2 rounded-xl border bg-card px-3 py-2.5 shadow-xs"
    >
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <span className="flex items-center gap-2">
          <span
            aria-hidden
            className={cn(
              "size-2.5 rounded-full",
              paused
                ? "bg-muted-foreground/50"
                : saving || reconnecting
                  ? "animate-pulse bg-running"
                  : "animate-pulse bg-failed",
            )}
          />
          <span
            className="font-mono text-base tabular-nums"
            role="timer"
            aria-label="Time recorded"
          >
            <Clock recorder={recorder} />
          </span>
        </span>
        <span className="text-xs text-muted-foreground" aria-live="polite">
          {state}
        </span>
        {tracks
          .split(",")
          .filter(Boolean)
          .map((track) => (
            <Meter
              key={track}
              recorder={recorder}
              track={track as MeetingLiveTrack}
              label={track === "tab" ? "Meeting tab" : kind === "online" ? "You" : "Microphone"}
            />
          ))}
        <span className="flex-1" />
        <span className="flex items-center gap-2">
          {paused ? (
            <Button size="sm" variant="outline" onClick={() => void recorder.resume()}>
              <Play data-icon="inline-start" />
              Resume
            </Button>
          ) : (
            <Button
              size="sm"
              variant="outline"
              disabled={phase !== "recording"}
              onClick={() => void recorder.pause()}
            >
              <Pause data-icon="inline-start" />
              Pause
            </Button>
          )}
          <StopButton meetingId={meetingId} disabled={saving} />
        </span>
      </div>
      {(quiet || tabEnded) && !saving && (
        <output className="flex items-center gap-1.5 text-xs text-needs-you">
          <CircleAlert className="size-3.5" aria-hidden />
          {tabEnded
            ? "The tab is no longer shared; only your microphone is recorded."
            : "The meeting tab has been silent for 20 seconds. Was its sound shared?"}
        </output>
      )}
    </section>
  )
}

function Clock({ recorder }: { recorder: MeetingRecorder }) {
  // Formatted in the selector, so the clock repaints once a second, not on every level reading.
  return <>{useRecorderValue(recorder, (s) => clock(s.elapsedMs))}</>
}

const barHeights = ["h-2/5", "h-11/20", "h-7/10", "h-17/20", "h-full"]

function Meter({
  recorder,
  track,
  label,
}: {
  recorder: MeetingRecorder
  track: MeetingLiveTrack
  label: string
}) {
  // Speech rarely passes an RMS of 0.3; the meter is a sign of life, repainted only when a bar changes.
  const lit = useRecorderValue(recorder, (s) => {
    const level = s.tracks.find((t) => t.track === track)?.level ?? 0

    return Math.round(Math.min(1, level / 0.15) * barHeights.length)
  })

  return (
    <span
      className="inline-flex items-center gap-1.5 text-xs text-muted-foreground"
      data-testid={`meter-${track}`}
    >
      {label}
      <span className="flex h-3.5 items-end gap-0.5" aria-hidden>
        {barHeights.map((h, i) => (
          <span
            key={h}
            className={cn("w-1 rounded-sm", h, i < lit ? "bg-done" : "bg-muted-foreground/25")}
          />
        ))}
      </span>
    </span>
  )
}

// StopButton asks first, then closes at once: the bar says the last words are kept while the meeting ends.
function StopButton({ meetingId, disabled }: { meetingId: string; disabled: boolean }) {
  const [open, setOpen] = useState(false)
  const cache = useQueryClient()

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <AlertDialogTrigger render={<Button size="sm" variant="destructive" disabled={disabled} />}>
        <Square data-icon="inline-start" />
        Stop
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Stop recording and end the meeting?</AlertDialogTitle>
          <AlertDialogDescription>
            The last words are kept, then the notes are written. An ended meeting is not recorded
            again.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Keep recording</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            onClick={() => {
              setOpen(false)
              endMeeting(meetingId, cache).then(
                () => toast.add({ title: "Meeting ended", type: "success" }),
                () =>
                  toast.add({
                    title: "The meeting could not be ended",
                    description: "Its recording is kept. End it again from its page.",
                    type: "error",
                  }),
              )
            }}
          >
            Stop and end
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
