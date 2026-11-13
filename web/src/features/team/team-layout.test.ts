import { describe, expect, it } from "vitest"

import {
  CARD_HEIGHT,
  CARD_WIDTH,
  HEADER_HEIGHT,
  LINE_HEIGHT,
  MACHINE_HEAD,
  MACHINE_WIDTH,
  fitView,
  machineHeight,
  polylinePath,
  teamGeometry,
  zoomAt,
} from "./team-layout"

// The Team map's geometry and view, ported from OpenMausBot's src/lib/team-canvas.test.ts and
// team-canvas-computers.test.ts.

const overlaps = (
  a: { x: number; y: number; width: number; height: number },
  b: { x: number; y: number; width: number; height: number },
) => a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height

describe("the team map's geometry", () => {
  it("puts the Bots in a column on the left and the machines, as tall as their work, on the right", () => {
    const geometry = teamGeometry(
      ["a", "b", "c"],
      [
        { id: "n1", lines: 1 },
        { id: "n2", lines: 0 },
        { id: "n3", lines: 2 },
      ],
      [],
    )

    expect(geometry.cards.map((c) => [c.bot, c.x])).toEqual([
      ["a", 0],
      ["b", 0],
      ["c", 0],
    ])
    expect(geometry.machines.map((m) => m.height)).toEqual([
      MACHINE_HEAD + LINE_HEIGHT,
      machineHeight(0),
      MACHINE_HEAD + 2 * LINE_HEIGHT,
    ])

    const boxes = [
      ...geometry.cards.map((c) => ({ ...c, width: CARD_WIDTH, height: CARD_HEIGHT })),
      ...geometry.machines,
    ]
    boxes.forEach((box, i) =>
      boxes.slice(i + 1).forEach((other) => expect(overlaps(box, other)).toBe(false)),
    )
    for (const box of boxes) {
      expect(box.y).toBeGreaterThanOrEqual(HEADER_HEIGHT)
      expect(box.x + box.width).toBeLessThanOrEqual(geometry.width)
      expect(box.y + box.height).toBeLessThanOrEqual(geometry.height)
    }
  })

  it("joins a Bot to a machine level with it by a straight line, and to another through the bus", () => {
    const geometry = teamGeometry(
      ["a", "b"],
      [
        { id: "n1", lines: 0 },
        { id: "n2", lines: 1 },
      ],
      [
        { bot: "b", node: "n1" },
        { bot: "a", node: "n2" },
        { bot: "gone", node: "n1" },
      ],
    )
    const [toFirst, toSecond] = geometry.links
    const first = geometry.machines[0]
    const second = geometry.machines[1]

    expect(geometry.links).toHaveLength(2)
    expect(toFirst?.points[0]).toEqual({
      x: CARD_WIDTH,
      y: (geometry.cards[1]?.y ?? 0) + CARD_HEIGHT / 2,
    })
    expect(toFirst?.points.at(-1)).toEqual({ x: first?.x, y: (first?.y ?? 0) + MACHINE_HEAD / 2 })
    expect(toSecond?.points.at(-1)).toEqual({
      x: second?.x,
      y: (second?.y ?? 0) + MACHINE_HEAD / 2,
    })
    // Every bend is in the gap between the columns.
    for (const link of geometry.links) {
      for (const point of link.points) {
        expect(point.x).toBeGreaterThanOrEqual(CARD_WIDTH)
        expect(point.x).toBeLessThanOrEqual(first?.x ?? 0)
      }
    }
  })

  it("is one column wide with no machines", () => {
    const geometry = teamGeometry(["a"], [], [])

    expect(geometry.width).toBe(CARD_WIDTH)
    expect(geometry.height).toBe(HEADER_HEIGHT + CARD_HEIGHT)
    expect(geometry.machines).toEqual([])
    expect(teamGeometry(["a"], [{ id: "n1", lines: 0 }], []).width).toBeGreaterThan(MACHINE_WIDTH)
  })

  it("draws polylines as SVG paths", () => {
    expect(
      polylinePath([
        { x: 1, y: 2 },
        { x: 3, y: 4 },
        { x: 5, y: 2 },
      ]),
    ).toBe("M1 2 L3 4 L5 2")
  })
})

describe("the team map's view", () => {
  it("centres the map with its margin", () => {
    const view = fitView({ width: 1000, height: 900 }, 900, 700)
    const right = 1000 * view.scale + view.x
    const bottom = 900 * view.scale + view.y

    expect(view.x).toBeCloseTo(900 - right)
    expect(view.y).toBeCloseTo(700 - bottom)
    expect(view.y).toBeCloseTo(40)
    expect(view.x).toBeGreaterThanOrEqual(40)
  })

  it("never fits past 100%, never below 30%, and handles an empty map", () => {
    expect(fitView({ width: 300, height: 200 }, 1000, 1000).scale).toBe(1)
    expect(fitView({ width: 3000, height: 2000 }, 100, 100).scale).toBe(0.3)
    expect(fitView({ width: 0, height: 0 }, 900, 700)).toEqual({ x: 0, y: 0, scale: 1 })
  })

  it("keeps the point under the cursor anchored while zooming", () => {
    const view = { x: -50, y: 80, scale: 0.5 }
    const point = { x: 310, y: 270 }
    const world = { x: (point.x - view.x) / view.scale, y: (point.y - view.y) / view.scale }
    const next = zoomAt(view, 1.4, point)

    expect(world.x * next.scale + next.x).toBeCloseTo(point.x)
    expect(world.y * next.scale + next.y).toBeCloseTo(point.y)
    expect(zoomAt(view, view.scale, point)).toEqual(view)
  })
})
