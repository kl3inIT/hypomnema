import { Minus, Plus } from "lucide-react"

import { Button } from "@/components/ui/button"

const step = 0.25

// Zoom is a quiet row of − 100% +: the percentage resets it (Omnigent's toolbar, Onyx's ZoomControls).
export function Zoom({
  scale,
  min,
  max,
  onChange,
}: {
  scale: number
  min: number
  max: number
  onChange: (scale: number) => void
}) {
  return (
    <div className="flex items-center gap-0.5">
      <Button
        variant="ghost"
        size="icon-xs"
        aria-label="Zoom out"
        disabled={scale <= min}
        onClick={() => onChange(scale - step)}
      >
        <Minus />
      </Button>
      <Button
        variant="ghost"
        size="xs"
        aria-label="Reset zoom"
        disabled={scale === 1}
        onClick={() => onChange(1)}
      >
        <span className="w-9 text-center tabular-nums">{Math.round(scale * 100)}%</span>
      </Button>
      <Button
        variant="ghost"
        size="icon-xs"
        aria-label="Zoom in"
        disabled={scale >= max}
        onClick={() => onChange(scale + step)}
      >
        <Plus />
      </Button>
    </div>
  )
}
