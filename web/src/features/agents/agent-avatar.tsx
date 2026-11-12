import { useId } from "react"

import { look } from "./agent-look"
import { faceBox, faceCentre, mascotEyes } from "./mascot-bodies"

// The picture leaves a margin around the face box (mascot-motion.ts: VIEW_SPAN is the box and 15 a side).
const margin = 15
const viewBox = `${-margin} ${-margin} ${faceBox + 2 * margin} ${faceBox + 2 * margin}`

// AgentAvatar is an agent's face: OpenMausBot's mascot (Apache License 2.0, https://github.com/milind-soni/
// OpenMausBot; src/components/CursorAvatar.tsx draws it, src/components/Avatar.tsx makes the gradient from a
// colour), one of its bodies filled with one of its colours, with the idle eyes. Changed: it is drawn still, with
// no expressions, gaze or motion, and the body and colour come from the agent's name where OpenMausBot stores
// what the person picked (owner, 2026-11-11: an avatar at random).
export function AgentAvatar({ name, className }: { name: string; className?: string }) {
  const id = useId()
  const { body, color } = look(name)
  const [x, y, scale] = body.anchor

  return (
    <svg viewBox={viewBox} className={className} aria-hidden>
      <defs>
        <linearGradient id={`${id}-fill`} x1="1" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={`color-mix(in srgb, ${color} 45%, #ffffff)`} />
          <stop offset="55%" stopColor={color} />
          <stop offset="100%" stopColor={`color-mix(in srgb, ${color} 58%, #000000)`} />
        </linearGradient>
        <clipPath id={`${id}-clip`} transform={body.fit}>
          <path d={body.d} />
        </clipPath>
      </defs>
      <path d={body.d} transform={body.fit} fill={`url(#${id}-fill)`} />
      <g clipPath={`url(#${id}-clip)`}>
        <g
          transform={`translate(${x} ${y}) scale(${scale}) translate(${-faceCentre[0]} ${-faceCentre[1]})`}
          className="fill-white"
        >
          {mascotEyes.map((d) => (
            <path key={d} d={d} />
          ))}
        </g>
      </g>
    </svg>
  )
}
