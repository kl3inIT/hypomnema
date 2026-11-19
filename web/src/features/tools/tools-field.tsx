import type { ToolAccess, ToolPreset } from "@/api/generated/types.gen"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

import { useToolFamilies } from "./use-tools"

const presetLabels: Record<ToolPreset, string> = { full: "Full", report: "Report" }

export function ToolsField({
  id,
  preset,
  tools,
  off,
  access,
  onChange,
  description,
}: {
  id: string
  preset: ToolPreset
  tools: string[]
  off: string[]
  access?: ToolAccess
  onChange: (change: { preset?: ToolPreset; tools?: string[]; toolsOff?: string[] }) => void
  description: string
}) {
  const families = useToolFamilies().data ?? []

  return (
    <fieldset className="flex min-w-0 flex-col gap-3">
      <legend className="mb-2 text-sm font-medium">Tools</legend>
      <FieldDescription>{description}</FieldDescription>
      <Field>
        <FieldLabel htmlFor={`${id}-preset`}>Preset</FieldLabel>
        <Select
          items={presetLabels}
          value={preset}
          onValueChange={(next) => {
            if (next) onChange({ preset: next })
          }}
        >
          <SelectTrigger id={`${id}-preset`} width="full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="full">Full</SelectItem>
            <SelectItem value="report">Report</SelectItem>
          </SelectContent>
        </Select>
        <FieldDescription>
          {preset === "report"
            ? "Memory, artifacts, routines, web and Superset."
            : "All families except phone."}
        </FieldDescription>
      </Field>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field>
          <FieldLabel htmlFor={`${id}-add`}>Add exceptions</FieldLabel>
          <Input
            id={`${id}-add`}
            list={`${id}-families`}
            placeholder="phone"
            value={tools.join(",")}
            onChange={(event) => onChange({ tools: event.target.value.split(",") })}
          />
        </Field>
        <Field>
          <FieldLabel htmlFor={`${id}-remove`}>Remove exceptions</FieldLabel>
          <Input
            id={`${id}-remove`}
            list={`${id}-families`}
            placeholder="browser"
            value={off.join(",")}
            onChange={(event) => onChange({ toolsOff: event.target.value.split(",") })}
          />
        </Field>
      </div>
      <datalist id={`${id}-families`}>
        {families.map((family) => (
          <option key={family.name} value={family.name}>
            {family.description}
          </option>
        ))}
      </datalist>
      <FieldDescription>Use commas between family or tool names. Removal wins.</FieldDescription>

      {access && (
        <div className="flex min-w-0 flex-col gap-2">
          <h3 className="text-sm font-medium">Saved tool access</h3>
          <ul aria-label="Saved tool access" className="divide-y rounded-lg border">
            {access.families.map((family) => (
              <li key={family.name}>
                <details className="px-3 py-2">
                  <summary
                    aria-label={`${family.name}: ${family.state}`}
                    className="cursor-pointer text-sm"
                  >
                    <span className={family.state === "unavailable" ? "text-muted-foreground" : ""}>
                      <span className="font-medium capitalize">{family.name}</span>
                      <span className="float-end rounded bg-muted px-2 text-xs text-muted-foreground capitalize">
                        {family.state}
                      </span>
                      <span className="block pt-0.5 text-xs text-muted-foreground">
                        {family.reason}
                      </span>
                    </span>
                  </summary>
                  <p className="mt-1 text-xs text-muted-foreground">{family.description}</p>
                  <ul aria-label={`${family.name} tools`} className="mt-2 divide-y border-t">
                    {family.tools.map((tool) => (
                      <li key={tool.name} className="py-2 text-xs">
                        <span className="font-mono break-all">{tool.name}</span>
                        <span className="ms-2 text-muted-foreground capitalize">{tool.state}</span>
                        <p className="text-muted-foreground">
                          {tool.reason} · {tool.source}
                        </p>
                      </li>
                    ))}
                  </ul>
                </details>
              </li>
            ))}
          </ul>
        </div>
      )}
    </fieldset>
  )
}
