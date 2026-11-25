import type { MeetingLine, MeetingLiveTrack } from "@/api/generated/types.gen"

import { startStreamCapture, type AudioCapture } from "./audio-capture"
import {
  MeetingStreamError,
  openMeetingSocket,
  terminalCodes,
  type MeetingSocket,
} from "./meeting-socket"

// Recording a meeting's tracks in the browser (issue #820), adapted from MemoryOS's MeetingRecorder
// (web/src/features/meetings/meeting-recorder.ts). Each track has its own capture and socket; while a socket reconnects its audio is queued, at most 30 seconds, and the new
// socket starts at the meeting's time of its first unsent sample, so the clock goes on. Once that time is in the new
// socket's address nothing is dropped from the queue until the socket is ready (or fails, at most openTimeoutMs
// later): a frame dropped then would move every later frame earlier on the meeting's clock. Pausing (issue #1153) drops
// the audio and finishes every socket, as MemoryOS's pause does and Meetily's recording state drops audio while paused
// (frontend/src-tauri/src/audio/recording_state.rs); resuming opens new sockets where the audio sent ends, so the
// transcript and the saved audio join without the pause, and the clock, counted from the audio, stops while paused.
// A socket kept open would not do: the server ends a track that has had no audio for 60 seconds. Muting the microphone
// (issue #1155, MemoryOS's setMicMuted after anarlog's set_mic_muted) finishes only its socket while the tab records
// on: what it hears meanwhile is never sent, but counts on its clock, so its socket opens again at the meeting's
// current time, and the server keeps the gap silent. Resume leaves a muted microphone closed, as MemoryOS's connect
// does.

// bytesPerMs is PCM16 mono at 24 kHz.
const bytesPerMs = 48
const maxQueuedBytes = 30_000 * bytesPerMs
const reconnectDelaysMs = [1_000, 2_000, 5_000, 10_000, 20_000]
// quietLevel is Nojoin's hint that a shared tab has no sound: a level under 6 on its 0 to 100 scale (RMS × 180).
const quietLevel = 6 / 180
const quietAfterMs = 20_000

type RecorderPhase = "recording" | "paused" | "stopping" | "stopped" | "failed"

type TrackSnapshot = {
  track: MeetingLiveTrack
  level: number
  connected: boolean
  reconnecting: boolean
  // quiet: the shared tab has been silent long enough to think its sound is not shared.
  quiet: boolean
  // ended: the tab is no longer shared.
  ended: boolean
}

export type RecorderSnapshot = {
  phase: RecorderPhase
  elapsedMs: number
  tracks: TrackSnapshot[]
  previews: Partial<Record<MeetingLiveTrack, { speaker: string; text: string }>>
  // micMuted: the microphone is off; the tab keeps recording and the clock keeps running.
  micMuted: boolean
  error?: string
}

export type RecorderDependencies = {
  meetingId: string
  issueTicket: (track: MeetingLiveTrack) => Promise<string>
  onLine: (line: MeetingLine) => void
  openSocket?: typeof openMeetingSocket
  capture?: typeof startStreamCapture
  now?: () => number
}

type Pipe = {
  track: MeetingLiveTrack
  stream: MediaStream
  startOffsetMs: number
  capture?: AudioCapture
  socket?: MeetingSocket
  connecting: boolean
  // handshaking: the new socket's offset is chosen; the queue keeps its head until the socket is ready.
  handshaking: boolean
  attempt: number
  timer?: number
  queue: ArrayBuffer[]
  queuedBytes: number
  capturedBytes: number
  deliveredBytes: number
  level: number
  lastLoudAt: number
  ended: boolean
  muted: boolean
  // mutes counts the times the pipe was muted: a socket whose offset was chosen before a mute is not used.
  mutes: number
}

export class MeetingRecorder {
  private readonly pipes: Pipe[] = []
  private readonly listeners = new Set<() => void>()
  private readonly deps: Required<RecorderDependencies>
  private snapshot: RecorderSnapshot = {
    phase: "recording",
    elapsedMs: 0,
    tracks: [],
    previews: {},
    micMuted: false,
  }
  private wakeLock?: { release: () => Promise<void> }

  constructor(deps: RecorderDependencies) {
    this.deps = {
      openSocket: openMeetingSocket,
      capture: startStreamCapture,
      now: () => Date.now(),
      ...deps,
    }
  }

  subscribe = (listener: () => void) => {
    this.listeners.add(listener)

    return () => {
      this.listeners.delete(listener)
    }
  }

  getSnapshot = () => this.snapshot

