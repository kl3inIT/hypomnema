import { Maximize, Minus, Plus } from "lucide-react"
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type PointerEvent,
} from "react"

import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

import { BotCard, MachineCard } from "./team-cards"
import {
  clampScale,
  fitView,
  HEADER_HEIGHT,
  polylinePath,
  teamGeometry,
  zoomAt,
  type Point,
  type View,
} from "./team-layout"
import type { TeamBot, TeamLink, TeamMachine } from "./team-map"

// TeamCanvas is adapted from OpenMausBot's TeamCanvas (Apache License 2.0, Copyright 2026 Milind Soni and
// OpenMausBot contributors, https://github.com/milind-soni/OpenMausBot at 1a4e4da9f, src/components/TeamCanvas.tsx;
// see web/THIRD_PARTY_NOTICES.md): one CSS transform for the whole map; drag empty space or the wheel to pan,
// Ctrl+wheel (a trackpad's pinch) to zoom at the cursor, the arrows, + and - and 0 from the keyboard; the pointer is
// captured only past the drag threshold, so a plain click still reaches a card; the map fits when it first has a
// size and whenever its contents change size; − / % / + under it, the % fitting. Changed: nothing is dragged
// (no bot or team moves, no computer drops), so a gesture is only a pan; a Fit button beside the %, and fitting
// never above 100% (AffectUI); a live link's flow is the page's one motion and stops under prefers-reduced-motion;
// a Task's line opens its panel, every card not part of it fades and its machine pans into view beside the panel,
// without zooming (AffectUI).

const toolbarInset = 56

type Gesture = { id: number; start: Point; view: View; moved: boolean }

