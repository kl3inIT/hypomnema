import type { MeetingLine, MeetingLiveTrack } from "@/api/generated/types.gen"

// The WebSocket one track of a meeting is recorded on (issue #820), adapted from MemoryOS's meeting-socket.ts: it
// opens with a ticket, sends PCM16 24 kHz mono in binary frames and one {"type":"end"}, and hears ready, preview,
// line (kept), finished and error {code}.

const openTimeoutMs = 8_000
// finishTimeoutMs bounds the wait for the last words after the end (MemoryOS's FINISH_TIMEOUT_MS).
const finishTimeoutMs = 20_000

// terminalCodes are the codes after which connecting again cannot help.
export const terminalCodes = new Set([
  "ended",
  "invalid",
  "invalid_audio",
  "invalid_message",
  "too_long",
  "unavailable",
])

export class MeetingStreamError extends Error {
  readonly code: string

  constructor(code: string) {
    super(`The meeting's stream failed: ${code}`)
    this.name = "MeetingStreamError"
    this.code = code
  }
}

export type MeetingSocket = {
  send: (pcm: ArrayBuffer) => void
  // finish sends the end and resolves once the last words are kept, or after a while.
  finish: () => Promise<void>
  close: () => void
}

export type MeetingSocketOptions = {
  meetingId: string
  track: MeetingLiveTrack
  offsetMs: number
  ticket: string
  onPreview: (speaker: string, text: string) => void
  onLine: (line: MeetingLine) => void
  // onFailure is a server's error or a close that was not asked for, while recording.
  onFailure: (error: MeetingStreamError) => void
  location?: Pick<Location, "origin" | "protocol">
}

function meetingStreamUrl(
  options: Pick<MeetingSocketOptions, "meetingId" | "track" | "offsetMs" | "ticket">,
  location: Pick<Location, "origin" | "protocol"> = window.location,
) {
  const url = new URL(
    `/api/v1/meetings/${encodeURIComponent(options.meetingId)}/stream/${encodeURIComponent(options.ticket)}`,
    location.origin,
  )
  url.protocol = location.protocol === "https:" ? "wss:" : "ws:"
  url.searchParams.set("track", options.track)
  url.searchParams.set("offset", String(Math.max(0, Math.floor(options.offsetMs))))

  return url.toString()
}

// openMeetingSocket opens the socket and resolves once the server opened the provider's stream (ready).
export function openMeetingSocket(options: MeetingSocketOptions): Promise<MeetingSocket> {
  return new Promise((resolve, reject) => {
    const socket = new WebSocket(meetingStreamUrl(options, options.location))
    socket.binaryType = "arraybuffer"
    let ready = false
    let settled = false
    let finishing = false
    let finished = false
    let reported = false
    let settleFinish: (() => void) | undefined

    const fail = (code: string) => {
      if (!settled) {
        settled = true
        window.clearTimeout(openTimer)
        reject(new MeetingStreamError(code))

        return
      }
      if (!finishing && !reported) {
        reported = true
        options.onFailure(new MeetingStreamError(code))
      }
      settleFinish?.()
    }
    const openTimer = window.setTimeout(() => {
      if (settled) return
      socket.close()
      fail("connection")
    }, openTimeoutMs)

    const api: MeetingSocket = {
      send(pcm) {
        if (ready && !finishing && socket.readyState === WebSocket.OPEN) socket.send(pcm)
      },
      finish() {
        if (finishing) return Promise.resolve()
        finishing = true
        if (socket.readyState !== WebSocket.OPEN) return Promise.resolve()
        socket.send(JSON.stringify({ type: "end" }))

        return new Promise<void>((done) => {
          const timer = window.setTimeout(() => settleFinish?.(), finishTimeoutMs)
          settleFinish = () => {
            window.clearTimeout(timer)
            settleFinish = undefined
            done()
          }
          if (finished) settleFinish()
        })
      },
      close() {
        finishing = true
        if (socket.readyState === WebSocket.CONNECTING || socket.readyState === WebSocket.OPEN)
          socket.close()
      },
    }

    socket.onmessage = (event: MessageEvent) => {
      if (typeof event.data !== "string") return
      let message: Record<string, unknown>
      try {
        const parsed: unknown = JSON.parse(event.data)
        if (!parsed || typeof parsed !== "object") return
        message = parsed as Record<string, unknown>
      } catch {
        return
      }

      switch (message.type) {
        case "ready":
          if (settled) return
          ready = true
          settled = true
          window.clearTimeout(openTimer)
          resolve(api)

          return
        case "preview":
          if (typeof message.speaker === "string" && typeof message.text === "string")
            options.onPreview(message.speaker, message.text)

          return
        case "line":
          if (message.line && typeof message.line === "object")
            options.onLine(message.line as MeetingLine)

          return
        case "finished":
          finished = true
          settleFinish?.()

          return
        case "error":
          fail(typeof message.code === "string" ? message.code : "unavailable")
      }
    }
    socket.onerror = () => {
      if (!settled) fail("connection")
    }
    socket.onclose = () => {
      window.clearTimeout(openTimer)
      if (!finished) fail("connection")
    }
  })
}