  // start records every track from offsetMs, the meeting's time where it goes on.
  async start(sources: { track: MeetingLiveTrack; stream: MediaStream; offsetMs: number }[]) {
    const added: Pipe[] = []
    for (const source of sources) {
      const pipe: Pipe = {
        track: source.track,
        stream: source.stream,
        startOffsetMs: source.offsetMs,
        connecting: false,
        handshaking: false,
        attempt: 0,
        queue: [],
        queuedBytes: 0,
        capturedBytes: 0,
        deliveredBytes: 0,
        level: 0,
        lastLoudAt: this.deps.now(),
        ended: false,
        muted: false,
        mutes: 0,
      }
      this.pipes.push(pipe)
      added.push(pipe)
      this.watchEnd(pipe)
      pipe.capture = await this.deps.capture(pipe.stream, {
        onChunk: (pcm) => this.chunk(pipe, pcm),
        onLevel: (level) => this.level(pipe, level),
      })
    }
    await Promise.all(added.map((pipe) => this.connect(pipe)))
    if (!this.wakeLock) void this.lockScreen()
    this.publish()
  }

  // pause keeps what was said and sends nothing more until resume: the sockets are finished, and audio is dropped.
  async pause() {
    if (this.snapshot.phase !== "recording") return
    for (const pipe of this.pipes) pipe.level = 0
    this.update({ phase: "paused", previews: {} })
    await Promise.all(this.pipes.map((pipe) => this.closeSocket(pipe)))
    this.publish()
  }

  // resume opens each shared track's socket again where its audio sent ends.
  async resume() {
    if (this.snapshot.phase !== "paused") return
    const now = this.deps.now()
    for (const pipe of this.pipes) pipe.lastLoudAt = now
    this.update({ phase: "recording" })
    await Promise.all(this.pipes.filter((pipe) => !pipe.ended).map((pipe) => this.connect(pipe)))
    this.publish()
  }

  // setMicMuted turns the microphone off or back on while the tab keeps recording: muted, its socket is finished and
  // nothing it hears is sent; unmuted, a socket opens at the meeting's current time (MemoryOS's setMicMuted).
  async setMicMuted(muted: boolean) {
    const mic = this.pipes.find((pipe) => pipe.track === "mic")
    if (!mic || mic.muted === muted) return
    mic.muted = muted
    if (muted) mic.mutes++
    mic.level = 0
    // Audio still waiting for a reconnect is dropped, and its time is kept.
    mic.deliveredBytes += mic.queuedBytes
    mic.queue = []
    mic.queuedBytes = 0
    this.update({ micMuted: muted, previews: { ...this.snapshot.previews, mic: undefined } })
    if (muted) await this.closeSocket(mic)
    else await this.connect(mic)
    this.publish()
  }

  // stop turns the microphone and the tab off at once, then waits while what they sent is kept.
  async stop() {
    if (this.snapshot.phase === "stopping" || this.snapshot.phase === "stopped") return
    this.update({ phase: "stopping" })
    for (const pipe of this.pipes) {
      pipe.capture?.stop()
      pipe.capture = undefined
    }
    await Promise.all(this.pipes.map((pipe) => this.closeSocket(pipe)))
    this.release()
    this.update({ phase: "stopped", previews: {} })
  }

  // dispose lets everything go at once.
  dispose() {
    for (const pipe of this.pipes) {
      pipe.socket?.close()
      pipe.socket = undefined
    }
    this.release()
    this.listeners.clear()
  }

  private release() {
    for (const pipe of this.pipes) {
      window.clearTimeout(pipe.timer)
      pipe.capture?.stop()
      pipe.capture = undefined
      pipe.queue = []
      pipe.queuedBytes = 0
    }
    void this.wakeLock?.release().catch(() => undefined)
    this.wakeLock = undefined
  }

  private chunk(pipe: Pipe, pcm: ArrayBuffer) {
    if (this.snapshot.phase !== "recording" || pipe.ended) return
    pipe.capturedBytes += pcm.byteLength
    if (pipe.muted) {
      // Never sent, but the clock goes on, so the microphone unmuted speaks at the meeting's true time.
      pipe.deliveredBytes += pcm.byteLength

      return
    }
    if (pipe.socket) {
      pipe.socket.send(pcm)
      pipe.deliveredBytes += pcm.byteLength

      return
    }
    pipe.queue.push(pcm)
    pipe.queuedBytes += pcm.byteLength
    if (!pipe.handshaking) this.trimQueue(pipe)
  }

  private trimQueue(pipe: Pipe) {
    while (pipe.queuedBytes > maxQueuedBytes) {
      const dropped = pipe.queue.shift()
      if (!dropped) break
      pipe.queuedBytes -= dropped.byteLength
      // Audio dropped still moves the clock, so later speech keeps its true time.
      pipe.deliveredBytes += dropped.byteLength
    }
  }

  private level(pipe: Pipe, level: number) {
    pipe.level = this.snapshot.phase === "paused" || pipe.muted ? 0 : level
    if (level >= quietLevel) pipe.lastLoudAt = this.deps.now()
    this.publish()
  }

