// A view of the phone (issue #417, step 3): the WebSocket, and the decoder that paints its H.264 frames on a canvas.
//
// Adapted from T3 Code's Android stream client (packages/client-runtime/src/device/stream.ts: connectAndroid,
// configureDecoder, decode, requestKeyframe; MIT License, Copyright (c) 2026 T3 Tools Inc.). Changed: a ticket is
// asked for at each connect, as the browser view does (../browser/stream-client.ts), in place of T3 Code's hub
// access; the socket also carries who controls and the phone's state; and there is no WebRTC, MJPEG or input-only
// socket, which the control plane does not serve.
import "@/api/client"
import { openDeviceView } from "@/api/generated/sdk.gen"
import { retryIn } from "@/features/browser/stream"

import {
  avcCodecString,
  parseSemuPacket,
  phoneGone,
  scanAccessUnit,
  socketUrl,
  type Controller,
  type PhoneInput,
  type PhoneState,
  type Screen,
} from "./stream"

export type PhoneEvents = {
  onConnected: (connected: boolean) => void
  onControl: (controller: Controller) => void
  onState: (state: PhoneState, reason: string) => void
  onScreen: (screen: Screen) => void
  // A frame was painted.
  onPainted: () => void
  // The stream could not start on the phone, or this browser cannot decode it: it is tried again by itself.
  onTrouble: (reason: string) => void
  // The phone cannot be shown, in the control plane's words: nothing is tried again.
  onGone: (reason: string) => void
}

export type PhoneClient = {
  // send gives the phone an input; false when it was not sent. Only taking control and a key frame asked for are
  // sent without control.
  send: (input: PhoneInput) => boolean
  stop: () => void
}

// softQueue is how many frames may wait for the decoder before it starts again from a key frame (T3 Code's
// SOFT_DECODE_QUEUE).
const softQueue = 8
const frameMicros = 16_667

