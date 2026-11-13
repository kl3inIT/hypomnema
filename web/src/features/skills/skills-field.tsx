import { Link } from "@tanstack/react-router"
import { CircleAlert, Trash2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"

import { pickable, turned } from "./skills-state"
import { useSkills } from "./use-skills"

// SkillsField is the skills an agent or a Bot gets (issue #21), in its form, saved with it. The skills for
// everyone are rows with a switch, on unless this one turns it off, as OpenMausBot's bot settings switch each
// library skill per bot (Apache License 2.0, https://github.com/milind-soni/OpenMausBot;
// src/components/bot-settings/SkillsSection.tsx). The skills it adds are rows with their description and a button
// that takes them off, and a select under them adds the next, as Omnara's agent config lists its skills (Apache
// License 2.0, https://github.com/omnara-ai/omnara; frontend/apps/web/src/components/agents/
// AgentConfigSkillsField.tsx, SelectedSkillRow) and OpenMausBot adds one from its library. A skill named that is
// no longer here is marked, as Omnara marks one no longer available. Changed: the select is the Fallbacks' (one
// list a form keeps), where Omnara opens a search box.
export function SkillsField({
  id,
  skills,
  off,
  onChange,
  description,
}: {
  id: string
  skills: string[]
  off: string[]
  onChange: (change: { skills?: string[]; skillsOff?: string[] }) => void
  description: string
}) {
  const all = useSkills().data ?? []
  const everyone = all.filter((s) => s.everyone)
  const free = pickable(all, skills)
  const described = new Map(all.map((s) => [s.name, s.latest.description]))

  return (
    <Field>
      <div className="flex flex-wrap items-baseline gap-x-3">
        <FieldLabel htmlFor={`${id}-add`}>Skills</FieldLabel>
        <Link
          to="/settings/skills"
          className="text-xs text-muted-foreground hover:text-foreground hover:underline"
        >
          Manage skills
        </Link>
      </div>

      {everyone.length > 0 && (
        <ul aria-label="Skills for everyone" className="flex flex-col divide-y rounded-lg border">
          {everyone.map((s) => {
            const on = !off.includes(s.name)

            return (
              <li key={s.name} className="flex min-w-0 items-center gap-3 px-3 py-2">
                <label htmlFor={`${id}-on-${s.name}`} className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium">{s.name}</span>
                  <span className="block truncate text-xs text-muted-foreground">
                    {s.latest.description}
                  </span>
                </label>
                <span className="shrink-0 text-xs text-muted-foreground">For everyone</span>
                <Switch
                  id={`${id}-on-${s.name}`}
                  size="sm"
                  checked={on}
                  onCheckedChange={(next) => onChange({ skillsOff: turned(off, s.name, !next) })}
                />
              </li>
            )
          })}
        </ul>
      )}

      {skills.length > 0 && (
        <ul aria-label="Skills added" className="flex flex-col divide-y rounded-lg border">
          {skills.map((name) => {
            const text = described.get(name)

            return (
              <li key={name} className="flex min-w-0 items-center gap-2 py-1.5 ps-3 pe-1.5">
                {text === undefined && all.length > 0 && (
                  <CircleAlert className="size-4 shrink-0 text-destructive" aria-hidden />
                )}
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium">{name}</p>
                  <p className="truncate text-xs text-muted-foreground">
                    {text ?? (all.length > 0 ? "No skill of this name any more." : "")}
                  </p>
                </div>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`Remove skill ${name}`}
                  onClick={() => onChange({ skills: skills.filter((n) => n !== name) })}
                >
                  <Trash2 aria-hidden />
                </Button>
              </li>
            )
          })}
        </ul>
      )}

      <Select
        value={null}
        disabled={free.length === 0}
        onValueChange={(picked) => {
          if (picked) onChange({ skills: [...skills, picked] })
        }}
      >
        <SelectTrigger id={`${id}-add`} width="full">
          <SelectValue
            placeholder={
              all.length === 0
                ? "No skills added yet"
                : free.length === 0
                  ? "Every skill is here"
                  : "Add a skill…"
            }
          />
        </SelectTrigger>
        <SelectContent>
          {free.map((s) => (
            <SelectItem key={s.name} value={s.name}>
              {s.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <FieldDescription>{description}</FieldDescription>
    </Field>
  )
}
