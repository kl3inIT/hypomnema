import { client } from "@/api/client"
import type { VideoMusic, VideoMusicTrack } from "@/api/generated/types.gen"
import { problemOf } from "@/lib/server-error"

// What the Music group of the Look works out (issue #910, rows 6a, 16 and 18): a track's upload and its refusals,
// the name an uploaded file is given, whether this browser plays a track, and the line a track or the library says.

// maxTrackName bounds a track's name, as the server does (videos.MaxMusicName).
const maxTrackName = 200

// trackName is the name a file is uploaded as: its own, without its extension, as `hypomnema videos music add` names
// it when no --name is given (clientcli/videomusic.go).
export function trackName(fileName: string) {
  const dot = fileName.lastIndexOf(".")
  const name = (dot > 0 ? fileName.slice(0, dot) : fileName).trim() || fileName.trim() || "Track"

  return Array.from(name).slice(0, maxTrackName).join("")
}

// UploadRefused is an upload the server refused, with the line to show.
class UploadRefused extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

// refusalLines are what an upload refused says when the answer has no message of the server's, as a proxy's page of
// HTML: the route's limits (clientapi/videomusic.go).
const refusalLines: Record<number, string> = {
  411: "The upload came without its length. Upload the file again from this page.",
  413: "A track is at most 64 MB.",
  429: "Another track is being uploaded. Try again in a few seconds.",
  507: "There is no room for this track: the library's 5 GB, or the server's disk, is full.",
}

// refusalOf is the line of an upload the server refused with status: the server's message when it sent one, else the
// line of its status.
export function refusalOf(status: number, error: unknown): UploadRefused {
  const problem = problemOf(error)
  const message = problem.code ? problem.message : (refusalLines[status] ?? problem.message)

  return new UploadRefused(status, message)
}

// uploadTrack sends a file to POST /api/v1/videos/music?name=…, outside the contract (row 6a): its bytes as they are,
// as application/octet-stream, with the length the browser gives a file. Through the web's client, so a refused
// session signs the page out as any request does.
export async function uploadTrack(file: File, signal?: AbortSignal): Promise<VideoMusicTrack> {
  const answer = await client.post<{ 201: VideoMusicTrack }, unknown, false>({
    url: "/videos/music",
    query: { name: trackName(file.name) },
    body: file,
    bodySerializer: null,
    headers: { "Content-Type": "application/octet-stream" },
    throwOnError: false,
    signal,
  })
  if (answer.error !== undefined || !answer.response?.ok) {
    throw refusalOf(answer.response?.status ?? 0, answer.error)
  }

  return answer.data as VideoMusicTrack
}

// audioUrl is where a track is played from, with ranges (ReadVideoMusicAudio).
export const audioUrl = (id: string) => `/api/v1/videos/music/${encodeURIComponent(id)}/audio`

// playable says whether this browser can play a track's type, as the media element's canPlayType says it
// (chromium.org/audio-video lists what Chromium plays): "" is no. A track it may play and then cannot shows the
// player's error state instead (row 18).
export function playable(mediaType: string, audio: Pick<HTMLAudioElement, "canPlayType">) {
  return audio.canPlayType(mediaType) !== ""
}

// amount is a number of bytes in the decimal units the library's quota is set in (5 GB).
export function amount(bytes: number) {
  if (bytes < 1000) return `${bytes} B`
  if (bytes < 1_000_000) return `${Math.round(bytes / 1000)} KB`
  if (bytes < 1_000_000_000) return `${+(bytes / 1_000_000).toFixed(1)} MB`

  // Rounded down, so a library nearly full never reads as full.
  return `${Math.floor(bytes / 10_000_000) / 100} GB`
}

// gainText is a track's gain from its level under the voice.
export const gainText = (db: number) => (db > 0 ? `+${db} dB` : `${db} dB`)

// Gone is a track a look names that is not in the library: deleted, with the name and length the look kept.
export type Gone = { track: string; name?: string; length_ms?: number }

// goneOf is the track the look's fields name that the library has no more, if any (row 16): left as it is, the server
// makes the video without it, with a warning; sent, it refuses it. stored is the music of the look the fields start
// from, which kept the track's name.
export function goneOf(
  track: string,
  tracks: VideoMusicTrack[] | undefined,
  stored: VideoMusic | undefined,
): Gone | undefined {
  if (track === "none" || !tracks || tracks.some((t) => t.id === track)) return undefined

  return stored?.track === track
    ? { track, name: stored.name, length_ms: stored.length_ms }
    : { track }
}
