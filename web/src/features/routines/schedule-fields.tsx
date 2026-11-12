import { useMemo, useState } from "react"

import type { RoutineSchedule } from "@/api/generated/types.gen"
import {
  Combobox,
  ComboboxCollection,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxInput,
  ComboboxItem,
  ComboboxList,
  ComboboxSelectTrigger,
} from "@/components/ui/combobox"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"

import { capital, errorText } from "./routines-state"
import {
  choices,
  minutePresets,
  scheduleOf,
  timeZones,
  usesZone,
  weekdays,
  type Choice,
  type Draft,
} from "./schedule-draft"
import { useSchedulePreview } from "./use-routines"

// ScheduleFields edits a Routine's schedule (issue #211). Adapted from OpenMausBot (Apache License 2.0, Copyright
// 2026 Milind Soni and OpenMausBot contributors, https://github.com/milind-soni/OpenMausBot):
// src/components/RoutineCalendarPage.tsx (the repeat choices as a row of pills L895-911, "Runs every … minutes"
// with its presets and a custom number L633-660), src/components/routines/CronScheduleFields.tsx (the expression
// with its help, the time zone from the IANA list, the next three runs in that zone). The box with the schedule in
// words beside its expression follows Hermes Agent (MIT, Copyright (c) 2025 Nous Research,
// https://github.com/NousResearch/hermes-agent; apps/desktop/src/app/cron/index.tsx L1616-1634). Changed: the
// words and the next times are the server's, which checks the schedule as it will keep it; no calendar.

