import type { Meeting, MeetingSpeaker } from "@/api/generated/types.gen"

// What the Meetings pages compute from a meeting (issue #721), kept apart from the screens so that it is tested
// without them.

// Day is the group a meeting is listed under, as Granola, Otter, Hyprnote and MemoryOS list meetings by day.
export type Day = "Today" | "Yesterday" | "Earlier"

const dayMs = 24 * 60 * 60 * 1000

function startOfDay(t: number) {
  const d = new Date(t)
  d.setHours(0, 0, 0, 0)

  return d.getTime()
}

function dayOf(iso: string, now = Date.now()): Day {
  const at = new Date(iso).getTime()
  const today = startOfDay(now)

  if (at >= today) return "Today"
  if (at >= today - dayMs) return "Yesterday"

  return "Earlier"
}

// byDay groups meetings, already newest first, under Today, Yesterday and Earlier, leaving out an empty group.
export function byDay<T extends { created_at: string }>(meetings: T[], now = Date.now()) {
  const groups: { day: Day; meetings: T[] }[] = []

  for (const m of meetings) {
    const day = dayOf(m.created_at, now)
    const last = groups.at(-1)
    if (last?.day === day) last.meetings.push(m)
    else groups.push({ day, meetings: [m] })
  }

  return groups
}

// clock is a time in the recording as m:ss, or h:mm:ss past an hour.
export function clock(ms: number) {
  const s = Math.max(0, Math.floor(ms / 1000))
  const h = Math.floor(s / 3600)
  const m = Math.floor(s / 60) % 60
  const sec = String(s % 60).padStart(2, "0")

  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${sec}` : `${m}:${sec}`
}

// length says how long a recording is: "42 min", "1 h 5 min", "40 s".
function length(ms: number) {
  if (ms <= 0) return ""
  if (ms < 60_000) return `${Math.round(ms / 1000)} s`

  const minutes = Math.round(ms / 60_000)
  if (minutes < 60) return `${minutes} min`

  return minutes % 60 ? `${Math.floor(minutes / 60)} h ${minutes % 60} min` : `${minutes / 60} h`
}

// Stage is where a meeting stands in the steps the page shows: uploaded → transcribed → writing notes (Nojoin's
// stage texts, Meetily, MemoryOS), and how it ended.
export type Stage = "transcribing" | "writing" | "ready" | "failed" | "notes_failed"

export function stageOf(m: Meeting): Stage {
  if (m.state === "transcribing") return "transcribing"
  if (m.state === "failed") return "failed"
  if (m.notes.state === "failed") return "notes_failed"
  if (m.notes.state === "ready") return "ready"

  return "writing"
}

export const stageNames: Record<Stage, string> = {
  transcribing: "Transcribing",
  writing: "Writing notes",
  ready: "Ready",
  failed: "Failed",
  notes_failed: "Notes failed",
}

// working says the server is still working on the meeting, so the page reads it again until it is done.
export function working(m: Meeting | undefined) {
  if (!m) return false

  const stage = stageOf(m)

  return stage === "transcribing" || stage === "writing"
}

export type Step = { name: string; state: "done" | "now" | "waiting" | "failed" }

// stepsOf is the meeting's steps: uploaded, transcribed with how many speakers, notes written.
export function stepsOf(m: Meeting): Step[] {
  const stage = stageOf(m)
  const speakers = m.speakers.length
  const heard =
    m.state === "transcribed"
      ? `Transcribed · ${speakers} ${speakers === 1 ? "speaker" : "speakers"}`
      : "Transcribed"

  return [
    { name: "Uploaded", state: "done" },
    {
      name: heard,
      state: stage === "transcribing" ? "now" : stage === "failed" ? "failed" : "done",
    },
    {
      name: stage === "ready" ? "Notes written" : "Writing notes",
      state:
        stage === "writing"
          ? "now"
          : stage === "notes_failed"
            ? "failed"
            : stage === "ready"
              ? "done"
              : "waiting",
    },
  ]
}

// speakerTone is a voice's color, by the order it first spoke in (the tokens --speaker-1 to 6).
const tones = [
  "text-speaker-1",
  "text-speaker-2",
  "text-speaker-3",
  "text-speaker-4",
  "text-speaker-5",
  "text-speaker-6",
]
const dots = [
  "bg-speaker-1",
  "bg-speaker-2",
  "bg-speaker-3",
  "bg-speaker-4",
  "bg-speaker-5",
  "bg-speaker-6",
]

export function speakerTone(speakers: MeetingSpeaker[], label: string) {
  const i = Math.max(
    0,
    speakers.findIndex((s) => s.label === label),
  )

  return { text: tones[i % tones.length], dot: dots[i % dots.length] }
}

// percent is a voice's share of the talk, rounded.
export function percent(share: number) {
  return `${Math.round(share * 100)}%`
}

// sameName compares two names as the server does: without case and extra spaces.
export function sameName(a: string, b: string) {
  const fold = (s: string) => s.normalize("NFC").toLowerCase().trim().split(/\s+/).join(" ")

  return fold(a) === fold(b)
}

// speakersText names the voices of a card: "Laura, Kai, Speaker 3".
export function speakersText(m: Meeting) {
  return m.speakers.map((s) => s.name).join(", ")
}

// actionsText says a meeting's actions: "3 open of 5", "All 4 done", "None".
export function actionsText(m: Meeting) {
  if (m.actions === 0) return "None"
  if (m.open_actions === 0) return `All ${m.actions} done`

  return `${m.open_actions} open of ${m.actions}`
}

// fromText says where the meeting came from and who heard it: "Upload · Soniox".
export function fromText(m: Meeting) {
  const provider = m.provider ? ` · ${providerName(m.provider)}` : ""

  return `Upload${provider}`
}

export function providerName(p: string) {
  if (p === "soniox") return "Soniox"
  if (p === "elevenlabs") return "ElevenLabs"

  return p
}

// whenText is when a meeting was uploaded and how long it is: "Today, 09:30 · 42 min".
export function whenText(m: Meeting, now = Date.now()) {
  const at = new Date(m.created_at)
  const time = at.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
  const day = dayOf(m.created_at, now)
  const date =
    day === "Earlier"
      ? at.toLocaleDateString(undefined, { month: "short", day: "numeric" })
      : `${day}, ${time}`
  const long = length(m.duration_ms)

  return long ? `${date} · ${long}` : date
}

// The languages offered on upload; the provider finds any other by itself.
export const languages = [
  { code: "auto", name: "Find it from the recording" },
  { code: "vi", name: "Vietnamese" },
  { code: "en", name: "English" },
  { code: "ja", name: "Japanese" },
  { code: "zh", name: "Chinese" },
  { code: "ko", name: "Korean" },
  { code: "fr", name: "French" },
  { code: "de", name: "German" },
  { code: "es", name: "Spanish" },
]

// listOf reads a list the owner typed, separated by commas or new lines.
export function listOf(text: string) {
  return text
    .split(/[,\n]/)
    .map((s) => s.trim())
    .filter(Boolean)
}

// maxRecording is the largest recording the server takes: 128 MB.
export const maxRecording = 128 * 1024 * 1024
