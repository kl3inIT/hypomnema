import type { Routine, RoutineSchedule } from "@/api/generated/types.gen"

// The schedule a form edits, as quick choices with a raw cron expression last (issue #211). Adapted from
// OpenMausBot (Apache License 2.0, Copyright 2026 Milind Soni and OpenMausBot contributors,
// https://github.com/milind-soni/OpenMausBot): src/components/routines/cron-editor.ts (a familiar expression is
// read back as its choice, any other stays raw, and an unchanged schedule is kept exactly) and the interval
// presets of src/components/RoutineCalendarPage.tsx (INTERVAL_PRESETS L91, "Runs every … minutes" L633-660).
// Changed: the choices are the ones the server's schedule has (every N minutes, or a cron expression with its
// time zone); its words and next times come from the server, not from a cron parser in the page.

export type Choice = "hourly" | "daily" | "weekdays" | "weekly" | "minutes" | "cron"

export const choices: { value: Choice; label: string }[] = [
  { value: "hourly", label: "Hourly" },
  { value: "daily", label: "Daily" },
  { value: "weekdays", label: "Weekdays" },
  { value: "weekly", label: "Weekly" },
  { value: "minutes", label: "Every N minutes" },
  { value: "cron", label: "Cron" },
]

export const minutePresets = [5, 10, 15, 30, 60]

export type Draft = {
  choice: Choice
  minutes: number
  // time is "HH:MM", the wall clock in the time zone.
  time: string
  // weekday is 0 (Sunday) to 6.
  weekday: number
  expression: string
  timeZone: string
}

export const browserZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"

// timeZones lists the IANA zones the browser knows, UTC first, as OpenMausBot's CronScheduleFields does.
export const timeZones = (): string[] => [
  "UTC",
  ...(typeof Intl.supportedValuesOf === "function"
    ? Intl.supportedValuesOf("timeZone")
    : []
  ).filter((z) => z !== "UTC"),
]

export const weekdays = [
  "Sunday",
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
]

const pad = (n: number) => String(n).padStart(2, "0")

// preset reads a familiar expression: "M H * * *", "M H * * 1-5" and "M H * * D".
function preset(expression: string): Pick<Draft, "choice" | "time" | "weekday"> | null {
  const parts = expression.trim().split(/\s+/)
  if (parts.length !== 5) return null

  const [minute = "", hour = "", day, month, weekday = ""] = parts
  if (!/^\d+$/.test(minute) || Number(minute) > 59 || !/^\d+$/.test(hour) || Number(hour) > 23) {
    return null
  }
  if (day !== "*" || month !== "*") return null

  const time = `${pad(Number(hour))}:${pad(Number(minute))}`
  if (weekday === "*") return { choice: "daily", time, weekday: 1 }
  if (weekday === "1-5") return { choice: "weekdays", time, weekday: 1 }
  if (/^[0-7]$/.test(weekday)) return { choice: "weekly", time, weekday: Number(weekday) % 7 }

  return null
}

export const newDraft = (): Draft => ({
  choice: "hourly",
  minutes: 15,
  time: "09:00",
  weekday: 1,
  expression: "0 9 * * *",
  timeZone: browserZone(),
})

// draftOf reads a Routine's schedule back into the form.
export function draftOf(routine: Pick<Routine, "every_minutes" | "cron" | "timezone">): Draft {
  const draft = newDraft()

  if (routine.cron) {
    const known = preset(routine.cron)

    return {
      ...draft,
      ...(known ?? { choice: "cron" as const }),
      expression: routine.cron,
      timeZone: routine.timezone ?? draft.timeZone,
    }
  }

  const minutes = routine.every_minutes ?? 60

  return minutes === 60 ? { ...draft, choice: "hourly" } : { ...draft, choice: "minutes", minutes }
}

// scheduleOf is what the server is sent for a draft.
export function scheduleOf(draft: Draft): RoutineSchedule {
  const [hour, minute] = draft.time.split(":").map(Number)
  const at = `${minute || 0} ${hour || 0}`

  switch (draft.choice) {
    case "hourly":
      return { every_minutes: 60 }
    case "minutes":
      return { every_minutes: draft.minutes }
    case "daily":
      return { cron: `${at} * * *`, timezone: draft.timeZone }
    case "weekdays":
      return { cron: `${at} * * 1-5`, timezone: draft.timeZone }
    case "weekly":
      return { cron: `${at} * * ${draft.weekday}`, timezone: draft.timeZone }
    case "cron":
      return { cron: draft.expression.trim(), timezone: draft.timeZone }
  }
}

// usesZone: only a cron schedule has a time zone.
export const usesZone = (choice: Choice) => choice !== "hourly" && choice !== "minutes"