export function createPhoneClient(
  canvas: HTMLCanvasElement,
  events: PhoneEvents,
  errorText: (e: unknown) => string,
): PhoneClient {
  let stopped = false
  let socket: WebSocket | null = null
  let retry: ReturnType<typeof setTimeout> | null = null
  let failures = 0
  let controller: Controller | null = null
  let decoder: VideoDecoder | null = null
  let configuring = false
  let awaitingKey = true
  let epoch = 0
  let timestamp = 0
  const context = canvas.getContext("2d")

  const send = (input: PhoneInput) => {
    if (socket?.readyState !== WebSocket.OPEN) return false
    socket.send(JSON.stringify(input))

    return true
  }

  const askKeyFrame = () => send({ type: "reset-video" })

  const closeDecoder = () => {
    epoch++
    try {
      decoder?.close()
    } catch {
      // Closed already.
    }
    decoder = null
    configuring = false
    awaitingKey = true
  }

  const paint = (frame: VideoFrame) => {
    if (!context) return
    if (canvas.width !== frame.displayWidth || canvas.height !== frame.displayHeight) {
      canvas.width = frame.displayWidth
      canvas.height = frame.displayHeight
    }
    context.drawImage(frame, 0, 0)
    events.onPainted()
  }

  const makeDecoder = () => {
    const made = new VideoDecoder({
      output: (frame) => {
        try {
          if (decoder === made && !stopped) paint(frame)
        } finally {
          frame.close()
        }
      },
      error: () => {
        if (stopped || decoder !== made) return
        // A decoder that failed starts again from the next key frame.
        closeDecoder()
        askKeyFrame()
      },
    })

    return made
  }

  const configure = async (codec: string): Promise<boolean> => {
    const at = epoch
    const config: VideoDecoderConfig = { codec, optimizeForLatency: true }
    const support = await VideoDecoder.isConfigSupported(config).catch(() => ({ supported: false }))
    if (stopped || at !== epoch) return false
    if (!support.supported) {
      events.onTrouble(`This browser cannot decode the phone's video (${codec}).`)

      return false
    }
    try {
      decoder ??= makeDecoder()
      decoder.configure(config)

      return true
    } catch (error) {
      events.onTrouble(`The video decoder failed: ${errorText(error)}`)

      return false
    }
  }

  const decode = (isKey: boolean, data: Uint8Array) => {
    if (!decoder || decoder.state !== "configured") return
    if (awaitingKey) {
      if (!isKey) return
      awaitingKey = false
    }
    if (decoder.decodeQueueSize > softQueue) {
      closeDecoder()
      askKeyFrame()

      return
    }
    try {
      decoder.decode(new EncodedVideoChunk({ type: isKey ? "key" : "delta", timestamp, data }))
      timestamp += frameMicros
    } catch {
      closeDecoder()
      askKeyFrame()
    }
  }

  const onFrame = (raw: ArrayBuffer) => {
    const packet = parseSemuPacket(raw)
    const ready = decoder !== null && decoder.state === "configured"
    const scanned =
      packet.isKey === null || (packet.isKey && !ready) ? scanAccessUnit(packet.data) : null
    const isKey = packet.isKey ?? scanned?.isKey ?? false

    if (scanned?.sps && !ready) {
      if (configuring) return
      configuring = true
      const at = epoch
      void configure(avcCodecString(scanned.sps)).then((configured) => {
        if (stopped || at !== epoch) return
        configuring = false
        awaitingKey = true
        // The key frame that carried the SPS went by while the decoder was made: another is asked for.
        if (configured) askKeyFrame()
      })

      return
    }
    if (!ready) {
      if (!isKey) askKeyFrame()

      return
    }
    decode(isKey, packet.data)
  }

  const again = () => {
    if (!stopped) retry = setTimeout(() => void connect(), retryIn(failures++))
  }

  const connect = async () => {
    if (stopped) return

    if (!("VideoDecoder" in globalThis) || !("EncodedVideoChunk" in globalThis)) {
      stopped = true
      // WebCodecs is there only on a secure page: HTTPS, or this computer's own address.
      events.onGone(
        "This browser cannot show the phone: it needs WebCodecs, which a page opened over HTTPS has.",
      )

      return
    }

    let ticket: string
    try {
      ticket = (await openDeviceView()).data.ticket
    } catch (error) {
      if (stopped) return
      stopped = true
      events.onGone(errorText(error))

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
    })
    ws.addEventListener("message", (event) => {
      if (socket !== ws) return
      if (event.data instanceof ArrayBuffer) {
        failures = 0
        onFrame(event.data)

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

      const {
        type,
        controller: who,
        state,
        reason,
        width,
        height,
      } = message as Record<string, unknown>
      if (type === "control" && (who === "agent" || who === "you")) {
        controller = who
        events.onControl(who)
      } else if (type === "state" && typeof state === "string") {
        events.onState(state as PhoneState, typeof reason === "string" ? reason : "")
      } else if (type === "screen" && typeof width === "number" && typeof height === "number") {
        failures = 0
        // The encoder starts again at the new size: the next key frame carries a new SPS, and the decoder is made
        // again from it.
        closeDecoder()
        events.onScreen({ width, height })
      } else if (type === "error" && typeof reason === "string") {
        events.onTrouble(reason)
      }
    })
    ws.addEventListener("close", (event) => {
      if (socket !== ws) return
      socket = null
      controller = null
      closeDecoder()
      if (opened) events.onConnected(false)
      if (stopped) return

      if (event.code === phoneGone) {
        stopped = true
        events.onGone(event.reason || "The phone cannot be shown now.")

        return
      }

      again()
    })
    ws.addEventListener("error", () => ws.close())
  }

  void connect()

  return {
    send: (input) => {
      if (input.type !== "takeControl" && input.type !== "reset-video" && controller !== "you") {
        return false
      }

      return send(input)
    },
    stop: () => {
      stopped = true
      if (retry !== null) clearTimeout(retry)
      const ws = socket
      socket = null
      controller = null
      closeDecoder()
      // A normal close: the owner keeps a hold for the grace; only "Hand back" (releaseControl) gives it back.
      ws?.close(1000)
    },
  }
}
