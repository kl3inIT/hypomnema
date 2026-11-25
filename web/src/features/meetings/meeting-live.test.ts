import { afterEach, describe, expect, it, vi } from "vitest"

import type { MeetingDetail, MeetingLine } from "@/api/generated/types.gen"

import { captureSupport } from "./meeting-capture"
import { MeetingRecorder } from "./meeting-recorder"
import { withLine } from "./meeting-session"
import { MeetingStreamError, type MeetingSocketOptions } from "./meeting-socket"

function detail(): MeetingDetail {
  return {
    meeting: {
      id: "mtg_1",
      title: "Meeting",
      titled: false,
      people: [],
      file_name: "recording.wav",
      media_type: "audio/wav",
      size: 0,
      duration_ms: 0,
      state: "recording",
      source: "live",
      kind: "online",
      created_at: "2026-11-18T09:00:00Z",
      notes: { state: "waiting", summary: "", sections: [] },
      speakers: [],
      actions: 0,
      open_actions: 0,
    },
    lines: [],
    items: [],
    suggestions: [],
  }
}

function line(n: number, track: MeetingLine["track"], startMs: number): MeetingLine {
  return {
    n,
    track,
    start_ms: startMs,
    end_ms: startMs + 500,
    speaker: "1",
    heard: "1",
    text: `line ${n}`,
  }
}