export function ScheduleFields({
  value,
  onChange,
}: {
  value: Draft
  onChange: (draft: Draft) => void
}) {
  const update = (patch: Partial<Draft>) => onChange({ ...value, ...patch })
  const schedule = scheduleOf(value)
  const custom = !minutePresets.includes(value.minutes)

  return (
    <div className="flex flex-col gap-3">
      <ToggleGroup
        aria-label="How often"
        value={[value.choice]}
        onValueChange={(next: string[]) => next[0] && update({ choice: next[0] as Choice })}
        variant="choice"
        size="sm"
        spacing={1}
      >
        {choices.map((c) => (
          <ToggleGroupItem key={c.value} value={c.value}>
            {c.label}
          </ToggleGroupItem>
        ))}
      </ToggleGroup>

      {value.choice === "minutes" && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="font-medium">Runs every</span>
          <Select
            value={custom ? "custom" : String(value.minutes)}
            onValueChange={(next) => update({ minutes: next === "custom" ? 0 : Number(next) })}
          >
            <SelectTrigger aria-label="Minutes between runs" size="sm">
              <SelectValue>{(v: string) => (v === "custom" ? "Custom" : v)}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {minutePresets.map((m) => (
                <SelectItem key={m} value={String(m)}>
                  {m}
                </SelectItem>
              ))}
              <SelectItem value="custom">Custom</SelectItem>
            </SelectContent>
          </Select>
          {custom && (
            <div className="w-20">
              <Input
                type="number"
                min={5}
                max={1440}
                step={1}
                aria-label="Custom minutes"
                value={value.minutes || ""}
                onChange={(e) => update({ minutes: Number(e.target.value) })}
              />
            </div>
          )}
          <span>minutes</span>
        </div>
      )}

      {(value.choice === "daily" || value.choice === "weekdays" || value.choice === "weekly") && (
        <div className="flex flex-wrap items-end gap-2">
          {value.choice === "weekly" && (
            <div className="w-40">
              <Field>
                <FieldLabel htmlFor="routine-weekday">Day</FieldLabel>
                <Select
                  value={String(value.weekday)}
                  onValueChange={(next) => update({ weekday: Number(next) })}
                >
                  <SelectTrigger id="routine-weekday" width="full">
                    <SelectValue>{(v: string) => weekdays[Number(v)]}</SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {weekdays.map((d, i) => (
                      <SelectItem key={d} value={String(i)}>
                        {d}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </div>
          )}
          <div className="w-36">
            <Field>
              <FieldLabel htmlFor="routine-time">Time</FieldLabel>
              <Input
                id="routine-time"
                type="time"
                step={60}
                value={value.time}
                onChange={(e) => update({ time: e.target.value })}
              />
            </Field>
          </div>
        </div>
      )}

      {value.choice === "cron" && (
        <Field>
          <FieldLabel htmlFor="routine-cron">Cron expression</FieldLabel>
          <Input
            id="routine-cron"
            value={value.expression}
            onChange={(e) => update({ expression: e.target.value })}
            placeholder="0 9 * * 1-5"
            spellCheck={false}
            autoComplete="off"
            code
          />
          <FieldDescription>
            Five fields: minute, hour, day of month, month, day of week.
          </FieldDescription>
        </Field>
      )}

      {usesZone(value.choice) && (
        <Field>
          <FieldLabel htmlFor="routine-zone">Time zone</FieldLabel>
          <ZonePicker
            id="routine-zone"
            value={value.timeZone}
            onChange={(timeZone) => update({ timeZone })}
          />
        </Field>
      )}

      <Preview schedule={schedule} zone={usesZone(value.choice) ? value.timeZone : undefined} />
    </div>
  )
}

// ZonePicker chooses an IANA time zone with a search, as the model picker chooses a model.
function ZonePicker({
  id,
  value,
  onChange,
}: {
  id: string
  value: string
  onChange: (zone: string) => void
}) {
  const zones = useMemo(() => timeZones(), [])
  const [query, setQuery] = useState("")

  return (
    <Combobox<string>
      items={zones}
      value={value}
      onValueChange={(next) => next && onChange(next)}
      inputValue={query}
      onInputValueChange={setQuery}
      onOpenChangeComplete={(open) => {
        if (!open) setQuery("")
      }}
      autoHighlight
    >
      <ComboboxSelectTrigger id={id}>
        <span className="truncate">{value}</span>
      </ComboboxSelectTrigger>
      <ComboboxContent>
        <ComboboxInput
          showTrigger={false}
          aria-label="Search time zones"
          placeholder="Search time zones…"
        />
        <ComboboxEmpty>No time zone found.</ComboboxEmpty>
        <ComboboxList>
          <ComboboxCollection>
            {(zone: string) => (
              <ComboboxItem key={zone} value={zone}>
                {zone}
              </ComboboxItem>
            )}
          </ComboboxCollection>
        </ComboboxList>
      </ComboboxContent>
    </Combobox>
  )
}

// Preview is the schedule in words beside what is kept, and its next three runs, as the server says them.
function Preview({ schedule, zone }: { schedule: RoutineSchedule; zone?: string }) {
  const preview = useSchedulePreview(schedule)
  const kept = schedule.cron ?? `every ${schedule.every_minutes ?? "?"} min`

  if (preview.error) {
    return (
      <p role="alert" className="text-xs text-destructive">
        {capital(errorText(preview.error))}
      </p>
    )
  }

  const p = preview.data
  const format = new Intl.DateTimeFormat(undefined, {
    timeZone: zone,
    weekday: "short",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  })

  return (
    <div className="flex flex-col gap-2" aria-live="polite">
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-md bg-muted px-3 py-2 text-xs">
        <span className="font-medium text-foreground" data-testid="schedule-words">
          {p ? p.schedule.charAt(0).toUpperCase() + p.schedule.slice(1) : "…"}
        </span>
        <span className="font-mono text-muted-foreground">{kept}</span>
      </div>
      {p && p.next.length > 0 && (
        <div aria-label="Next runs" className="flex flex-col gap-0.5 text-xs text-muted-foreground">
          <span className="font-medium">Next runs{zone ? ` · ${zone}` : ""}</span>
          {p.next.map((at) => (
            <time key={at} dateTime={at}>
              {format.format(new Date(at))}
            </time>
          ))}
        </div>
      )}
    </div>
  )
}
