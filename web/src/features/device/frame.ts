// The phone's frame and its wheel (issue #528): the shell drawn around the screen, fitted in the pane, its side
// buttons, and a wheel turned over the screen as a finger's drag.
//
// Adapted from Orca (src/renderer/src/components/emulator-pane: emulator-device-frame-layout.ts,
// fitDeviceFrameToPane and measureChrome for a phone; emulator-phone-hardware-buttons.tsx; emulator-screen-gesture.ts,
// resolveEmulatorWheelDelta and resolveWheelGestureStep; MIT License, Copyright (c) 2026 Lovecast Inc.). Changed: a
// phone only (no tablet, no rotation), the screen's place found through the browser view's letterbox, and Orca's
// action button (an iPhone's) left out.

import { letterbox } from "@/features/browser/stream"

import type { Screen } from "./stream"

type Size = { width: number; height: number }

export type FrameLayout = {
  // The whole frame, side buttons included, and the shell inside it.
  width: number
  height: number
  shellWidth: number
  shellHeight: number
  // How far the side buttons stand out of the shell on each side.
  hardwareOutset: number
  bezel: number
  outerRadius: number
  innerRadius: number
  sideButtonThickness: number
}

const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, value))

// Room left for a fraction's rounding, so that a split pane does not clip the frame's edge by a sub-pixel.
const fitMargin = 0.5

// A phone's shape before its screen's size is known (Orca's default).
export const defaultAspect = 9 / 19

function fitScreen(pane: Size, aspect: number): Size | null {
  if (pane.width <= 0 || pane.height <= 0 || aspect <= 0) return null
  if (pane.width / pane.height > aspect) {
    return { width: Math.max(1, pane.height * aspect), height: pane.height }
  }

  return { width: pane.width, height: Math.max(1, pane.width / aspect) }
}

function chrome(screen: Size) {
  const short = Math.min(screen.width, screen.height)

  return {
    bezel: clamp(short * 0.021, 7, 15),
    hardwareOutset: clamp(short * 0.012, 3, 7),
    sideButtonThickness: clamp(short * 0.01, 3, 6),
  }
}

// fitPhoneFrame is the frame of a screen of `aspect` (width over height) fitted in a pane, or null when the pane has
// no room. The bezel depends on the screen's size and the screen on the room the bezel leaves, so it is worked out
// four times, as Orca does.
export function fitPhoneFrame(pane: Size | null, aspect: number): FrameLayout | null {
  if (!pane || pane.width <= 0 || pane.height <= 0 || aspect <= 0) return null

  let screen = fitScreen(pane, aspect)
  for (let pass = 0; pass < 4 && screen; pass++) {
    const c = chrome(screen)
    screen = fitScreen(
      {
        width: Math.max(1, pane.width - c.hardwareOutset * 2 - c.bezel * 2 - fitMargin),
        height: Math.max(1, pane.height - c.bezel * 2 - fitMargin),
      },
      aspect,
    )
  }
  if (!screen) return null

  const { bezel, hardwareOutset, sideButtonThickness } = chrome(screen)
  const shellWidth = screen.width + bezel * 2
  const shellHeight = screen.height + bezel * 2
  const outerRadius = clamp(Math.min(screen.width, screen.height) * 0.135, 44, 92)

  return {
    width: shellWidth + hardwareOutset * 2,
    height: shellHeight,
    shellWidth,
    shellHeight,
    hardwareOutset,
    bezel,
    outerRadius,
    innerRadius: clamp(outerRadius - bezel, 34, 82),
    sideButtonThickness,
  }
}

export type SideButton = {
  name: "volumeUp" | "volumeDown" | "power"
  side: "left" | "right"
  top: number
  height: number
}

// sideButtons are where the side buttons stand on the frame, from its top, as Orca draws them: the volume rocker's
// two halves on the left, Power on the right. Unlike Orca's, whose bars are only drawn, the two halves are kept 2px
// apart on a short frame, so that each is pressed alone.
export function sideButtons(layout: FrameLayout): SideButton[] {
  const volume = Math.min(clamp(layout.shellHeight * 0.08, 34, 64), layout.shellHeight * 0.09 - 2)

  return [
    { name: "volumeUp", side: "left", top: layout.shellHeight * 0.24, height: volume },
    { name: "volumeDown", side: "left", top: layout.shellHeight * 0.33, height: volume },
    {
      name: "power",
      side: "right",
      top: layout.shellHeight * 0.24,
      height: clamp(layout.shellHeight * 0.095, 42, 76),
    },
  ]
}

type Point = { x: number; y: number }
type Rect = { left: number; top: number; width: number; height: number }

const domDeltaLine = 1
const domDeltaPage = 2

// wheelDrag is a wheel's turn over the screen as a finger's drag: where it starts, from 0 to 1 of the screen, and how
// far it goes, the other way from the wheel. null outside the picture.
export function wheelDrag(
  sample: { clientX: number; clientY: number; deltaX: number; deltaY: number; deltaMode: number },
  rect: Rect,
  screen: Screen,
  sensitivity = 1.2,
): { start: Point; delta: Point } | null {
  const shown = letterbox(rect, screen)
  if (!(shown.width > 0 && shown.height > 0)) return null

  const x = (sample.clientX - rect.left - shown.left) / shown.width
  const y = (sample.clientY - rect.top - shown.top) / shown.height
  if (x < 0 || x > 1 || y < 0 || y > 1) return null

  const pixels = (delta: number, page: number) =>
    sample.deltaMode === domDeltaLine
      ? delta * 16
      : sample.deltaMode === domDeltaPage
        ? delta * page
        : delta
  const dx = pixels(sample.deltaX, shown.width)
  const dy = pixels(sample.deltaY, shown.height)
  if (!Number.isFinite(dx) || !Number.isFinite(dy)) return null

  return {
    start: { x, y },
    delta: { x: (-dx / shown.width) * sensitivity, y: (-dy / shown.height) * sensitivity },
  }
}

// The finger keeps off the screen's edges, which the phone keeps for its own gestures.
const low = 0.12
const high = 0.88

// wheelStep is the next stroke of a wheel's drag from a point: it starts inside the edges, moved to the far side
// when it would leave at once, and stops at an edge. null for no movement.
export function wheelStep(point: Point, delta: Point): { start: Point; end: Point } | null {
  if (Math.hypot(delta.x, delta.y) < 1e-9) return null

  const start = { x: clamp(point.x, low, high), y: clamp(point.y, low, high) }
  for (const axis of ["x", "y"] as const) {
    if (start[axis] <= low && delta[axis] < 0) start[axis] = high
    else if (start[axis] >= high && delta[axis] > 0) start[axis] = low
  }
  const xFraction = delta.x === 0 ? Infinity : ((delta.x < 0 ? low : high) - start.x) / delta.x
  const yFraction = delta.y === 0 ? Infinity : ((delta.y < 0 ? low : high) - start.y) / delta.y
  const fraction = Math.min(1, xFraction, yFraction)

  return {
    start,
    end: {
      x: fraction === xFraction ? (delta.x < 0 ? low : high) : start.x + delta.x * fraction,
      y: fraction === yFraction ? (delta.y < 0 ? low : high) : start.y + delta.y * fraction,
    },
  }
}
