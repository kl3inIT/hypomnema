const ago = new Intl.RelativeTimeFormat(undefined, { style: "narrow" })

// since says how long ago a moment was: "just now" under a minute, as t3code's formatRelativeElapsed
// (apps/web/src/timestampFormat.ts), then "5m ago", "3h ago", "2d ago".
export function since(iso: string, now = Date.now()) {
  const seconds = Math.max(0, (now - new Date(iso).getTime()) / 1000)
  if (seconds < 60) return "just now"

  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return ago.format(-minutes, "minute")
  if (minutes < 60 * 24) return ago.format(-Math.round(minutes / 60), "hour")

  return ago.format(-Math.round(minutes / (60 * 24)), "day")
}

// span says how long something has lasted: "40s", "6m", "2h 5m".
export function span(fromIso: string, now = Date.now()) {
  const seconds = Math.max(0, Math.round((now - new Date(fromIso).getTime()) / 1000))
  if (seconds < 60) return `${seconds}s`

  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m`

  const hours = Math.floor(minutes / 60)

  return minutes % 60 ? `${hours}h ${minutes % 60}m` : `${hours}h`
}

// elapsed says how long something has lasted to the second while that is still short, as AffectUI's orchestration
// view rolls a duration up (owner, 2026-11-13: "1m 34s"): "34s", "1m 34s", then "2h 5m".
export function elapsed(fromIso: string, now = Date.now()) {
  const seconds = Math.max(0, Math.round((now - new Date(fromIso).getTime()) / 1000))
  if (seconds < 60) return `${seconds}s`

  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ${String(seconds % 60).padStart(2, "0")}s`

  return span(fromIso, now)
}
