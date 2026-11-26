import { useQuery } from "@tanstack/react-query"

import "@/api/client"
import { getTakeOptions } from "@/api/generated/@tanstack/react-query.gen"
import type { Take, TakeSegment, TakeStep } from "@/api/generated/types.gen"

// What the web client works out of a take (issue #817): which take a video tool's call names, the take's state as
// it goes, where a time of its timeline is in its segments, and its chapters.

// takeId is a take's id as the control plane gives it.
const takeId = /\btk_[0-9a-f]{16}\b/

// takeOfCall is the take a video tool's call names: its argument, or the take its answer says.
export function takeOfCall(args?: Record<string, unknown>, output?: string): string | null {
  const named = args?.take
  if (typeof named === "string" && takeId.test(named)) return named

  return output?.match(takeId)?.[0] ?? null
}

// startsATake says a tool starts filming: the phone's video_start, or a tab of the browser's (issue #1189).
export const startsATake = (tool: string) =>
  tool === "video_start" || tool === "browser_video_start"

// sourceName is where a take was filmed, as a chip says it (issue #1189).
export function sourceName(source: Take["source"]): string {
  return source === "browser" ? "Browser" : "Phone"
}

// A take's state is final once it no longer changes: recording, paused, finalizing and uploading come before.
export const finalStates: readonly string[] = ["available", "failed"]

// stateLine is a take's state as a card says it.
export function stateLine(take: Take): string {
  switch (take.state) {
    case "recording":
      return "Filming"
    case "paused":
      return "Paused while you control the phone"
    case "finalizing":
      return "Finishing the take"
    case "uploading":
      return "Uploading from the phone's machine"
    case "available":
      return "Ready to watch"
    case "failed":
      return take.reason ? `Failed: ${take.reason}` : "Failed"
    default:
      return take.state
  }
}

// useTake reads a take, again every two seconds until its state is final.
export function useTake(id: string) {
  return useQuery({
    ...getTakeOptions({ path: { take_id: id } }),
    refetchInterval: (query) => {
      const state = query.state.data?.state

      return state && finalStates.includes(state) ? false : 2000
    },
  })
}

// clock is a time of the timeline as minutes and seconds, as autodemo's share page stamps its chapters.
export function clock(ms: number): string {
  const s = Math.floor(Math.max(ms, 0) / 1000)

  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`
}

export const segmentUrl = (take: string, file: string) =>
  `/api/v1/takes/${encodeURIComponent(take)}/segments/${encodeURIComponent(file)}`

export const archiveUrl = (take: string) => `/api/v1/takes/${encodeURIComponent(take)}/archive`

// takeLength is the take's whole timeline.
export function takeLength(take: Take): number {
  const last = take.segments.at(-1)

  return last ? last.at_ms + last.length_ms : take.duration_ms
}

// locate is the segment that holds a time of the timeline, and the time in it; the end is in the last.
export function locate(take: Take, ms: number): { index: number; offset: number } | null {
  const segments = take.segments
  if (segments.length === 0) return null

  const at = Math.min(Math.max(ms, 0), takeLength(take))
  for (let index = 0; index < segments.length; index++) {
    const s = segments[index] as TakeSegment
    if (at < s.at_ms + s.length_ms || index === segments.length - 1) {
      return { index, offset: Math.min(Math.max(at - s.at_ms, 0), s.length_ms) }
    }
  }

  return null
}

// tailLead is how far before the last frame a seek into the still tail lands: a browser does not seek into a
// file's last sample (tried with Chromium on 2026-11-18, issue #817), so it seeks just before it and plays on.
const tailLead = 10

// seekInSegment is where the video seeks for a time in a segment, and how far the timeline is ahead of the video
// then: in the still tail after the last frame the video stands just before that frame, the picture being the same
// until the segment ends.
export function seekInSegment(segment: TakeSegment, offset: number): { to: number; ahead: number } {
  if (offset < segment.last_frame_ms) return { to: offset, ahead: 0 }

  const to = Math.max(segment.last_frame_ms - tailLead, 0)

  return { to, ahead: offset - to }
}

// Chapter is a chapter of the take, at a time of its timeline.
export type Chapter = { title: string; at: number }

// chapters are the take's chapters as autodemo makes them (src/share.js:15-22): the chapters the agent named, else
// one for each line said at least six seconds after the last, named by its first words; and "Intro" at the start
// when the first is later than half a second.
export function chapters(steps: TakeStep[]): Chapter[] {
  let out: Chapter[] = steps.flatMap((s) => (s.chapter ? [{ title: s.chapter, at: s.at_ms }] : []))

  if (out.length === 0) {
    for (const s of steps) {
      const last = out.at(-1)
      if (!last || s.at_ms - last.at >= 6000) {
        const words = (s.text.split(/[.,:;!?]/)[0] ?? "").trim().split(/\s+/).slice(0, 7).join(" ")
        out.push({ title: words, at: s.at_ms })
      }
    }
  }

  const first = out[0]
  if (first && first.at > 500) out = [{ title: "Intro", at: 0 }, ...out]

  return out
}
