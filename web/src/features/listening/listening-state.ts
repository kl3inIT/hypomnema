import type {
  Listening,
  ListeningBackend,
  ListeningGroup,
  ListeningSource,
} from "@/api/generated/types.gen"
import type { Tone } from "@/features/models/models-state"

// What the Social listening page shows of each source (issue #1290). API only: built in the next commit.

type GroupInfo = { label: string; tone: Tone; why: string }

export const groupOrder: ListeningGroup[] = []

export function groupInfo(_group: ListeningGroup): GroupInfo {
  return { label: "", tone: "outline", why: "" }
}

export function codeLabel(code: string) {
  return code
}

export function backendState(_b: ListeningBackend): { label: string; tone: Tone; why?: string } {
  return { label: "", tone: "outline" }
}

export function needsYou(_source: ListeningSource) {
  return false
}

export function readLine(_source: ListeningSource, _now = Date.now()) {
  return ""
}

export function itemsLine(_source: ListeningSource) {
  return ""
}

export function failureShown(_source: ListeningSource) {
  return false
}

export function moved(order: string[], _name: string, _direction: "up" | "down") {
  return order
}

export function shownSources(listening: Listening, _group: ListeningGroup | null) {
  return listening.sources
}