export function TeamCanvas({
  bots,
  machines,
  links,
  selected,
  now,
  onOpenBot,
  onOpenTask,
}: {
  bots: TeamBot[]
  machines: TeamMachine[]
  links: TeamLink[]
  // The open Task, its Bot and its machine: every other card fades.
  selected?: { task: string; bot?: string; node?: string }
  now: number
  onOpenBot: (bot: TeamBot) => void
  onOpenTask: (taskId: string) => void
}) {
  const viewport = useRef<HTMLDivElement>(null)
  const gesture = useRef<Gesture | null>(null)
  const suppressClick = useRef(false)
  const [view, setView] = useState<View>({ x: 40, y: 40, scale: 1 })
  const geometry = useMemo(
    () =>
      teamGeometry(
        bots.map((b) => b.name),
        machines.map((m) => ({ id: m.node.id, lines: m.lines.length })),
        links,
      ),
    [bots, machines, links],
  )
  // What the wheel and the observer read without being made again on every change.
  const current = useRef({ view, geometry })
  useLayoutEffect(() => {
    current.current = { view, geometry }
  })

  const fit = useCallback(() => {
    const bounds = viewport.current?.getBoundingClientRect()
    // The toolbar covers the bottom: fitting keeps the map above it (OpenMausBot's bottomInset).
    if (bounds)
      setView(fitView(current.current.geometry, bounds.width, bounds.height - toolbarInset))
  }, [])

  // An open Task's panel covers the right of the map: its machine is brought to the middle of what stays in view,
  // as OpenMausBot centres the selected bot's card beside its settings panel. Nothing zooms.
  const selectedNode = selected?.node
  useEffect(() => {
    if (!selectedNode) return

    const timer = window.setTimeout(() => {
      const element = viewport.current
      const card = element?.querySelector(`[data-node-id="${CSS.escape(selectedNode)}"]`)
      if (!element || !card) return

      const bounds = element.getBoundingClientRect()
      const panel = document.querySelector('[role="dialog"]')?.getBoundingClientRect()
      const right = panel && panel.left > bounds.left + 240 ? panel.left : bounds.right
      const box = card.getBoundingClientRect()
      const dx = (bounds.left + right) / 2 - (box.left + box.width / 2)
      const dy = bounds.top + bounds.height / 2 - (box.top + box.height / 2)
      setView((v) => ({ ...v, x: v.x + dx, y: v.y + dy }))
    }, 250)

    return () => window.clearTimeout(timer)
  }, [selectedNode])

  // The first size fits the map; a later resize keeps it where it was, centred as before.
  useEffect(() => {
    const element = viewport.current
    if (!element) return

    let previous: { width: number; height: number } | null = null
    const observer = new ResizeObserver(() => {
      const bounds = element.getBoundingClientRect()
      if (!previous) fit()
      else {
        const dx = (bounds.width - previous.width) / 2
        const dy = (bounds.height - previous.height) / 2
        setView((v) => ({ ...v, x: v.x + dx, y: v.y + dy }))
      }
      previous = { width: bounds.width, height: bounds.height }
    })
    observer.observe(element)

    return () => observer.disconnect()
  }, [fit])

  const size = `${geometry.width}x${geometry.height}`
  useEffect(() => {
    fit()
  }, [fit, size])

  useEffect(() => {
    const element = viewport.current
    if (!element) return

    const wheel = (event: WheelEvent) => {
      event.preventDefault()
      if (gesture.current) return

      const previous = current.current.view
      const bounds = element.getBoundingClientRect()
      if (event.ctrlKey || event.metaKey) {
        const scale = clampScale(previous.scale * Math.exp(-event.deltaY * 0.006))
        setView(
          zoomAt(previous, scale, {
            x: event.clientX - bounds.left,
            y: event.clientY - bounds.top,
          }),
        )
      } else {
        const unit = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? bounds.height : 1
        setView({
          ...previous,
          x: previous.x - event.deltaX * unit,
          y: previous.y - event.deltaY * unit,
        })
      }
    }
    element.addEventListener("wheel", wheel, { passive: false })

    return () => element.removeEventListener("wheel", wheel)
  }, [])

  const start = (event: PointerEvent<HTMLDivElement>) => {
    if (event.button !== 0 || !event.isPrimary || gesture.current) return
    if ((event.target as HTMLElement).closest("[data-team-tools]")) return

    gesture.current = {
      id: event.pointerId,
      start: { x: event.clientX, y: event.clientY },
      view,
      moved: false,
    }
    suppressClick.current = false
    // Capture only after the drag threshold: capturing earlier retargets a plain click to the canvas.
  }
  const move = (event: PointerEvent<HTMLDivElement>) => {
    const active = gesture.current
    if (!active || active.id !== event.pointerId) return
    if (event.buttons === 0) {
      finish(event)
      return
    }

    const dx = event.clientX - active.start.x
    const dy = event.clientY - active.start.y
    if (!active.moved && Math.hypot(dx, dy) < 5) return

    active.moved = true
    event.currentTarget.setPointerCapture(event.pointerId)
    setView({ ...active.view, x: active.view.x + dx, y: active.view.y + dy })
  }
  const finish = (event?: PointerEvent<HTMLDivElement>) => {
    const active = gesture.current
    if (!active || (event && active.id !== event.pointerId)) return

    gesture.current = null
    suppressClick.current = active.moved
    window.setTimeout(() => {
      suppressClick.current = false
    }, 0)
    if (viewport.current?.hasPointerCapture(active.id))
      viewport.current.releasePointerCapture(active.id)
  }
  const zoom = (factor: number) => {
    const bounds = viewport.current?.getBoundingClientRect()
    if (bounds) {
      setView(
        zoomAt(view, clampScale(view.scale * factor), {
          x: bounds.width / 2,
          y: bounds.height / 2,
        }),
      )
    }
  }

  const fades = (bot?: string, node?: string) =>
    !!selected && (bot ? bot !== selected.bot : node !== selected.node)

  // The map is a surface driven by the pointer and its own keys (arrows, + and -, 0), as OpenMausBot's canvas is:
  // focusable, with its handlers, though no ARIA role is interactive for it.
  return (
    // oxlint-disable-next-line jsx-a11y/no-noninteractive-element-interactions
    <div
      ref={viewport}
      role="application"
      aria-roledescription="map"
      aria-label="Team map"
      // oxlint-disable-next-line jsx-a11y/no-noninteractive-tabindex
      tabIndex={0}
      data-testid="team-canvas"
      className="relative min-h-0 flex-1 touch-none overflow-clip outline-none focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset"
      style={{
        backgroundImage:
          "radial-gradient(circle, color-mix(in srgb, var(--muted-foreground) 22%, transparent) 1px, transparent 1px)",
        backgroundSize: `${24 * view.scale}px ${24 * view.scale}px`,
        backgroundPosition: `${view.x}px ${view.y}px`,
      }}
      onPointerDown={start}
      onPointerMove={move}
      onPointerUp={(event) => finish(event)}
      onPointerCancel={(event) => finish(event)}
      onLostPointerCapture={() => {
        if (gesture.current) finish()
      }}
      onClickCapture={(event) => {
        if (suppressClick.current) {
          suppressClick.current = false
          event.preventDefault()
          event.stopPropagation()
        }
      }}
      onKeyDown={(event) => {
        if (event.target !== event.currentTarget) return

        const directions: Record<string, Point> = {
          ArrowLeft: { x: 60, y: 0 },
          ArrowRight: { x: -60, y: 0 },
          ArrowUp: { x: 0, y: 60 },
          ArrowDown: { x: 0, y: -60 },
        }
        const delta = directions[event.key]
        if (delta) {
          event.preventDefault()
          setView({ ...view, x: view.x + delta.x, y: view.y + delta.y })
        }
        if (event.key === "+" || event.key === "=") {
          event.preventDefault()
          zoom(1.2)
        }
        if (event.key === "-") {
          event.preventDefault()
          zoom(1 / 1.2)
        }
        if (event.key === "0") {
          event.preventDefault()
          fit()
        }
      }}
    >
      <div
        className="absolute top-0 left-0 origin-top-left"
        style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.scale})` }}
      >
        <ColumnTitle x={0}>Bots</ColumnTitle>
        {geometry.machines.length > 0 && <ColumnTitle x={geometry.machinesX}>Machines</ColumnTitle>}
        <svg
          aria-hidden
          className="pointer-events-none absolute top-0 left-0 overflow-visible"
          width={geometry.width}
          height={geometry.height}
        >
          {geometry.links.map((link) => (
            <path
              key={`${link.bot}:${link.node}`}
              data-testid="team-link"
              d={polylinePath(link.points)}
              fill="none"
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              strokeDasharray="8 4"
              className={cn(
                "animate-link-flow stroke-running motion-reduce:animate-none",
                selected &&
                  (link.bot !== selected.bot || link.node !== selected.node) &&
                  "opacity-35",
              )}
            />
          ))}
        </svg>
        {geometry.cards.map((card) => {
          const bot = bots.find((b) => b.name === card.bot)

          return bot ? (
            <BotCard
              key={bot.name}
              bot={bot}
              x={card.x}
              y={card.y}
              faded={fades(bot.name)}
              onOpen={onOpenBot}
            />
          ) : null
        })}
        {geometry.machines.map((box) => {
          const machine = machines.find((m) => m.node.id === box.id)

          return machine ? (
            <MachineCard
              key={box.id}
              node={machine.node}
              lines={machine.lines}
              x={box.x}
              y={box.y}
              faded={fades(undefined, box.id)}
              selected={selected?.task}
              now={now}
              onOpenTask={onOpenTask}
            />
          ) : null
        })}
      </div>
      <div
        data-team-tools
        className="absolute bottom-4 left-1/2 flex -translate-x-1/2 items-center gap-0.5 rounded-xl border bg-popover p-1 shadow-sm"
      >
        <Button variant="ghost" size="icon-sm" aria-label="Zoom out" onClick={() => zoom(1 / 1.2)}>
          <Minus />
        </Button>
        <Button
          variant="ghost"
          size="sm"
          aria-label="Fit to view"
          title="Fit to view (0)"
          onClick={fit}
        >
          <span className="min-w-8 tabular-nums">{Math.round(view.scale * 100)}%</span>
        </Button>
        <Button variant="ghost" size="icon-sm" aria-label="Zoom in" onClick={() => zoom(1.2)}>
          <Plus />
        </Button>
        <Button variant="ghost" size="sm" onClick={fit}>
          <Maximize />
          Fit
        </Button>
      </div>
    </div>
  )
}

function ColumnTitle({ x, children }: { x: number; children: string }) {
  return (
    <h2
      className="absolute text-xs font-medium tracking-wide text-muted-foreground uppercase"
      style={{ left: x + 2, top: 0, lineHeight: `${HEADER_HEIGHT - 10}px` }}
    >
      {children}
    </h2>
  )
}
