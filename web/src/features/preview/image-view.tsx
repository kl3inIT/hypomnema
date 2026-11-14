import { cn } from "cn"
import { RotateCw, ZoomIn, ZoomOut } from "lucide-react"
import { useEffect, useRef, useState } from "react"

import { Button } from "@/components/ui/button"

import { lockedImage, svgSize } from "./locked-image"

// Copied from MemoryOS (web/src/features/preview/image-view.tsx), without its cited regions and crop. Changed
// here: an SVG is never given a blob: address of this origin, where its script would run if it were opened on its
// own (issue #422, row 4, after the #402 review): it is drawn from the address the server sends it at with a
// sandboxing policy, or, when only its text is at hand, inside a locked frame — a srcdoc frame sandboxed with
// nothing allowed, whose policy loads nothing but the picture as a data: address, so it has an opaque origin and
// runs no script.

// The source of a picture: an address to draw from, or an SVG's markup for the locked frame.
export type ImageSource = { url: string } | { markup: string }

// InlineSvg is an SVG inside a document (a Markdown file's picture), in the locked frame, as wide as it says it is
// and no wider than the document.
export function InlineSvg({ markup, alt }: { markup: string; alt: string }) {
  const size = svgSize(markup) ?? { width: 640, height: 320 }

  return (
    <iframe
      title={alt}
      sandbox=""
      referrerPolicy="no-referrer"
      srcDoc={lockedImage(markup, alt)}
      className="block max-w-full border-0"
      // The frame's scheme is its document's (locked-image.ts), so it draws no canvas of its own.
      style={{
        width: size.width,
        aspectRatio: `${size.width} / ${size.height}`,
        colorScheme: "light",
      }}
    />
  )
}

// ImageView is a picture zoomed and turned as its controls say. Zoomed, it is dragged rather than scrolled, as an
// image viewer does; at 100% there is nothing to pan.
export function ImageView({
  source,
  alt,
  zoom,
  rotation,
  onZoom,
}: {
  source: ImageSource | undefined
  alt: string
  zoom: number
  rotation: number
  // Wheel over the picture; the same callback on every render, so its listener is attached once.
  onZoom?: (deltaY: number) => void
}) {
  const frame = useRef<HTMLDivElement>(null)
  const [pan, setPan] = useState({ x: 0, y: 0 })
  const [dragging, setDragging] = useState(false)
  const from = useRef<{ x: number; y: number } | null>(null)
  const pannable = zoom > 100
  // At 100% there is nothing to pan, so the offset is derived away rather than reset in an effect.
  const offset = pannable ? pan : { x: 0, y: 0 }
  const transform = `translate(${offset.x}px, ${offset.y}px) scale(${zoom / 100}) rotate(${rotation}deg)`
  useEffect(() => {
    const node = frame.current
    if (!node || !onZoom) return
    const onWheel = (event: WheelEvent) => {
      event.preventDefault()
      onZoom(event.deltaY)
    }
    node.addEventListener("wheel", onWheel, { passive: false })

    return () => node.removeEventListener("wheel", onWheel)
  }, [onZoom])

  return (
    <div
      ref={frame}
      className={cn(
        "relative flex min-h-0 flex-1 items-center justify-center overflow-hidden p-4",
        pannable && (dragging ? "cursor-grabbing" : "cursor-grab"),
      )}
      onPointerDown={(event) => {
        if (!pannable) return
        from.current = { x: event.clientX - offset.x, y: event.clientY - offset.y }
        setDragging(true)
        event.currentTarget.setPointerCapture(event.pointerId)
      }}
      onPointerMove={(event) => {
        if (!from.current) return
        setPan({ x: event.clientX - from.current.x, y: event.clientY - from.current.y })
      }}
      onPointerUp={() => {
        from.current = null
        setDragging(false)
      }}
      onPointerCancel={() => {
        from.current = null
        setDragging(false)
      }}
    >
      {source && "url" in source && (
        <img
          src={source.url}
          alt={alt}
          draggable={false}
          className="max-h-full max-w-full object-contain transition-transform duration-300 ease-in-out"
          style={{ transform }}
        />
      )}
      {source && "markup" in source && (
        <iframe
          title={alt}
          sandbox=""
          referrerPolicy="no-referrer"
          srcDoc={lockedImage(source.markup, alt)}
          // The frame takes no pointer: the drag and the wheel are the viewer's.
          className="pointer-events-none size-full border-0 transition-transform duration-300 ease-in-out"
          // The frame's scheme is its document's (locked-image.ts), so it draws no canvas of its own.
          style={{ transform, colorScheme: "light" }}
        />
      )}
    </div>
  )
}

export function ImageControls({
  zoom,
  minZoom = 25,
  maxZoom = 200,
  zoomStep = 25,
  onZoom,
  onRotate,
}: {
  zoom: number
  minZoom?: number
  maxZoom?: number
  zoomStep?: number
  onZoom: (zoom: number) => void
  onRotate: (degrees: number) => void
}) {
  return (
    <div className="flex items-center gap-1 rounded-xl border bg-popover/95 p-1 text-popover-foreground shadow-lg backdrop-blur-sm">
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label="Zoom out"
        disabled={zoom <= minZoom}
        onClick={() => onZoom(Math.max(zoom - zoomStep, minZoom))}
      >
        <ZoomOut />
      </Button>
      <span className="w-12 text-center text-xs font-medium tabular-nums">{zoom}%</span>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label="Zoom in"
        disabled={zoom >= maxZoom}
        onClick={() => onZoom(Math.min(zoom + zoomStep, maxZoom))}
      >
        <ZoomIn />
      </Button>
      <Button variant="ghost" size="icon-sm" aria-label="Turn right" onClick={() => onRotate(90)}>
        <RotateCw />
      </Button>
    </div>
  )
}
