// A view of one tab of the shared browser (issue #226): the WebSocket, and the canvas its frames are painted on.
//
// Adapted from T3 Code (packages/client-runtime/src/preview/serverBrowserStream.ts, MIT License, Copyright (c) 2026
// T3 Tools Inc.; web/THIRD_PARTY_NOTICES.md). Changed: a ticket is asked for at each connect, as the terminal page
// does, in place of T3 Code's stream access; the messages are the control plane's (api/openapi.yaml,
// OpenBrowserView), which has no dialogs, file choosers, downloads, clipboard or agent pointer.
import "@/api/client"
import { openBrowserView } from "@/api/generated/sdk.gen"

import {
  retryIn,
  socketUrl,
  tabGone,
  type Controller,
  type ViewInput,
  type Viewport,
} from "./stream"

export type ViewEvents = {
  onConnected: (connected: boolean) => void
  onFrame: (jpeg: ArrayBuffer) => void
  onViewport: (viewport: Viewport) => void
  onControl: (controller: Controller) => void
  onUrl: (url: string) => void
  // The tab is not open any more: nothing is tried again.
  onGone: () => void
  // The control plane refused a ticket, in its own words.
  onRefused: (message: string) => void
}

export type ViewClient = {
  // send gives the tab an input; false when it was not sent. Only taking control is sent without control.
  send: (input: ViewInput) => boolean
  stop: () => void
}

const ack = JSON.stringify({ type: "ack" })

export function createViewClient(
  tab: string,
  events: ViewEvents,
  errorText: (e: unknown) => string,
): ViewClient {
  let stopped = false
  let socket: WebSocket | null = null
  let retry: ReturnType<typeof setTimeout> | null = null
  let failures = 0
  let controller: Controller | null = null
  let size: Extract<ViewInput, { type: "resize" }> | null = null

  const again = () => {
    if (!stopped) retry = setTimeout(() => void connect(), retryIn(failures++))
  }

  const connect = async () => {
    if (stopped) return

    let ticket: string
    try {
      ticket = (await openBrowserView({ path: { tab_id: tab } })).data.ticket
    } catch (error) {
      if (stopped) return
      // A control plane with no browser, or one that could not start it, says so; that is not tried again.
      stopped = true
      events.onRefused(errorText(error))

      return
    }
    if (stopped) return

    const ws = new WebSocket(socketUrl(window.location, ticket))
    ws.binaryType = "arraybuffer"
    socket = ws
    let opened = false

    ws.addEventListener("open", () => {
      if (socket !== ws) return
      opened = true
      events.onConnected(true)
      // The tab fills this pane from the start, and again after a reconnect.
      if (size) ws.send(JSON.stringify(size))
    })
    ws.addEventListener("message", (event) => {
      if (socket !== ws) return

      if (event.data instanceof ArrayBuffer) {
        failures = 0
        // Acknowledged before it is painted: the next frame is on its way while this one is decoded.
        ws.send(ack)
        events.onFrame(event.data)

        return
      }
      if (typeof event.data !== "string") return

      let message: unknown
      try {
        message = JSON.parse(event.data)
      } catch {
        return
      }
      if (typeof message !== "object" || message === null) return

      const { type, width, height, controller: who, url } = message as Record<string, unknown>
      if (type === "viewport" && typeof width === "number" && typeof height === "number") {
        failures = 0
        events.onViewport({ width, height })
      } else if (type === "control" && (who === "agent" || who === "you")) {
        controller = who
        events.onControl(who)
      } else if (type === "url" && typeof url === "string") {
        events.onUrl(url)
      }
    })
    ws.addEventListener("close", (event) => {
      if (socket !== ws) return
      socket = null
      controller = null
      if (opened) events.onConnected(false)
      if (stopped) return

      if (event.code === tabGone) {
        stopped = true
        events.onGone()

        return
      }

      again()
    })
    ws.addEventListener("error", () => ws.close())
  }

  void connect()

  return {
    send: (input) => {
      // The pane's size is kept for the next connection and needs no control: the tab fills it whoever drives.
      if (input.type === "resize") size = input
      if (socket?.readyState !== WebSocket.OPEN) return false
      if (input.type !== "takeControl" && input.type !== "resize" && controller !== "you")
        return false
      socket.send(JSON.stringify(input))

      return true
    },
    stop: () => {
      stopped = true
      if (retry !== null) clearTimeout(retry)
      const ws = socket
      socket = null
      controller = null
      ws?.close()
    },
  }
}

export type FramePainter = { paint: (jpeg: ArrayBuffer) => void; stop: () => void }

// createFramePainter draws frames on a canvas, one decode at a time, keeping only the newest frame that waits: a
// slow page skips frames rather than fall behind.
export function createFramePainter(
  canvas: HTMLCanvasElement,
  onPainted?: () => void,
): FramePainter {
  const context = canvas.getContext("2d")
  let stopped = false
  let decoding = false
  let waiting: ArrayBuffer | null = null

  const draw = (jpeg: ArrayBuffer) => {
    decoding = true
    void createImageBitmap(new Blob([jpeg], { type: "image/jpeg" }))
      .then((bitmap) => {
        if (stopped || !context) {
          bitmap.close()

          return
        }

        if (canvas.width !== bitmap.width || canvas.height !== bitmap.height) {
          canvas.width = bitmap.width
          canvas.height = bitmap.height
        }
        context.drawImage(bitmap, 0, 0)
        bitmap.close()
        onPainted?.()
      })
      .catch(() => undefined)
      .finally(() => {
        decoding = false
        const next = waiting
        waiting = null
        if (next && !stopped) draw(next)
      })
  }

  return {
    paint: (jpeg) => {
      if (stopped) return
      if (decoding) waiting = jpeg
      else draw(jpeg)
    },
    stop: () => {
      stopped = true
      waiting = null
    },
  }
}