describe("a meeting recorded live", () => {
  // A line kept is added by when it was said, not by its number: the tab's line said first reads first though the
  // microphone's was kept first; its voice joins the meeting the first time it speaks, the microphone online as You;
  // a line already there is not added twice (issue #820).
  it("adds the lines kept in the order they were said", () => {
    let d = withLine(detail(), line(1, "mic", 3_000))
    d = withLine(d, line(2, "tab", 1_000))
    d = withLine(d, line(2, "tab", 1_000))

    expect(d.lines.map((l) => l.n)).toEqual([2, 1])
    expect(d.meeting.speakers.map((s) => [s.track, s.label, s.name, s.owner])).toEqual([
      ["mic", "1", "You", true],
      ["tab", "1", "Speaker 1", false],
    ])
  })

  // Chromium on a computer shares a tab's sound; a phone, or another browser, records the microphone only (Nojoin's
  // featureDetect, MemoryOS's captureSupport).
  it("offers a tab's sound only where it can be shared", () => {
    const media = { getUserMedia: () => undefined, getDisplayMedia: () => undefined }
    const desktop =
      "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36"
    const phone =
      "Mozilla/5.0 (Linux; Android 14; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Mobile Safari/537.36"
    const firefox = "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"
    const nav = (userAgent: string) =>
      ({ userAgent, mediaDevices: media }) as unknown as Parameters<typeof captureSupport>[0]

    expect(captureSupport(nav(desktop))).toBe("sharedAudio")
    expect(captureSupport(nav(phone))).toBe("microphoneOnly")
    expect(captureSupport(nav(firefox))).toBe("microphoneOnly")
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  // A socket reconnects while its 30 s queue is full: the new socket's offset is the meeting's time of the first
  // frame it is sent, though more audio came while it opened; otherwise the track's lines and audio would move
  // against the other track's (issue #820, review of #821).
  it("starts a reconnected socket at the time of the first frame it sends", async () => {
    vi.useFakeTimers()
    const second = 24_000 * 2
    const startOf = new Map<ArrayBuffer, number>()
    let onChunk: (pcm: ArrayBuffer) => void = () => undefined
    let n = 0
    const speak = (seconds: number) => {
      for (let i = 0; i < seconds; i++) {
        const pcm = new ArrayBuffer(second)
        startOf.set(pcm, n++ * 1_000)
        onChunk(pcm)
      }
    }

    const opened: { offsetMs: number; sent: ArrayBuffer[]; options: MeetingSocketOptions }[] = []
    let letOpen: () => void = () => undefined
    const openSocket = (options: MeetingSocketOptions) => {
      const socket = {
        offsetMs: options.offsetMs,
        sent: [] as ArrayBuffer[],
        options,
      }
      opened.push(socket)
      const api = {
        send: (pcm: ArrayBuffer) => socket.sent.push(pcm),
        finish: () => Promise.resolve(),
        close: () => undefined,
      }
      if (opened.length === 1) return Promise.resolve(api)

      return new Promise<typeof api>((resolve) => {
        letOpen = () => resolve(api)
      })
    }

    const recorder = new MeetingRecorder({
      meetingId: "mtg_1",
      issueTicket: () => Promise.resolve("ticket"),
      onLine: () => undefined,
      openSocket,
      capture: (_stream, handlers) => {
        onChunk = handlers.onChunk
        handlers.onLevel(0)

        return Promise.resolve({ stop: () => undefined })
      },
      now: () => 0,
    })
    await recorder.start([{ track: "mic", stream: {} as MediaStream, offsetMs: 0 }])

    speak(1)
    opened[0]?.options.onFailure(new MeetingStreamError("connection"))
    // 31 s while the socket is away: the queue keeps the last 30.
    speak(31)
    await vi.advanceTimersByTimeAsync(1_000)
    expect(opened).toHaveLength(2)

    // More audio while the new socket opens, past the queue's 30 s.
    speak(6)
    letOpen()
    await vi.advanceTimersByTimeAsync(0)

    const reconnected = opened[1]
    if (!reconnected) throw new Error("no second socket")
    const first = reconnected.sent[0]
    if (!first) throw new Error("nothing was sent")
    expect(startOf.get(first)).toBe(reconnected.offsetMs)
    expect(reconnected.sent.map((pcm) => startOf.get(pcm))).toEqual(
      Array.from({ length: reconnected.sent.length }, (_, i) => reconnected.offsetMs + i * 1_000),
    )
    recorder.dispose()
  })

  // Pause sends nothing more and finishes the sockets; the clock stops. Resume opens a socket where the audio sent
  // ended, so the transcript and the saved audio join without the pause; Stop still ends it (issue #1153, MemoryOS's
  // "pausing stores what was said and drops audio").
  it("pauses, resumes on the same clock, then stops", async () => {
    const second = 24_000 * 2
    let onChunk: (pcm: ArrayBuffer) => void = () => undefined
    let onLevel: (level: number) => void = () => undefined
    // Each chunk is followed by its level reading, which publishes the clock, as the capture does.
    const speak = (seconds: number) => {
      for (let i = 0; i < seconds; i++) {
        onChunk(new ArrayBuffer(second))
        onLevel(0.1)
      }
    }
    const opened: { offsetMs: number; sent: number; finished: boolean }[] = []
    const recorder = new MeetingRecorder({
      meetingId: "mtg_1",
      issueTicket: () => Promise.resolve("ticket"),
      onLine: () => undefined,
      openSocket: (options) => {
        const socket = { offsetMs: options.offsetMs, sent: 0, finished: false }
        opened.push(socket)

        return Promise.resolve({
          send: () => void socket.sent++,
          finish: () => {
            socket.finished = true

            return Promise.resolve()
          },
          close: () => undefined,
        })
      },
      capture: (_stream, handlers) => {
        onChunk = handlers.onChunk
        onLevel = handlers.onLevel

        return Promise.resolve({ stop: () => undefined })
      },
      now: () => 0,
    })
    const phases: string[] = []
    recorder.subscribe(() => {
      const { phase } = recorder.getSnapshot()
      if (phases.at(-1) !== phase) phases.push(phase)
    })
    await recorder.start([{ track: "mic", stream: {} as MediaStream, offsetMs: 5_000 }])

    speak(2)
    await recorder.pause()
    expect(opened[0]).toMatchObject({ sent: 2, finished: true })
    speak(3)
    expect(opened[0]?.sent).toBe(2)
    expect(recorder.getSnapshot()).toMatchObject({ phase: "paused", elapsedMs: 7_000 })
    expect(recorder.getSnapshot().tracks[0]?.level).toBe(0)

    await recorder.resume()
    expect(opened).toHaveLength(2)
    expect(opened[1]?.offsetMs).toBe(7_000)
    speak(1)
    expect(opened[1]?.sent).toBe(1)
    expect(recorder.getSnapshot().elapsedMs).toBe(8_000)

    await recorder.pause()
    await recorder.stop()
    expect(phases).toEqual(["recording", "paused", "recording", "paused", "stopping", "stopped"])
    recorder.dispose()
  })
})

describe("muting the microphone while a meeting records", () => {
  const second = 24_000 * 2

  type FakeSocket = {
    track: string
    offsetMs: number
    sent: number
    finished: boolean
    open: () => void
  }

  // rig records a meeting's microphone and tab; a socket opens at once unless held, then opens when let go.
  async function rig(tracks: ("mic" | "tab")[], hold = () => false) {
    const handlers = new Map<
      MediaStream,
      { onChunk: (pcm: ArrayBuffer) => void; onLevel: (level: number) => void }
    >()
    const streams = new Map(
      tracks.map((t) => [t, { getAudioTracks: () => [] } as unknown as MediaStream]),
    )
    const opened: FakeSocket[] = []
    const recorder = new MeetingRecorder({
      meetingId: "mtg_1",
      issueTicket: () => Promise.resolve("ticket"),
      onLine: () => undefined,
      openSocket: (options) =>
        new Promise((resolve) => {
          const socket: FakeSocket = {
            track: options.track,
            offsetMs: options.offsetMs,
            sent: 0,
            finished: false,
            open: () =>
              resolve({
                send: () => void socket.sent++,
                finish: () => {
                  socket.finished = true

                  return Promise.resolve()
                },
                close: () => undefined,
              }),
          }
          opened.push(socket)
          if (!hold()) socket.open()
        }),
      capture: (stream, h) => {
        handlers.set(stream, h)

        return Promise.resolve({ stop: () => undefined })
      },
      now: () => 0,
    })
    await recorder.start(
      tracks.map((track) => ({ track, stream: streams.get(track)!, offsetMs: 0 })),
    )
    // speak sends a second of audio on every track, each followed by its level, as the capture does.
    const speak = (seconds: number) => {
      for (let i = 0; i < seconds; i++)
        for (const h of handlers.values()) {
          h.onChunk(new ArrayBuffer(second))
          h.onLevel(0.1)
        }
    }
    const sockets = (track: string) => opened.filter((s) => s.track === track)
    const level = (track: string) =>
      recorder.getSnapshot().tracks.find((t) => t.track === track)?.level

    return { recorder, speak, sockets, level }
  }

  // Muting finishes only the microphone's socket: the tab's goes on and the clock runs, nothing the microphone hears
  // is sent and its meter is still. Unmuting opens a microphone socket at the meeting's current time, so its words
  // after the mute land where they were said (issue #1155, MemoryOS's setMicMuted).
  it("stops only the microphone, then opens it again at the meeting's time", async () => {
    const { recorder, speak, sockets, level } = await rig(["mic", "tab"])
    speak(2)

    await recorder.setMicMuted(true)
    expect(sockets("mic")[0]).toMatchObject({ sent: 2, finished: true })
    expect(sockets("tab")[0]).toMatchObject({ sent: 2, finished: false })
    speak(3)
    expect(sockets("mic")).toHaveLength(1)
    expect(sockets("mic")[0]?.sent).toBe(2)
    expect(sockets("tab")[0]?.sent).toBe(5)
    expect(level("mic")).toBe(0)
    expect(level("tab")).toBe(0.1)
    expect(recorder.getSnapshot()).toMatchObject({ micMuted: true, elapsedMs: 5_000 })

    await recorder.setMicMuted(false)
    expect(sockets("mic")).toHaveLength(2)
    expect(sockets("mic")[1]?.offsetMs).toBe(5_000)
    speak(1)
    expect(sockets("mic")[1]?.sent).toBe(1)
    expect(level("mic")).toBe(0.1)
    expect(recorder.getSnapshot()).toMatchObject({ micMuted: false, elapsedMs: 6_000 })
    recorder.dispose()
  })

  // Resume opens the tab again but leaves a muted microphone off; unmuted later, it opens where the meeting is. A
  // meeting held in person, the microphone alone, records nothing while muted though its clock goes on.
  it("keeps the microphone muted across a pause", async () => {
    const online = await rig(["mic", "tab"])
    online.speak(1)
    await online.recorder.setMicMuted(true)
    await online.recorder.pause()
    online.speak(2)
    await online.recorder.resume()
    expect(online.sockets("tab")).toHaveLength(2)
    expect(online.sockets("mic")).toHaveLength(1)
    online.speak(2)
    expect(online.sockets("mic")[0]?.sent).toBe(1)
    await online.recorder.setMicMuted(false)
    expect(online.sockets("mic")[1]?.offsetMs).toBe(3_000)
    online.recorder.dispose()

    const inPerson = await rig(["mic"])
    await inPerson.recorder.setMicMuted(true)
    inPerson.speak(4)
    expect(inPerson.sockets("mic")[0]?.sent).toBe(0)
    expect(inPerson.recorder.getSnapshot()).toMatchObject({ phase: "recording", elapsedMs: 4_000 })
    inPerson.recorder.dispose()
  })

  // Muted and unmuted while its socket opens: that socket's offset was chosen before audio went unsent, so it is let
  // go and another opens at the meeting's current time (the reconnect rule above: no frame may go missing once a
  // socket's offset is chosen).
  it("does not use a socket opened before a mute", async () => {
    let holding = false
    const { recorder, speak, sockets } = await rig(["mic"], () => holding)
    speak(1)
    await recorder.setMicMuted(true)
    holding = true
    const unmuted = recorder.setMicMuted(false)
    await vi.waitFor(() => expect(sockets("mic")).toHaveLength(2))
    await recorder.setMicMuted(true)
    speak(2)
    const unmutedAgain = recorder.setMicMuted(false)
    holding = false
    sockets("mic")[1]!.open()
    await unmuted
    await unmutedAgain
    await vi.waitFor(() => expect(sockets("mic")).toHaveLength(3))
    expect(sockets("mic")[2]?.offsetMs).toBe(3_000)
    speak(1)
    expect(sockets("mic")[1]?.sent).toBe(0)
    expect(sockets("mic")[2]?.sent).toBe(1)
    recorder.dispose()
  })
})
