import { useState } from "react"

import type { Model } from "@/api/generated/types.gen"
import {
  Combobox,
  ComboboxCollection,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxGroup,
  ComboboxInput,
  ComboboxItem,
  ComboboxLabel,
  ComboboxList,
  ComboboxSelectTrigger,
} from "@/components/ui/combobox"

import { choices, matches, type Choice, type Group } from "./model-choices"
import { ModelMark, ProviderMark } from "./model-mark"

// ModelPicker chooses a model from every provider's, with a search (issue #200). It is Omnara's model combobox
// (Apache License 2.0, https://github.com/omnara-ai/omnara; frontend/apps/web/src/components/ui/
// resource-combobox.tsx, resource-combobox-content.tsx, agents/AgentConfigModelField.tsx): a button that shows
// the choice, the search inside the popup above the list, the first match highlighted so Enter takes it, a letter
// typed on the closed button opening the popup with that letter.
// A row and the button lead with the mark of the model's maker, and a group's label with its provider's
// (model-mark.tsx, issue #205). The check on the chosen row and the provider said beside the name are t3code's row (apps/web/src/components/
// chat/ModelListRow.tsx). The groups by provider and the row for a model that is gone are this field's own, kept
// from the select it replaces.
export function ModelPicker({
  id,
  model,
  models,
  onChange,
  defaultLabel,
}: {
  id: string
  // model is "provider/name", a bare name, or "" for the default (profiles-state.ts, modelKey).
  model: string
  models: Model[]
  onChange: (model: string) => void
  defaultLabel: string
}) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState("")
  const groups = choices(models, model, defaultLabel)
  const chosen = groups.flatMap((g) => g.items).find((c) => c.key === model) ?? null

  return (
    <Combobox<Choice>
      items={groups}
      value={chosen}
      onValueChange={(next) => {
        if (next) onChange(next.key)
      }}
      open={open}
      onOpenChange={setOpen}
      onOpenChangeComplete={(now) => {
        if (!now) setQuery("")
      }}
      inputValue={query}
      onInputValueChange={setQuery}
      filter={matches}
      itemToStringLabel={(c) => c.name}
      itemToStringValue={(c) => c.key}
      isItemEqualToValue={(a, b) => a.key === b.key}
      autoHighlight
    >
      <ComboboxSelectTrigger
        id={id}
        onKeyDown={(event) => {
          if (event.key.length !== 1 || event.key === " ") return
          if (event.ctrlKey || event.metaKey || event.altKey) return

          event.preventDefault()
          event.preventBaseUIHandler()
          setQuery(event.key)
          setOpen(true)
        }}
      >
        <span className="flex min-w-0 items-center gap-2">
          {chosen && chosen.key !== "" && <ModelMark name={chosen.name} className="size-3.5" />}
          <span className="truncate">{chosen?.name ?? defaultLabel}</span>
          {chosen?.provider && (
            <span className="truncate text-xs text-muted-foreground">{chosen.provider}</span>
          )}
          {chosen?.gone && <span className="shrink-0 text-xs text-needs-you">not offered</span>}
        </span>
      </ComboboxSelectTrigger>
      <ComboboxContent>
        <ComboboxInput
          showTrigger={false}
          aria-label="Search models"
          placeholder="Search models or providers…"
        />
        <ComboboxEmpty>No models found.</ComboboxEmpty>
        <ComboboxList>
          {(group: Group) => (
            <ComboboxGroup key={group.value} items={group.items}>
              {group.value && (
                <ComboboxLabel>
                  <span className="flex items-center gap-1.5">
                    <ProviderMark kind={group.kind} className="size-3 opacity-70" />
                    {group.value}
                  </span>
                </ComboboxLabel>
              )}
              <ComboboxCollection>
                {(choice: Choice) => (
                  <ComboboxItem key={choice.key} value={choice}>
                    {choice.key !== "" && <ModelMark name={choice.name} className="size-3.5" />}
                    <span className="truncate">{choice.name}</span>
                    {choice.gone && (
                      <span className="shrink-0 text-xs text-muted-foreground">not offered</span>
                    )}
                  </ComboboxItem>
                )}
              </ComboboxCollection>
            </ComboboxGroup>
          )}
        </ComboboxList>
      </ComboboxContent>
    </Combobox>
  )
}
