import { useId } from "react"

import type { VideoTemplate } from "@/api/generated/types.gen"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { errorText } from "@/lib/server-error"

import { aspectNames, cardsText, lengthText } from "./template-state"

// TemplatePicker is Make video's template (issue #1230, row 5): none, or one of GET /videos/templates, and the
// skeleton of the one chosen: its use case, length, shapes and cards, then its sections, each with its words and
// seconds at 2.5 words a second, which the take's lines are written against. A picker is an inference: aidemo,
// agent-demo-video and autodemo have no library of templates to pick from (aidemo's starter is one, written by
// `aidemo init`).
export function TemplatePicker({
  templates,
  value,
  onChange,
}: {
  // templates: the list read, and why it could not be.
  templates: { data?: VideoTemplate[]; error: unknown }
  value: string
  onChange: (id: string) => void
}) {
  const id = useId()
  const list = templates.data ?? []
  const chosen = list.find((t) => t.id === value)
  const items = { none: "None", ...Object.fromEntries(list.map((t) => [t.id, t.name])) }

  return (
    <Field>
      <FieldLabel htmlFor={`${id}-template`}>Template</FieldLabel>
      <Select
        value={chosen ? chosen.id : "none"}
        onValueChange={(next) => next && onChange(next === "none" ? "" : next)}
        items={items}
        disabled={!templates.data}
      >
        <SelectTrigger id={`${id}-template`} width="full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="none">None</SelectItem>
          {list.map((t) => (
            <SelectItem key={t.id} value={t.id}>
              {t.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {templates.error !== null && templates.error !== undefined ? (
        <p role="alert" className="text-sm text-destructive">
          The templates could not be read: {errorText(templates.error)}
        </p>
      ) : !templates.data ? (
        <output className="flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner /> Reading the templates…
        </output>
      ) : chosen ? (
        <div
          className="flex flex-col gap-2 rounded-lg border bg-muted/40 p-3 text-sm"
          data-testid="template-skeleton"
        >
          <p>{chosen.use_case}</p>
          <p className="text-muted-foreground">
            {lengthText(chosen)} · {chosen.aspects.map((a) => aspectNames[a]).join(" or ")} ·{" "}
            {chosen.captions ? "captions" : "no captions"} · {cardsText(chosen)}
          </p>
          <ol className="flex list-decimal flex-col gap-1 ps-5">
            {chosen.sections.map((s) => (
              <li key={s.name}>
                <span className="font-medium">{s.name}</span>{" "}
                <span className="text-muted-foreground">
                  ~{s.words} words, {Math.round(s.seconds)} s
                </span>
                : {s.purpose}
              </li>
            ))}
          </ol>
        </div>
      ) : (
        <FieldDescription>
          A template sets the shape, the captions, the cards and the parts of the script.
        </FieldDescription>
      )}
    </Field>
  )
}
