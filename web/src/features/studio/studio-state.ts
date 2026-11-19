import { useInfiniteQuery, useQuery } from "@tanstack/react-query"

import "@/api/client"
import {
  getVideoOptions,
  listStudioItemsInfiniteOptions,
} from "@/api/generated/@tanstack/react-query.gen"
import type { StudioItem, Video, VideoRevision } from "@/api/generated/types.gen"

// What the Studio works out of the takes and videos (issue #818, rows 10 and 12): the state groups it filters by,
// how a card names a state, a video's stages while it is made, and the reads that follow them until they settle.

export type Kind = StudioItem["kind"]
export type Group = "in_progress" | "review" | "approved" | "failed"

// The state groups of the design (row 12), as the API's state filter names them.
export const groups: readonly Group[] = ["in_progress", "review", "approved", "failed"]

export const groupNames: Record<Group, string> = {
  in_progress: "In progress",
  review: "Awaiting review",
  approved: "Approved",
  failed: "Failed or changes asked",
}

// The states of a revision being made, in the order the runner goes through them (row 1).
const makingStates: readonly string[] = ["queued", "voicing", "composing", "captioning"]

// making says a revision is queued or being made.
export const making = (state: string) => makingStates.includes(state)

// A take's states before it can be watched (issue #817).
const takeWorking: readonly string[] = ["recording", "paused", "finalizing", "uploading"]

// inProgress says an item of the Studio still changes on its own: a take filmed or uploaded, a revision made.
export function inProgress(item: StudioItem): boolean {
  return item.kind === "take" ? takeWorking.includes(item.state) : making(item.state)
}

// filming says a take is being filmed now: its card shows a placeholder with a red dot.
export const filming = (item: StudioItem) =>
  item.kind === "take" && (item.state === "recording" || item.state === "paused")

const takeStates: Record<string, string> = {
  recording: "Filming",
  paused: "Paused",
  finalizing: "Finishing",
  uploading: "Uploading",
  available: "Ready",
  failed: "Failed",
}

const revisionStates: Record<string, string> = {
  queued: "Queued",
  voicing: "Voicing",
  composing: "Composing",
  captioning: "Captioning",
  review: "Awaiting review",
  approved: "Approved",
  changes_requested: "Changes asked",
  failed: "Failed",
}

// stateName is a state as a chip says it.
export function stateName(kind: Kind, state: string): string {
  return (kind === "take" ? takeStates[state] : revisionStates[state]) ?? state
}

// Tone is a chip's colour: the app's own status tones (components/ui/badge.tsx).
export type Tone = "running" | "warning" | "success" | "destructive" | "secondary"

// stateTone is the chip's tone for a state: being made runs, awaiting review waits for the owner.
export function stateTone(kind: Kind, state: string): Tone {
  if (kind === "take") {
    if (takeWorking.includes(state)) return "running"

    return state === "failed" ? "destructive" : "secondary"
  }

  if (making(state)) return "running"
  if (state === "review") return "warning"
  if (state === "approved") return "success"

  return "destructive"
}

// studioPage is how many items a page of the Studio asks for.
const studioPage = 30

export type StudioQuery = { kind?: Kind; state?: Group; q?: string }

// useStudio reads the Studio a page at a time, as Cap's My Caps pages its caps from the server
// (caps-page-data.tsx; its layout learned, nothing copied): each page goes on after the cursor of the one before. It
// reads again every three seconds while an item shown is still made, so a card follows its take or video.
export function useStudio(query: StudioQuery) {
  return useInfiniteQuery({
    ...listStudioItemsInfiniteOptions({
      query: {
        limit: studioPage,
        ...(query.kind ? { kind: query.kind } : {}),
        ...(query.state ? { state: query.state } : {}),
        ...(query.q?.trim() ? { q: query.q.trim() } : {}),
      },
    }),
    initialPageParam: {},
    getNextPageParam: (last) => last.next_cursor,
    refetchInterval: (q) =>
      q.state.data?.pages.some((p) => p.data.some(inProgress)) ? 3000 : false,
  })
}

// useVideo reads a video, again every two seconds while a revision of it is made.
export function useVideo(id: string) {
  return useQuery({
    ...getVideoOptions({ path: { video_id: id } }),
    refetchInterval: (q) => (q.state.data?.revisions.some((r) => making(r.state)) ? 2000 : false),
  })
}

export const videoUrl = (video: string, rev: number) =>
  `/api/v1/videos/${encodeURIComponent(video)}/revisions/${rev}/video.mp4`

export const posterUrl = (video: string, rev: number) =>
  `/api/v1/videos/${encodeURIComponent(video)}/revisions/${rev}/thumbnail.jpg`

// made says a revision has its MP4: in review, approved, or asked for changes.
export const made = (r: VideoRevision) =>
  r.state === "review" || r.state === "approved" || r.state === "changes_requested"

// Stage is one stage of the making of a revision, as the video tab shows it.
export type Stage = {
  state: string
  name: string
  // done: passed; now: being made; waiting: to come; failed: where it failed; skipped: captions off.
  status: "done" | "now" | "waiting" | "failed" | "skipped"
}

const stageNames: Record<string, string> = {
  voicing: "Voice",
  composing: "Cut and fit",
  captioning: "Captions",
}

// stages are the stages of a revision, voice, cut and fit, then captions, each with where the revision stands, as
// aidemo's render job goes voice → record → captions → compose and says its stage (src/mcp/jobs.ts:64-67).
export function stages(r: VideoRevision): Stage[] {
  const order = ["voicing", "composing", "captioning"]
  const at =
    r.state === "failed"
      ? order.indexOf(r.failed_stage ?? "voicing")
      : r.state === "queued"
        ? -1
        : making(r.state)
          ? order.indexOf(r.state)
          : order.length

  return order.map((state, i) => {
    let status: Stage["status"]
    if (state === "captioning" && !r.captions) status = "skipped"
    else if (r.state === "failed" && i === at) status = "failed"
    else if (i < at) status = "done"
    else if (i === at) status = "now"
    else status = "waiting"

    return { state, name: stageNames[state] ?? state, status }
  })
}

// sceneDone says whether the stage being made has done a scene, by its progress (row 2; aidemo's scenesDone).
export function sceneDone(r: VideoRevision, scene: number): boolean {
  return r.progress !== undefined && scene < r.progress.scenes_done
}

// latest is a video's latest revision.
export const latest = (v: Video): VideoRevision | undefined => v.revisions.at(-1)
