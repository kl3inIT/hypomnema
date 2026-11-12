// What the browser page works out without a browser: how a frame sits in its panel and where a pointer is on the
// tab, what a key or a pasted text is on the wire, and how long to wait before the next try.
//
// The letterbox and the pointer's mapping are OpenMausBot's (src/components/browser/StreamSurface.tsx, Apache
// License 2.0, Copyright 2026 Milind Soni and OpenMausBot contributors), which adapted them from T3 Code
// (apps/web/src/browser/ServerBrowserSurface.tsx, MIT License, Copyright (c) 2026 T3 Tools Inc.). The key's
// message, the modifier bits and the wheel's steps are T3 Code's. Changed: a pasted or composed text is sent as one
// key a character, since the control plane takes keys only (issue #219).

export type Controller = "agent" | "you"

export type Viewport = { width: number; height: number }

export type MouseButton = "left" | "middle" | "right" | "none"

// What the page sends on a view's WebSocket (api/openapi.yaml, OpenBrowserView).
export type ViewInput =
  | { type: "takeControl" }
  | { type: "releaseControl" }
  | {
      type: "mouse"
      action: "move" | "down" | "up"
      x: number
      y: number
      button: MouseButton
      clickCount: number
      modifiers: number
    }
  | { type: "wheel"; x: number; y: number; deltaX: number; deltaY: number; modifiers: number }
  | {
      type: "key"
      action: "down" | "up"
      key: string
      code: string
      keyCode: number
      text?: string
      modifiers: number
    }
  | { type: "navigate"; url: string }
  | { type: "history"; delta: number }
  | { type: "reload" }

export type PagePoint = { x: number; y: number; scale: number }

type Box = { width: number; height: number }
type Rect = Box & { left: number; top: number }

// letterbox is how a frame of `canvas` pixels sits in a box: scaled to fit and centred, as `object-contain` draws
// it.
export function letterbox(box: Box, canvas: Box) {
  const fit = Math.min(box.width / canvas.width, box.height / canvas.height)
  const width = canvas.width * fit
  const height = canvas.height * fit

  return { width, height, left: (box.width - width) / 2, top: (box.height - height) / 2 }
}

// mapToPage is a pointer's place on the tab, in the tab's own pixels, or null outside the picture; `clamp` keeps a
// drag that left the picture on its edge.
export function mapToPage(
  rect: Rect,
  canvas: Box,
  viewport: Viewport,
  clientX: number,
  clientY: number,
  clamp: boolean,
): PagePoint | null {
  const shown = letterbox(rect, canvas)
  if (!(shown.width > 0 && shown.height > 0)) return null

  const scale = viewport.width / shown.width
  const x = (clientX - rect.left - shown.left) * scale
  const y = (clientY - rect.top - shown.top) * (viewport.height / shown.height)
  const inside = x >= 0 && y >= 0 && x <= viewport.width && y <= viewport.height
  if (!inside && !clamp) return null

  return {
    x: Math.min(Math.max(x, 0), viewport.width),
    y: Math.min(Math.max(y, 0), viewport.height),
    scale,
  }
}

// modifiers are Chrome's bits for the keys held with an event.
export function modifiers(event: {
  altKey: boolean
  ctrlKey: boolean
  metaKey: boolean
  shiftKey: boolean
}) {
  return (
    (event.altKey ? 1 : 0) |
    (event.ctrlKey ? 2 : 0) |
    (event.metaKey ? 4 : 0) |
    (event.shiftKey ? 8 : 0)
  )
}

export const buttonOf = (button: number): MouseButton =>
  button === 0 ? "left" : button === 1 ? "middle" : button === 2 ? "right" : "none"

export const pressedButtonOf = (buttons: number): MouseButton =>
  buttons & 1 ? "left" : buttons & 2 ? "right" : buttons & 4 ? "middle" : "none"

// A press counts on from the last one of the same button within this time and distance.
const multiClickMs = 500
const multiClickSlopPx = 4

export type Click = { button: MouseButton; time: number; x: number; y: number; count: number }

// countClick is the click a press is: the second of a double click, the third of a triple one.
export function countClick(
  last: Click | null,
  button: MouseButton,
  x: number,
  y: number,
  time: number,
): Click {
  const again =
    last !== null &&
    last.button === button &&
    time - last.time < multiClickMs &&
    Math.hypot(x - last.x, y - last.y) < multiClickSlopPx

  return { button, time, x, y, count: again ? last.count + 1 : 1 }
}

// wheelUnit is how many pixels one step of a wheel is: a line is 16, a page the tab's height.
export function wheelUnit(deltaMode: number, viewport: Viewport | null) {
  if (deltaMode === 1) return 16
  if (deltaMode === 2) return viewport?.height ?? 0

  return 1
}

type KeyEvent = {
  key: string
  code: string
  keyCode: number
  altKey: boolean
  ctrlKey: boolean
  metaKey: boolean
  shiftKey: boolean
}

// keyInput is a key as the tab is to get it. A key that types carries its text on the way down, and Enter a
// carriage return, so that a form is sent and a text area breaks its line; a shortcut carries none.
export function keyInput(action: "down" | "up", event: KeyEvent): ViewInput {
  const shortcut = event.ctrlKey || event.metaKey
  const text = shortcut
    ? undefined
    : [...event.key].length === 1
      ? event.key
      : event.key === "Enter"
        ? "\r"
        : undefined

  return {
    type: "key",
    action,
    key: event.key,
    code: event.code,
    keyCode: event.keyCode,
    ...(action === "down" && text !== undefined ? { text } : {}),
    modifiers: modifiers(event),
  }
}

// textInput is a pasted or composed text as keys: each character goes down with its text and up again.
export function textInput(text: string): ViewInput[] {
  return [...text].flatMap((character): ViewInput[] => {
    const typed = character === "\n" ? "\r" : character
    const key = character === "\n" ? "Enter" : character
    const keyCode = character === "\n" ? 13 : 0

    return [
      { type: "key", action: "down", key, code: "", keyCode, text: typed, modifiers: 0 },
      { type: "key", action: "up", key, code: "", keyCode, modifiers: 0 },
    ]
  })
}

// retryIn is the wait before try number `failures` (from 0) after a view dropped: half a second, doubled each
// time, ten seconds at most.
export function retryIn(failures: number) {
  return Math.min(500 * 2 ** failures, 10_000)
}

// socketUrl is a view's WebSocket on the server that served the page: the ticket is its permission.
export function socketUrl(location: { protocol: string; host: string }, ticket: string) {
  const scheme = location.protocol === "https:" ? "wss:" : "ws:"

  return `${scheme}//${location.host}/api/v1/browser/views/${encodeURIComponent(ticket)}`
}

// tabGone is the close code of a tab that is not open any more.
export const tabGone = 4404