  private async connect(pipe: Pipe): Promise<void> {
    if (pipe.socket || pipe.connecting || pipe.muted || this.snapshot.phase !== "recording") return
    pipe.connecting = true
    pipe.timer = undefined
    this.publish()
    let unmutedMeanwhile = false
    try {
      const ticket = await this.deps.issueTicket(pipe.track)
      pipe.handshaking = true
      const mutes = pipe.mutes
      const socket = await this.deps.openSocket({
        meetingId: this.deps.meetingId,
        track: pipe.track,
        offsetMs: pipe.startOffsetMs + pipe.deliveredBytes / bytesPerMs,
        ticket,
        onPreview: (speaker, text) =>
          this.update({
            previews: {
              ...this.snapshot.previews,
              [pipe.track]: text ? { speaker, text } : undefined,
            },
          }),
        onLine: (line) => {
          this.update({ previews: { ...this.snapshot.previews, [pipe.track]: undefined } })
          this.deps.onLine(line)
        },
        onFailure: (error) => this.socketFailed(pipe, error),
      })
      if (this.snapshot.phase !== "recording" || pipe.mutes !== mutes) {
        socket.close()
        // Muted and unmuted while it opened: the audio muted meanwhile was not sent, so a socket opens again at the
        // meeting's current time once this one is let go.
        unmutedMeanwhile = this.snapshot.phase === "recording" && !pipe.muted
      } else {
        pipe.socket = socket
        pipe.attempt = 0
        for (const pcm of pipe.queue) {
          socket.send(pcm)
          pipe.deliveredBytes += pcm.byteLength
        }
        pipe.queue = []
        pipe.queuedBytes = 0
      }
    } catch (error) {
      this.socketFailed(
        pipe,
        error instanceof MeetingStreamError ? error : new MeetingStreamError("connection"),
      )
    } finally {
      pipe.handshaking = false
      this.trimQueue(pipe)
      pipe.connecting = false
      this.publish()
    }
    if (unmutedMeanwhile) await this.connect(pipe)
  }

  private socketFailed(pipe: Pipe, error: MeetingStreamError) {
    pipe.socket = undefined
    if (this.snapshot.phase !== "recording" || pipe.muted) return
    if (terminalCodes.has(error.code) || pipe.attempt >= reconnectDelaysMs.length) {
      this.release()
      for (const other of this.pipes) other.socket?.close()
      this.update({ phase: "failed", error: error.code, previews: {} })

      return
    }
    const delay = reconnectDelaysMs[pipe.attempt++]
    window.clearTimeout(pipe.timer)
    pipe.timer = window.setTimeout(() => void this.connect(pipe), delay)
    this.publish()
  }

  private async closeSocket(pipe: Pipe) {
    window.clearTimeout(pipe.timer)
    pipe.timer = undefined
    const socket = pipe.socket
    pipe.socket = undefined
    if (!socket) return
    await socket.finish()
    socket.close()
  }

  // watchEnd notices the person stopped sharing the tab: its socket ends with its last words, the microphone goes on.
  private watchEnd(pipe: Pipe) {
    if (pipe.track !== "tab") return
    for (const track of pipe.stream.getAudioTracks()) {
      track.addEventListener("ended", () => {
        pipe.ended = true
        void this.closeSocket(pipe).then(() => this.publish())
        this.publish()
      })
    }
  }

  // lockScreen keeps the screen on while recording; denied, the screen may only dim (Nojoin, MemoryOS).
  private async lockScreen() {
    const wake = (
      navigator as Navigator & {
        wakeLock?: { request: (type: "screen") => Promise<{ release: () => Promise<void> }> }
      }
    ).wakeLock
    if (!wake) return
    try {
      this.wakeLock = await wake.request("screen")
    } catch {
      // Best effort.
    }
  }

  private update(change: Partial<RecorderSnapshot>) {
    this.snapshot = { ...this.snapshot, ...change }
    this.publish()
  }

  private publish() {
    const now = this.deps.now()
    const mic = this.pipes.find((pipe) => pipe.track === "mic") ?? this.pipes[0]
    this.snapshot = {
      ...this.snapshot,
      elapsedMs: mic ? mic.startOffsetMs + mic.capturedBytes / bytesPerMs : 0,
      tracks: this.pipes.map((pipe) => ({
        track: pipe.track,
        level: pipe.level,
        connected: !!pipe.socket,
        reconnecting:
          !pipe.socket &&
          (pipe.connecting || pipe.timer !== undefined) &&
          this.snapshot.phase === "recording",
        quiet:
          pipe.track === "tab" &&
          !pipe.ended &&
          this.snapshot.phase === "recording" &&
          now - pipe.lastLoudAt >= quietAfterMs,
        ended: pipe.ended,
      })),
    }
    for (const listener of this.listeners) listener()
  }
}
