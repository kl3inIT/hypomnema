// The phone frame's hooks (issue #528): the size of the room the frame is fitted in, and a wheel's drag sent as a
// finger's touches.
//
// Adapted from Orca (src/renderer/src/components/emulator-pane/use-emulator-pane-size.ts and
// use-emulator-wheel-gesture.ts; MIT License, Copyright (c) 2026 Lovecast Inc.). Changed: the touches are this
// view's `down`, `move` and `up`.
import { useCallback, useEffect, useRef, useState } from "react"

import { wheelStep } from "./frame"

type Size = { width: number; height: number }

// useRoomSize is an element's size in whole pixels, read again a frame after it changes.
export function useRoomSize() {
  const ref = useRef<HTMLDivElement | null>(null)
  const [size, setSize] = useState<Size | null>(null)

  useEffect(() => {
    const node = ref.current
    if (!node) return

    let frame: number | null = null
    const update = () => {
      const rect = node.getBoundingClientRect()
      const width = Math.floor(rect.width)
      const height = Math.floor(rect.height)
      setSize((now) => (now?.width === width && now.height === height ? now : { width, height }))
    }
    const schedule = () => {
      if (frame !== null) cancelAnimationFrame(frame)
      frame = requestAnimationFrame(() => {
        frame = null
        update()
      })
    }

    update()
    const observer = new ResizeObserver(schedule)
    observer.observe(node)

    return () => {
      if (frame !== null) cancelAnimationFrame(frame)
      observer.disconnect()
    }
  }, [])

  return { ref, size }
}

type Point = { x: number; y: number }
export type Touch = Point & { action: "down" | "move" | "up" }

type Drag = {
  end: Point
  remaining: Point
  moveTimer: number | null
  endTimer: number | null
  tailFrames: number
  firstMove: boolean
}

// useWheelDrag turns a wheel's turns into one finger's drag, as Orca's: the finger goes down where the wheel is,
// moves a part of what is left every 16ms, slows evenly at the end so that the phone does not fling, starts again
// from the far side when it reaches an edge, and lifts 80ms after the last move. `send` is false when the touch
// could not be sent; the drag then stops.
export function useWheelDrag(send: (touch: Touch) => boolean) {
  const drag = useRef<Drag | null>(null)

  const release = useCallback(() => {
    const now = drag.current
    drag.current = null
    if (!now) return
    if (now.moveTimer !== null) window.clearTimeout(now.moveTimer)
    if (now.endTimer !== null) window.clearTimeout(now.endTimer)
    send({ ...now.end, action: "up" })
  }, [send])
  useEffect(() => release, [release])

  const advance = useCallback(
    function advanceDrag() {
      const now = drag.current
      if (!now) return

      const distance = Math.hypot(now.remaining.x, now.remaining.y)
      // An even, slow release: a last jump can be the last motion the phone sees, and fling.
      if (now.tailFrames === 0 && !now.firstMove && distance <= 0.01) now.tailFrames = 6
      let fraction = 0.35
      if (now.tailFrames > 0) fraction = 1 / now.tailFrames
      else if (now.firstMove) fraction = Math.min(0.9, 0.012 / distance)

      const stroke = wheelStep(now.end, {
        x: now.remaining.x * fraction,
        y: now.remaining.y * fraction,
      })
      if (!stroke) {
        now.moveTimer = null
        now.endTimer = window.setTimeout(release, 80)

        return
      }
      if (stroke.start.x !== now.end.x || stroke.start.y !== now.end.y) {
        // At an edge: the finger lifts and comes down again on the far side.
        send({ ...now.end, action: "up" })
        if (!send({ ...stroke.start, action: "down" })) {
          drag.current = null

          return
        }
        now.end = stroke.start
        now.firstMove = true
      } else {
        now.firstMove = false
        const completes =
          Math.hypot(stroke.end.x - stroke.start.x, stroke.end.y - stroke.start.y) >=
          distance * fraction - 1e-9
        if (completes && now.tailFrames > 0) now.tailFrames--
        now.remaining = {
          x: now.remaining.x - (stroke.end.x - stroke.start.x),
          y: now.remaining.y - (stroke.end.y - stroke.start.y),
        }
        now.end = stroke.end
        send({ ...stroke.end, action: "move" })
      }
      if (Math.hypot(now.remaining.x, now.remaining.y) < 1e-9) {
        now.moveTimer = null
        now.endTimer = window.setTimeout(release, 80)
      } else {
        now.moveTimer = window.setTimeout(advanceDrag, 16)
      }
    },
    [release, send],
  )

  const queue = useCallback(
    ({ start, delta }: { start: Point; delta: Point }) => {
      let now = drag.current
      if (!now) {
        const stroke = wheelStep(start, delta)
        // Without the socket there is no finger to move.
        if (!stroke || !send({ ...stroke.start, action: "down" })) return
        now = {
          end: stroke.start,
          remaining: { x: 0, y: 0 },
          moveTimer: null,
          endTimer: null,
          tailFrames: 0,
          firstMove: true,
        }
      }
      now = {
        ...now,
        remaining: { x: now.remaining.x + delta.x, y: now.remaining.y + delta.y },
        tailFrames: 0,
      }
      if (now.endTimer !== null) {
        window.clearTimeout(now.endTimer)
        now.endTimer = null
      }
      // The first move does not share the touch's frame, or the phone takes it as a tap.
      if (now.moveTimer === null) now.moveTimer = window.setTimeout(advance, 16)
      drag.current = now
    },
    [advance, send],
  )

  return { cancel: release, queue, active: () => drag.current !== null }
}
