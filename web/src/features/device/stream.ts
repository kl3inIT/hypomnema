// What the phone's view works out without a phone (issue #417, step 3): a frame's SEMU header, whether an access unit
// starts a decoder and with which codec, a key as Android's key code, a pasted text as messages, and where a pointer
// is on the screen.
//
// The SEMU header, the access unit's scan, the codec string, the key codes and the text's split are T3 Code's
// (packages/client-runtime/src/device/stream.ts: parseSemuPacket, scanAccessUnit, avcCodecString,
// ANDROID_KEYCODE_BY_KEY, sendText; MIT License, Copyright (c) 2026 T3 Tools Inc.), which speak serve-emu's
// protocol (expo-device-hub, llp/0008-serve-emu-protocol.spec.md). The pointer's place through the letterbox is the
// browser view's (../browser/stream.ts), as a point from 0 to 1 of the screen, which serve-emu's input takes.

import { letterbox } from "@/features/browser/stream"

export type Controller = "agent" | "you"

export type Screen = { width: number; height: number }

// What the page sends on the phone's WebSocket (api/openapi.yaml, OpenDeviceView).
export type PhoneInput =
  | { type: "takeControl" }
  | { type: "releaseControl" }
  | { type: "reset-video" }
  | { type: "touch"; action: "down" | "move" | "up"; x: number; y: number }
  | { type: "key"; keycode: number }
  | { type: "text"; text: string }
  | { type: "back" }
  | { type: "home" }
  | { type: "recents" }
  | { type: "power" }

const semuMagic = 0x53454d55
const semuHeaderBytes = 16

// parseSemuPacket is a frame without its SEMU header, with whether it is a key frame; null when the header is
// missing, and the whole message is the access unit.
export function parseSemuPacket(raw: ArrayBuffer): { data: Uint8Array; isKey: boolean | null } {
  const bytes = new Uint8Array(raw)
  if (bytes.byteLength > semuHeaderBytes) {
    const view = new DataView(raw, 0, semuHeaderBytes)
    if (view.getUint32(0, false) === semuMagic && view.getUint8(4) === 1) {
      return { data: bytes.subarray(semuHeaderBytes), isKey: (view.getUint8(5) & 1) !== 0 }
    }
  }

  return { data: bytes, isKey: null }
}

// scanAccessUnit says whether an Annex-B access unit holds an IDR picture, and its SPS when it has one.
export function scanAccessUnit(buf: Uint8Array): { isKey: boolean; sps: Uint8Array | null } {
  let isKey = false
  let sps: Uint8Array | null = null
  let i = 0
  while (i + 2 < buf.length) {
    if (buf[i] === 0 && buf[i + 1] === 0) {
      let codeLen = 0
      if (buf[i + 2] === 1) codeLen = 3
      else if (i + 3 < buf.length && buf[i + 2] === 0 && buf[i + 3] === 1) codeLen = 4
      if (codeLen) {
        const nalType = (buf[i + codeLen] ?? 0) & 0x1f
        if (nalType === 7 && !sps) sps = buf.subarray(i + codeLen)
        if (nalType === 5) isKey = true
        i += codeLen + 1
        continue
      }
    }
    i++
  }

  return { isKey, sps }
}

// avcCodecString is the WebCodecs codec of an SPS: its profile, constraints and level.
export function avcCodecString(sps: Uint8Array): string {
  if (sps.length < 4) return "avc1.42E01E"
  const hex = (byte: number) => byte.toString(16).padStart(2, "0")

  return `avc1.${hex(sps[1] ?? 0)}${hex(sps[2] ?? 0)}${hex(sps[3] ?? 0)}`
}

// The keys that have an Android key code of their own; any other key that types is sent as its text.
const androidKeycodes: Readonly<Record<string, number>> = {
  ArrowUp: 19,
  ArrowDown: 20,
  ArrowLeft: 21,
  ArrowRight: 22,
  Tab: 61,
  Enter: 66,
  Backspace: 67,
  Delete: 112,
  Home: 122,
  End: 123,
  PageUp: 92,
  PageDown: 93,
}

// keyInput is a key pressed in the view as the phone takes it: Escape is Back, a key with a code of its own is that
// code, a key that types is its text; null for a key the phone is not sent.
export function keyInput(event: {
  key: string
  ctrlKey: boolean
  metaKey: boolean
  altKey: boolean
}): PhoneInput | null {
  if (event.key === "Escape") return { type: "back" }
  const keycode = androidKeycodes[event.key]
  if (keycode !== undefined) return { type: "key", keycode }
  if ([...event.key].length === 1 && !event.ctrlKey && !event.metaKey) {
    return { type: "text", text: event.key }
  }

  return null
}

// A text message is at most 300 UTF-8 bytes (serve-emu's input.js MAX_TEXT_BYTES, scrcpy's inject-text limit), and a
// paste at most 64 of them.
const maxTextBytes = 300
const maxTextChunks = 64

// textInputs is a pasted or composed text as text messages, split between characters.
export function textInputs(text: string): PhoneInput[] {
  const out: PhoneInput[] = []
  let chunk = ""
  let bytes = 0
  for (const character of text) {
    const size = new TextEncoder().encode(character).length
    if (bytes + size > maxTextBytes) {
      out.push({ type: "text", text: chunk })
      if (out.length >= maxTextChunks) return out
      chunk = ""
      bytes = 0
    }
    chunk += character
    bytes += size
  }
  if (chunk) out.push({ type: "text", text: chunk })

  return out
}

type Rect = { left: number; top: number; width: number; height: number }

// screenPoint is a pointer's place on the screen, from 0 to 1 each way, through the picture's letterbox; null outside
// it unless `clamp` keeps a drag that left the picture on its edge.
export function screenPoint(
  rect: Rect,
  screen: Screen,
  clientX: number,
  clientY: number,
  clamp: boolean,
): { x: number; y: number } | null {
  const shown = letterbox(rect, screen)
  if (!(shown.width > 0 && shown.height > 0)) return null

  const x = (clientX - rect.left - shown.left) / shown.width
  const y = (clientY - rect.top - shown.top) / shown.height
  const inside = x >= 0 && y >= 0 && x <= 1 && y <= 1
  if (!inside && !clamp) return null

  return { x: Math.min(Math.max(x, 0), 1), y: Math.min(Math.max(y, 0), 1) }
}

// socketUrl is the phone view's WebSocket on the server that served the page: the ticket is its permission.
export function socketUrl(location: { protocol: string; host: string }, ticket: string) {
  const scheme = location.protocol === "https:" ? "wss:" : "ws:"

  return `${scheme}//${location.host}/api/v1/device/views/${encodeURIComponent(ticket)}`
}

// phoneGone is the close code of a phone that cannot be shown now: nothing is tried again until the owner asks.
export const phoneGone = 4404

// The phone's states as the view says them (api/openapi.yaml, OpenDeviceView).
export type PhoneState =
  | "waking"
  | "absent"
  | "installing"
  | "stopped"
  | "starting"
  | "running"
  | "failed"

// stateLine is what the view says while the phone is not on screen yet.
export function stateLine(state: PhoneState | null): string {
  switch (state) {
    case "waking":
      return "Waking the phone's machine… about a minute."
    case "absent":
    case "installing":
      return "Installing the phone… a few minutes the first time."
    case "stopped":
    case "starting":
      return "Starting the phone… about a minute."
    case "failed":
      return "The phone did not start."
    case "running":
      return "Connecting video…"
    default:
      return "Connecting…"
  }
}
