import type { QueryClient } from "@tanstack/react-query"
import { useSyncExternalStore } from "react"

import "@/api/client"
import {
  getMeetingQueryKey,
  listMeetingActionsQueryKey,
  listMeetingsQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import { endLiveMeeting, openMeetingTrack } from "@/api/generated/sdk.gen"
import type { MeetingDetail, MeetingLine, MeetingLiveTrack } from "@/api/generated/types.gen"
import { everyCall } from "@/lib/query-keys"

import { MeetingRecorder, type RecorderSnapshot } from "./meeting-recorder"
import { sameVoice } from "./meetings-state"

// The one meeting being recorded in this browser tab (issue #820), as MemoryOS keeps it (meeting-session.ts): it lives
// outside any page, so moving elsewhere in the app keeps recording, and leaving the tab asks first.

type Active = { meetingId: string; recorder: MeetingRecorder }

let active: Active | undefined
// failure is why the last recording in this tab stopped by itself, until the next one starts or the meeting ends.
type Failure = { meetingId: string; code: string }
let failure: Failure | undefined
const listeners = new Set<() => void>()

function publish() {
  for (const listener of listeners) listener()
}

function subscribe(listener: () => void) {
  listeners.add(listener)

  return () => {
    listeners.delete(listener)
  }
}

export function useActiveMeeting() {
  return useSyncExternalStore(subscribe, () => active)
}

export function useRecordingFailure() {
  return useSyncExternalStore(subscribe, () => failure)
}

const idle: RecorderSnapshot = { phase: "stopped", elapsedMs: 0, tracks: [], previews: {} }
const unsubscribed = () => () => undefined

// useRecorderValue is one value of the recorder, which publishes on every level reading: select returns a primitive or
// a value the recorder keeps, so a component repaints only when what it shows changes (MemoryOS's recorder-state.ts).
export function useRecorderValue<T>(
  recorder: MeetingRecorder | undefined,
  select: (snapshot: RecorderSnapshot) => T,
): T {
  return useSyncExternalStore(recorder?.subscribe ?? unsubscribed, () =>
    select(recorder?.getSnapshot() ?? idle),
  )
}

// startRecording records a meeting's tracks; another meeting recording in this tab is stopped first.
export async function startRecording(
  meetingId: string,
  sources: { track: MeetingLiveTrack; stream: MediaStream; offsetMs: number }[],
  cache: QueryClient,
) {
  if (active && active.meetingId !== meetingId) await stopRecording()
  if (active) return active.recorder
  failure = undefined
  const recorder = new MeetingRecorder({
    meetingId,
    issueTicket: async (track) =>
      (await openMeetingTrack({ path: { meeting_id: meetingId }, body: { track } })).data.ticket,
    onLine: (line) => appendLine(cache, meetingId, line),
  })
  active = { meetingId, recorder }
  const unsubscribe = recorder.subscribe(() => {
    const { phase, error } = recorder.getSnapshot()
    if (phase === "stopped" || phase === "failed") {
      unsubscribe()
      if (phase === "failed") failure = { meetingId, code: error ?? "connection" }
      if (active?.recorder === recorder) active = undefined
      window.removeEventListener("beforeunload", warnBeforeUnload)
      publish()
    }
  })
  window.addEventListener("beforeunload", warnBeforeUnload)
  publish()
  try {
    await recorder.start(sources)
  } catch (error) {
    recorder.dispose()
    for (const source of sources) for (const track of source.stream.getTracks()) track.stop()
    if (active?.recorder === recorder) active = undefined
    window.removeEventListener("beforeunload", warnBeforeUnload)
    publish()
    throw error
  }

  return recorder
}

async function stopRecording() {
  const current = active
  if (!current) return
  await current.recorder.stop()
  if (active === current) active = undefined
  window.removeEventListener("beforeunload", warnBeforeUnload)
  publish()
}

// endMeeting stops recording at once, keeps the last words while the bar says so, then ends the meeting. It lives
// here, not on the page, so moving elsewhere in the app does not lose them (MemoryOS's endMeeting).
export async function endMeeting(meetingId: string, cache: QueryClient) {
  if (active?.meetingId === meetingId) await stopRecording()
  const ended = (await endLiveMeeting({ path: { meeting_id: meetingId } })).data
  if (failure?.meetingId === meetingId) {
    failure = undefined
    publish()
  }
  await Promise.all([
    cache.invalidateQueries({ queryKey: getMeetingQueryKey({ path: { meeting_id: meetingId } }) }),
    cache.invalidateQueries({ queryKey: everyCall(listMeetingsQueryKey()) }),
    cache.invalidateQueries({ queryKey: everyCall(listMeetingActionsQueryKey()) }),
  ])

  return ended
}

function warnBeforeUnload(event: BeforeUnloadEvent) {
  if (!active) return
  event.preventDefault()
}

// appendLine adds a line kept to the meeting's page, by when it was said, with its voice the first time it speaks.
function appendLine(cache: QueryClient, meetingId: string, line: MeetingLine) {
  cache.setQueryData<MeetingDetail>(getMeetingQueryKey({ path: { meeting_id: meetingId } }), (d) =>
    d ? withLine(d, line) : d,
  )
}

export function withLine(d: MeetingDetail, line: MeetingLine): MeetingDetail {
  if (d.lines.some((l) => l.n === line.n)) return d

  const voice = { track: line.track, label: line.speaker }
  const speakers = d.meeting.speakers.some((s) => sameVoice(s, voice))
    ? d.meeting.speakers
    : [
        ...d.meeting.speakers,
        {
          ...voice,
          owner: d.meeting.kind === "online" && line.track === "mic",
          name:
            d.meeting.kind === "online" && line.track === "mic"
              ? "You"
              : `Speaker ${d.meeting.speakers.filter((s) => s.track === line.track).length + 1}`,
          named: false,
          talk_ms: 0,
          share: 0,
        },
      ]
  const lines = [...d.lines, line].toSorted((a, b) => a.start_ms - b.start_ms || a.n - b.n)

  return { ...d, meeting: { ...d.meeting, speakers }, lines }
}
