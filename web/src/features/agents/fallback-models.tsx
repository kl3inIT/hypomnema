import { Trash2 } from "lucide-react"

import type { Model } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"

import { ModelPicker } from "./model-picker"
import { maxFallbacks, modelKey } from "./profiles-state"

// FallbackModels is an agent's ordered fallback models, each provider/model, a Bot's and an Agent's (issue #1336), as
// OpenClaw's model fallbacks (src/config/zod-schema.agent-model.ts), drawn as OpenMausBot's backups of a bot (Apache
// License 2.0, https://github.com/milind-soni/OpenMausBot; src/components/bot-panel/BackupAis.tsx): numbered rows,
// each changed or removed in place, then one control that adds the next. models are those the list may name; a
// row's picker offers its own model and those not chosen yet.
export function FallbackModels({
  id,
  description,
  missing,
  primary,
  chain,
  models,
  onChange,
}: {
  // id names the controls: <id>-1 for the first row, <id>-add for the one that adds.
  id: string
  description: string
  // missing is said instead of description while the primary model is not one the list can follow.
  missing: string
  primary: string
  chain: string[]
  models: Model[]
  onChange: (chain: string[]) => void
}) {
  const gateway = models
  const canAdd = gateway.some((model) => modelKey(model.provider, model.name) === primary)
  const free = gateway.filter((model) => {
    const key = modelKey(model.provider, model.name)
    return key !== primary && !chain.includes(key)
  })

  return (
    <Field>
      <FieldLabel htmlFor={chain.length ? `${id}-1` : `${id}-add`}>Fallback models</FieldLabel>
      {chain.length > 0 && (
        <ol aria-label="Fallback models, in order" className="flex flex-col gap-1.5">
          {chain.map((key, index) => (
            <li key={key} className="flex items-center gap-2">
              <span className="w-3 text-xs text-muted-foreground tabular-nums" aria-hidden>
                {index + 1}
              </span>
              <div className="min-w-0 flex-1">
                <ModelPicker
                  id={`${id}-${index + 1}`}
                  model={key}
                  models={[
                    ...gateway.filter((model) => modelKey(model.provider, model.name) === key),
                    ...free,
                  ]}
                  defaultLabel="Remove fallback"
                  onChange={(next) =>
                    onChange(
                      next
                        ? chain.map((item, at) => (at === index ? next : item))
                        : chain.filter((item) => item !== key),
                    )
                  }
                />
              </div>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={`Remove fallback ${index + 1}`}
                onClick={() => onChange(chain.filter((item) => item !== key))}
              >
                <Trash2 aria-hidden />
              </Button>
            </li>
          ))}
        </ol>
      )}
      {canAdd && free.length > 0 && chain.length < maxFallbacks && (
        <ModelPicker
          id={`${id}-add`}
          model=""
          models={free}
          defaultLabel="Add a fallback…"
          onChange={(next) => {
            if (next) onChange([...chain, next])
          }}
        />
      )}
      <FieldDescription>{canAdd ? description : missing}</FieldDescription>
    </Field>
  )
}
