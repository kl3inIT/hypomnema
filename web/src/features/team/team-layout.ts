// Where the Team page's cards and links go, and how its view pans and zooms (issue #262).
//
// Adapted from OpenMausBot (Apache License 2.0, Copyright 2026 Milind Soni and OpenMausBot contributors,
// https://github.com/milind-soni/OpenMausBot at 1a4e4da9f; see web/THIRD_PARTY_NOTICES.md):
// src/lib/team-canvas.ts (Point, View, fitTeams, zoomAt) and src/lib/team-canvas-computers.ts (teamGeometry,
// polylinePath). Changed: one team, so one tile: the Bots in a column on the left and every machine in a column on
// the right, since any machine serves any Bot; a machine's card is as tall as its lines of work, where OpenMausBot's
// monitor has one size; every link takes the shared computer's bus (a step down the gap between the columns);
// fitting never zooms past 100%, as AffectUI's (OpenMausBot's fitTeams already stops there) and keeps a smaller
// margin on a narrow screen. Saved positions, bot order, departments and the chief's tree are left out.

export interface Point {
  x: number
  y: number
}

export interface View extends Point {
  scale: number
}

export interface Box extends Point {
  width: number
  height: number
}

export const CARD_WIDTH = 256
export const CARD_HEIGHT = 112
export const GAP = 16
// The column titles ("Bots", "Machines") sit above the cards.
export const HEADER_HEIGHT = 28
export const LINK_WIDTH = 96
export const MACHINE_WIDTH = 320
export const MACHINE_HEAD = 40
export const LINE_HEIGHT = 52
export const IDLE_HEIGHT = 36

export const MIN_SCALE = 0.3
export const MAX_SCALE = 1.5

// machineHeight is a machine card's height: its head, then a line a live Dispatch or one line saying it is idle.
export function machineHeight(lines: number) {
  return MACHINE_HEAD + (lines > 0 ? lines * LINE_HEIGHT : IDLE_HEIGHT)
}

export interface TeamGeometry {
  width: number
  height: number
  cards: Array<Point & { bot: string }>
  machines: Array<Box & { id: string }>
  // Polylines from a Bot card's right edge to a machine card's head.
  links: Array<{ bot: string; node: string; points: Point[] }>
  // Where the machines' column starts, for its title.
  machinesX: number
}

export function teamGeometry(
  bots: readonly string[],
  machines: ReadonlyArray<{ id: string; lines: number }>,
  links: ReadonlyArray<{ bot: string; node: string }>,
): TeamGeometry {
  const cards = bots.map((bot, index) => ({
    bot,
    x: 0,
    y: HEADER_HEIGHT + index * (CARD_HEIGHT + GAP),
  }))
  const machinesX = CARD_WIDTH + LINK_WIDTH
  let y = HEADER_HEIGHT
  const boxes = machines.map((machine) => {
    const box = {
      id: machine.id,
      x: machinesX,
      y,
      width: MACHINE_WIDTH,
      height: machineHeight(machine.lines),
    }
    y += box.height + GAP

    return box
  })
  const busX = CARD_WIDTH + LINK_WIDTH / 2
  const drawn = links.flatMap((link) => {
    const card = cards.find((c) => c.bot === link.bot)
    const box = boxes.find((b) => b.id === link.node)
    if (!card || !box) return []

    const start = { x: CARD_WIDTH, y: card.y + CARD_HEIGHT / 2 }
    const port = box.y + MACHINE_HEAD / 2
    const points =
      start.y === port
        ? [start, { x: machinesX, y: port }]
        : [start, { x: busX, y: start.y }, { x: busX, y: port }, { x: machinesX, y: port }]

    return [{ bot: link.bot, node: link.node, points }]
  })
  const botsBottom = cards.length ? (cards.at(-1)?.y ?? 0) + CARD_HEIGHT : HEADER_HEIGHT
  const machinesBottom = boxes.length ? y - GAP : HEADER_HEIGHT

  return {
    width: boxes.length ? machinesX + MACHINE_WIDTH : CARD_WIDTH,
    height: Math.max(botsBottom, machinesBottom),
    cards,
    machines: boxes,
    links: drawn,
    machinesX,
  }
}

export function polylinePath(points: readonly Point[]): string {
  return points.map((point, index) => `${index ? "L" : "M"}${point.x} ${point.y}`).join(" ")
}

// fitView centres the whole map with a margin, at the largest scale that shows all of it, never above 100%.
export function fitView(
  content: { width: number; height: number },
  width: number,
  height: number,
): View {
  if (content.width <= 0 || content.height <= 0) return { x: 0, y: 0, scale: 1 }

  const margin = width < 640 ? 32 : 80
  const scale = Math.max(
    MIN_SCALE,
    Math.min(1, (width - margin) / content.width, (height - margin) / content.height),
  )

  return {
    x: (width - content.width * scale) / 2,
    y: (height - content.height * scale) / 2,
    scale,
  }
}

// zoomAt keeps the point under the cursor where it is while the scale changes.
export function zoomAt(view: View, nextScale: number, point: Point): View {
  const ratio = nextScale / view.scale

  return {
    x: point.x - (point.x - view.x) * ratio,
    y: point.y - (point.y - view.y) * ratio,
    scale: nextScale,
  }
}

export function clampScale(scale: number) {
  return Math.min(MAX_SCALE, Math.max(MIN_SCALE, scale))
}
