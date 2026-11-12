import { mascotBodies } from "./mascot-bodies"

// The colours a mascot is filled with: OpenMausBot's, without its two metals (src/lib/mascot.ts, MAUS_COLORS).
const colors = [
  "#009957",
  "#377FE6",
  "#D94B52",
  "#E78531",
  "#8057C8",
  "#0EA5C6",
  "#D84F8B",
  "#D8A729",
  "#01A492",
  "#E5634E",
] as const

// hash is FNV-1a over a name: the same name always gives the same number.
function hash(name: string) {
  let h = 0x811c9dc5

  for (let i = 0; i < name.length; i++) {
    h ^= name.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }

  return h >>> 0
}

// look is the body and the colour an agent's name gives it. The two are taken from different parts of the
// number, so agents that share a colour seldom share a body.
export function look(name: string) {
  const h = hash(name)

  return {
    body: mascotBodies[h % mascotBodies.length]!,
    color: colors[(h >>> 8) % colors.length]!,
  }
}
