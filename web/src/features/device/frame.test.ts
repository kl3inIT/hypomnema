import { describe, expect, it } from "vitest"

import { fitPhoneFrame, sideButtons, wheelDrag, wheelStep, type FrameLayout } from "./frame"

const aspect = 720 / 1280

const screenOf = (layout: FrameLayout) => ({
  width: layout.shellWidth - layout.bezel * 2,
  height: layout.shellHeight - layout.bezel * 2,
})

describe("the phone's frame", () => {
  it("fits inside the pane it is given, whatever its shape, and keeps the screen's shape", () => {
    for (const pane of [
      { width: 1200, height: 900 },
      { width: 260, height: 900 },
      { width: 180, height: 300 },
      { width: 601.5, height: 433 },
    ]) {
      const layout = fitPhoneFrame(pane, aspect)!

      expect(layout.width, `${pane.width}×${pane.height}`).toBeLessThanOrEqual(pane.width)
      expect(layout.height, `${pane.width}×${pane.height}`).toBeLessThanOrEqual(pane.height)
      const screen = screenOf(layout)
      expect(screen.width / screen.height).toBeCloseTo(aspect, 6)
      expect(layout.width).toBeCloseTo(layout.shellWidth + layout.hardwareOutset * 2, 6)
    }
  })

  it("scales the bezel and the corners with the screen, within their bounds", () => {
    const tall = fitPhoneFrame({ width: 2000, height: 2000 }, aspect)!
    expect(tall.bezel).toBe(15)
    expect(tall.outerRadius).toBe(92)
    expect(tall.innerRadius).toBe(77)

    const small = fitPhoneFrame({ width: 200, height: 260 }, aspect)!
    expect(small.bezel).toBe(7)
    expect(small.outerRadius).toBe(44)
    expect(small.innerRadius).toBe(37)

    // In between, 2.1% and 13.5% of the screen's short side.
    const middle = fitPhoneFrame({ width: 900, height: 900 }, aspect)!
    const short = screenOf(middle).width
    expect(middle.bezel).toBeCloseTo(short * 0.021, 6)
    expect(middle.outerRadius).toBeCloseTo(short * 0.135, 6)
    expect(middle.innerRadius).toBeCloseTo(middle.outerRadius - middle.bezel, 6)
  })

  it("is none in a pane with no room", () => {
    expect(fitPhoneFrame(null, aspect)).toBeNull()
    expect(fitPhoneFrame({ width: 0, height: 500 }, aspect)).toBeNull()
  })
})

describe("the phone's side buttons", () => {
  it("are the volume rocker on the left and Power on the right, sized from the shell's height", () => {
    const layout = fitPhoneFrame({ width: 600, height: 800 }, aspect)!
    const buttons = sideButtons(layout)

    expect(buttons.map((b) => [b.name, b.side])).toEqual([
      ["volumeUp", "left"],
      ["volumeDown", "left"],
      ["power", "right"],
    ])
    const [up, down, power] = buttons
    expect(up!.top).toBeCloseTo(layout.shellHeight * 0.24, 6)
    expect(down!.top).toBeCloseTo(layout.shellHeight * 0.33, 6)
    // The two halves of the rocker do not overlap, and every button stays on the frame.
    expect(up!.top + up!.height).toBeLessThanOrEqual(down!.top)
    for (const b of buttons) expect(b.top + b.height).toBeLessThan(layout.shellHeight)
    expect(up!.height).toBeCloseTo(Math.min(64, Math.max(34, layout.shellHeight * 0.08)), 6)
    expect(power!.height).toBeCloseTo(Math.min(76, Math.max(42, layout.shellHeight * 0.095)), 6)
  })
})

describe("a wheel over the phone's screen", () => {
  const rect = { left: 100, top: 50, width: 360, height: 640 }
  const screen = { width: 720, height: 1280 }

  it("is a drag from the pointer, the other way from the wheel", () => {
    const drag = wheelDrag(
      { clientX: 280, clientY: 370, deltaX: 0, deltaY: 64, deltaMode: 0 },
      rect,
      screen,
    )

    expect(drag?.start).toEqual({ x: 0.5, y: 0.5 })
    expect(drag?.delta.x).toBeCloseTo(0, 9)
    // Scrolling down drags the finger up: 64px of 640, by 1.2.
    expect(drag?.delta.y).toBeCloseTo(-0.12, 6)
  })

  it("counts a wheel in lines as 16 pixels a line", () => {
    const drag = wheelDrag(
      { clientX: 280, clientY: 370, deltaX: 0, deltaY: 4, deltaMode: 1 },
      rect,
      screen,
    )

    expect(drag?.delta.y).toBeCloseTo(-0.12, 6)
  })

  it("is nothing outside the picture", () => {
    expect(
      wheelDrag({ clientX: 50, clientY: 370, deltaX: 0, deltaY: 64, deltaMode: 0 }, rect, screen),
    ).toBeNull()
  })

  it("starts inside the screen's edges and stops at one", () => {
    expect(wheelStep({ x: 0.5, y: 0.5 }, { x: 0, y: 0 })).toBeNull()

    // From near the bottom, dragging down: it starts again from the top edge.
    expect(wheelStep({ x: 0.5, y: 0.95 }, { x: 0, y: 0.1 })).toEqual({
      start: { x: 0.5, y: 0.12 },
      end: { x: 0.5, y: expect.closeTo(0.22, 9) as number },
    })
    // A long drag up stops at the top edge.
    expect(wheelStep({ x: 0.5, y: 0.5 }, { x: 0, y: -2 })?.end).toEqual({ x: 0.5, y: 0.12 })
  })
})
