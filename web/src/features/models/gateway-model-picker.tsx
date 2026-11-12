import { Search } from "lucide-react"
import { useState } from "react"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Field, FieldLabel } from "@/components/ui/field"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { ModelMark, ProviderMark } from "@/features/agents/model-mark"
import { type PickGroup, type PickModel } from "./gateway-keys-state"

function matches(model: PickModel, title: string, query: string) {
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean)
  const text = `${model.id} ${model.name} ${title}`.toLowerCase()

  return terms.every((term) => text.includes(term))
}

// GatewayModelPicker chooses the models a key may use. From MemoryOS's model fields (D:\MemoryOS\web\src\features\
// models\model-picker.tsx, provider-models-field.tsx): one group a provider under its name, a model's logo before a
// name to read with its id quieter under it, a search, how many are chosen, "Select all shown" and "Clear". The
// group's own box chooses all of a provider, as 9router's picker groups models by provider (ModelSelectModal.js).
export function GatewayModelPicker({
  groups,
  chosen,
  onChange,
}: {
  groups: PickGroup[]
  chosen: string[]
  onChange: (chosen: string[]) => void
}) {
  const [query, setQuery] = useState("")
  const total = groups.reduce((n, g) => n + g.models.length, 0)
  const shown = groups
    .map((g) => ({ ...g, models: g.models.filter((m) => matches(m, g.title, query)) }))
    .filter((g) => g.models.length > 0)
  const shownIds = shown.flatMap((g) => g.models.map((m) => m.id))
  const has = new Set(chosen)

  function set(ids: string[], on: boolean) {
    const next = new Set(chosen)
    for (const id of ids) {
      if (on) next.add(id)
      else next.delete(id)
    }

    onChange([...next])
  }

  return (
    <div className="flex flex-col overflow-hidden rounded-lg border">
      <div className="border-b p-2">
        <InputGroup>
          <InputGroupAddon>
            <Search />
          </InputGroupAddon>
          <InputGroupInput
            type="search"
            aria-label="Search models"
            placeholder="Search models…"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
        </InputGroup>
      </div>
      <div className="flex items-center gap-1 border-b px-3 py-1.5 text-xs text-muted-foreground">
        <span className="me-auto" data-testid="chosen-count">
          <span className="font-medium text-foreground">{chosen.length} selected</span> of {total}
        </span>
        <Button type="button" variant="ghost" size="xs" onClick={() => set(shownIds, true)}>
          Select all shown
        </Button>
        <Button type="button" variant="ghost" size="xs" onClick={() => onChange([])}>
          Clear
        </Button>
      </div>
      <div className="max-h-72 overflow-y-auto py-1">
        {shown.length === 0 && (
          <p className="px-3 py-4 text-center text-sm text-muted-foreground">No model matches.</p>
        )}
        {shown.map((group) => {
          const ids = group.models.map((m) => m.id)
          const on = ids.filter((id) => has.has(id)).length
          const groupId = `provider-${group.provider}`

          return (
            <fieldset key={group.provider} aria-label={group.title}>
              <div className="px-3 pt-2 pb-1">
                <Field orientation="horizontal">
                  <Checkbox
                    id={groupId}
                    aria-label={`Every model of ${group.title}`}
                    checked={on === ids.length}
                    indeterminate={on > 0 && on < ids.length}
                    onCheckedChange={(checked) => set(ids, checked)}
                  />
                  <span className="flex flex-1 items-center gap-2 text-xs font-medium text-muted-foreground">
                    <ProviderMark kind={group.kind} className="size-4 shrink-0" />
                    {group.title}
                    <span className="ms-auto font-normal">
                      {on} of {ids.length}
                    </span>
                  </span>
                </Field>
              </div>
              {group.models.map((model) => {
                const id = `model-${model.id}`

                return (
                  <div key={model.id} className="px-3 py-1.5 hover:bg-muted/50">
                    <Field orientation="horizontal">
                      <Checkbox
                        id={id}
                        checked={has.has(model.id)}
                        onCheckedChange={(checked) => set([model.id], checked)}
                      />
                      <FieldLabel htmlFor={id}>
                        <ModelMark name={model.id} className="size-4.5 shrink-0" />
                        <span className="flex min-w-0 flex-col">
                          <span>{model.name}</span>
                          {model.name !== model.id && (
                            <span className="font-mono text-xs font-normal text-muted-foreground">
                              {model.id}
                            </span>
                          )}
                        </span>
                      </FieldLabel>
                    </Field>
                  </div>
                )
              })}
            </fieldset>
          )
        })}
      </div>
    </div>
  )
}
