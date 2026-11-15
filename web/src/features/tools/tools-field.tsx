import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Switch } from "@/components/ui/switch"
import { turned } from "@/features/skills/skills-state"

import { familyOn, kept, others } from "./tools-state"
import { useToolFamilies } from "./use-tools"

// ToolsField is the Hypomnema tools an agent or a Bot gets (issue #536, step 7), in its form, saved with it: a row
// per family with a switch, on unless this one turns it off, the control the skills for everyone have
// (SkillsField, after OpenMausBot's per-bot switches, src/components/bot-settings/SkillsSection.tsx), as OpenClaw's
// agent settings switch its tools by group (docs/gateway/config-tools/tool-policy.md, tool groups). A switch writes
// the family's entry in tools_off. A list of only some tools, or a single tool turned off, is set in the YAML or
// with the CLI; the form shows it and keeps it.
export function ToolsField({
  id,
  tools,
  off,
  onChange,
  description,
}: {
  id: string
  tools: string[]
  off: string[]
  onChange: (change: { toolsOff: string[] }) => void
  description: string
}) {
  const families = useToolFamilies().data ?? []
  const otherOff = others(families, off)

  return (
    <Field>
      <FieldLabel htmlFor={families[0] ? `${id}-on-${families[0].name}` : undefined}>
        Tools
      </FieldLabel>

      {families.length > 0 && (
        <ul aria-label="Tools" className="flex flex-col divide-y rounded-lg border">
          {families.map((f) => {
            const allowed = kept(f, tools)

            return (
              <li key={f.name} className="flex min-w-0 items-center gap-3 px-3 py-2">
                <label htmlFor={`${id}-on-${f.name}`} className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium">{f.name}</span>
                  <span className="block truncate text-xs text-muted-foreground">
                    {f.description}
                  </span>
                </label>
                {!allowed && (
                  <span className="shrink-0 text-xs text-muted-foreground">Not in its list</span>
                )}
                <Switch
                  id={`${id}-on-${f.name}`}
                  size="sm"
                  checked={familyOn(f, tools, off)}
                  disabled={!allowed}
                  onCheckedChange={(next) => onChange({ toolsOff: turned(off, f.pattern, !next) })}
                />
              </li>
            )
          })}
        </ul>
      )}

      {(tools.length > 0 || otherOff.length > 0) && (
        <p className="text-xs text-muted-foreground">
          {tools.length > 0 && <>Only: {tools.join(", ")}. </>}
          {otherOff.length > 0 && <>Also off: {otherOff.join(", ")}.</>}
        </p>
      )}
      <FieldDescription>{description}</FieldDescription>
    </Field>
  )
}
