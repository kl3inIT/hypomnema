import { Fragment } from "react"

import {
  Combobox,
  ComboboxChip,
  ComboboxChips,
  ComboboxChipsInput,
  ComboboxCollection,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxGroup,
  ComboboxItem,
  ComboboxLabel,
  ComboboxList,
  ComboboxValue,
  useComboboxAnchor,
} from "@/components/ui/combobox"
import { ModelMark, ProviderMark } from "@/components/icons/model-mark"
import { type PickGroup, type PickModel } from "./gateway-keys-state"

function matches(model: PickModel | undefined, title: string, query: string) {
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean)
  const text = `${model?.id ?? ""} ${model?.name ?? ""} ${title}`.toLowerCase()

  return terms.every((term) => text.includes(term))
}

// GatewayModelPicker chooses the models a key may use (issue #1277): the chosen ones as chips, each with its maker's
// logo, and a search that lists the others grouped by the provider that serves them first, as LiteLLM's key form
// picks its models in a chip multiselect (create_key_button.tsx) and new-api's drawer its models
// (api-keys-mutate-drawer.tsx); the groups and logos are those of 9router's picker (ModelSelectModal.js). The chips
// and the list are the shadcn combobox's (components/ui/combobox.tsx).
export function GatewayModelPicker({
  groups,
  chosen,
  onChange,
  invalid,
}: {
  groups: PickGroup[]
  chosen: string[]
  onChange: (chosen: string[]) => void
  invalid?: boolean
}) {
  const anchor = useComboboxAnchor()
  const models = new Map(groups.flatMap((g) => g.models.map((m) => [m.id, m] as const)))
  const titles = new Map(groups.flatMap((g) => g.models.map((m) => [m.id, g.title] as const)))
  const items = groups.map((g) => ({
    value: g.title,
    kind: g.kind,
    items: g.models.map((m) => m.id),
  }))
  const total = models.size

  return (
    <div className="flex flex-col gap-1.5">
      <Combobox
        multiple
        autoHighlight
        items={items}
        value={chosen}
        onValueChange={(next: string[]) => onChange(next)}
        itemToStringLabel={(id: string) => models.get(id)?.name ?? id}
        filter={(id: string, query: string) => matches(models.get(id), titles.get(id) ?? "", query)}
      >
        <ComboboxChips ref={anchor} aria-invalid={invalid || undefined}>
          <ComboboxValue>
            {(values: string[]) => (
              <Fragment>
                {values.map((id) => (
                  <ComboboxChip key={id} aria-label={models.get(id)?.name ?? id}>
                    <ModelMark name={id} className="size-3.5 shrink-0" />
                    {models.get(id)?.name ?? id}
                  </ComboboxChip>
                ))}
                <ComboboxChipsInput
                  aria-label="Search models"
                  placeholder={values.length ? "Add a model…" : "Search models…"}
                />
              </Fragment>
            )}
          </ComboboxValue>
        </ComboboxChips>
        <ComboboxContent anchor={anchor}>
          <ComboboxEmpty>No model matches.</ComboboxEmpty>
          <ComboboxList>
            {(group: (typeof items)[number]) => (
              <ComboboxGroup key={group.value} items={group.items}>
                <ComboboxLabel>
                  <span className="flex items-center gap-1.5">
                    <ProviderMark kind={group.kind} className="size-3.5 shrink-0" />
                    {group.value}
                  </span>
                </ComboboxLabel>
                <ComboboxCollection>
                  {(id: string) => {
                    const model = models.get(id)

                    return (
                      <ComboboxItem key={id} value={id}>
                        <ModelMark name={id} className="size-4 shrink-0" />
                        <span className="flex min-w-0 flex-col">
                          <span className="truncate">{model?.name ?? id}</span>
                          {model && model.name !== id && (
                            <span className="truncate font-mono text-xs text-muted-foreground">
                              {id}
                            </span>
                          )}
                        </span>
                      </ComboboxItem>
                    )
                  }}
                </ComboboxCollection>
              </ComboboxGroup>
            )}
          </ComboboxList>
        </ComboboxContent>
      </Combobox>
      <p className="text-xs text-muted-foreground" data-testid="chosen-count">
        {chosen.length} of {total} chosen. A model added later is not given until you add it here.
      </p>
    </div>
  )
}
