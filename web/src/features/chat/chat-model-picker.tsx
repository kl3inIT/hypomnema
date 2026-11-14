import { Gauge } from "lucide-react"
import { useState } from "react"

import type { GatewayModel, Model } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
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
  ComboboxTrigger,
} from "@/components/ui/combobox"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { choices, matches, type Choice, type Group } from "@/features/agents/model-choices"
import { ModelMark, ProviderMark } from "@/features/agents/model-mark"
import { splitModel } from "@/features/agents/profiles-state"
import { useModels } from "@/features/agents/use-profiles"
import { useBots } from "@/features/bots/use-bots"
import { useGatewayModels } from "@/features/models/use-models"
import { modelName } from "@/lib/model-name"

import {
  keyOf,
  levelLabel,
  levelsOf,
  piModels,
  running,
  withModel,
  type ChatChoice,
} from "./chat-model-state"

// The model and effort of a chat, in the composer's footer left of Send (issue #369), as t3code's composer has
// them (MIT, Copyright (c) 2026 T3 Tools Inc., https://github.com/pingdotgg/t3code; apps/web/src/components/chat/
// ProviderModelPicker.tsx, TraitsPicker.tsx, ComposerModelEffort.tsx): a quiet button with the model's mark and
// name that opens a searchable list grouped by provider, and beside it a button for the effort listing only the
// model's own levels, absent for a model that does not reason. The list is the app's model picker
// (features/agents/model-picker.tsx: Omnara's combobox with AI Elements' and assistant-ui's marks), its first row
// the Bot's own model, so a chat can go back to it; the effort's first row is the Bot's too, as t3code marks a
// level "Default". Orca's session combobox (NativeChatModelCombobox.tsx) and AI Elements' ModelSelector keep the
// search above the list in the same way.
export function ChatModelControls({
  bot,
  choice,
  onChange,
  disabled,
}: {
  bot: string
  choice: ChatChoice
  onChange: (next: ChatChoice) => void
  disabled?: boolean
}) {
  const bots = useBots().data
  const models = piModels(useModels().data ?? [])
  const gateway = useGatewayModels().data ?? []
  const own = bots?.find((b) => b.name === bot)
  const now = running(choice, own)

  return (
    <>
      <ModelButton
        bot={bot}
        botModel={own?.model ?? ""}
        model={now.model}
        chosen={keyOf(choice)}
        models={models}
        disabled={disabled}
        onPick={(key) => {
          if (key === "") return onChange({ ...choice, provider: "", model: "" })
          const { provider, name } = splitModel(key, models)
          onChange(withModel(choice, { provider, model: name }, gateway))
        }}
      />
      <EffortButton
        model={now.model}
        gateway={gateway}
        effort={now.effort}
        chosen={choice.effort}
        botEffort={own?.effort ?? ""}
        disabled={disabled}
        onPick={(effort) => onChange({ ...choice, effort })}
      />
    </>
  )
}

function ModelButton({
  bot,
  botModel,
  model,
  chosen,
  models,
  disabled,
  onPick,
}: {
  bot: string
  botModel: string
  // model is what the chat runs with, chosen the key of the chat's own ("" for the Bot's).
  model: string
  chosen: string
  models: Model[]
  disabled?: boolean
  onPick: (key: string) => void
}) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState("")
  const botsLabel = botModel ? `${bot}'s model: ${modelName(botModel)}` : `${bot}'s model`
  const groups = choices(models, chosen, botsLabel)
  const value = groups.flatMap((g) => g.items).find((c) => c.key === chosen) ?? null
  const name = model ? modelName(model) : "Default model"

  return (
    <Combobox<Choice>
      items={groups}
      value={value}
      onValueChange={(next) => {
        if (next) onPick(next.key)
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
      disabled={disabled}
    >
      <ComboboxTrigger
        aria-label={`Model: ${name}`}
        render={<Button type="button" variant="ghost" size="sm" />}
      >
        {model && <ModelMark name={model} className="size-3.5" />}
        <span className="max-w-32 truncate sm:max-w-48">{name}</span>
      </ComboboxTrigger>
      <ComboboxContent side="top" align="end" width="list">
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
                {(c: Choice) => (
                  <ComboboxItem key={c.key} value={c}>
                    <ModelMark name={c.key === "" ? botModel : c.id} className="size-3.5" />
                    <span className="truncate">{c.name}</span>
                    {c.gone && (
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

// botsLevel stands for "the Bot's effort" in the radio group, whose values cannot be empty.
const botsLevel = "bot"

function EffortButton({
  model,
  gateway,
  effort,
  chosen,
  botEffort,
  disabled,
  onPick,
}: {
  model: string
  gateway: GatewayModel[]
  // effort is what the chat runs with, chosen the chat's own ("" for the Bot's).
  effort: string
  chosen: string
  botEffort: string
  disabled?: boolean
  onPick: (effort: string) => void
}) {
  const levels = levelsOf(model, gateway)
  if (levels.length === 0) return null

  const label = effort ? levelLabel(effort) : "Default"

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        disabled={disabled}
        render={<Button type="button" variant="ghost" size="sm" aria-label={`Effort: ${label}`} />}
      >
        <Gauge />
        {label}
      </DropdownMenuTrigger>
      <DropdownMenuContent side="top" align="end" width="content">
        <DropdownMenuGroup>
          <DropdownMenuLabel>Effort</DropdownMenuLabel>
          <DropdownMenuRadioGroup
            value={chosen || botsLevel}
            onValueChange={(value: string) => onPick(value === botsLevel ? "" : value)}
          >
            <DropdownMenuRadioItem value={botsLevel} closeOnClick>
              {botEffort ? `Bot's: ${levelLabel(botEffort)}` : "Bot's default"}
            </DropdownMenuRadioItem>
            {levels.map((level) => (
              <DropdownMenuRadioItem key={level} value={level} closeOnClick>
                {levelLabel(level)}
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
