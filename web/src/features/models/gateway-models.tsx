import { useForm } from "@tanstack/react-form"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import {
  Brain,
  CircleAlert,
  CircleDollarSign,
  Image,
  Pencil,
  RotateCcw,
  Route,
  ScrollText,
  Type,
} from "lucide-react"
import { useId, useState, type ReactNode } from "react"

import "@/api/client"
import { setGatewayModelAttributes } from "@/api/generated/sdk.gen"
import type { GatewayModel, GatewayModelSource } from "@/api/generated/types.gen"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field"
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemTitle,
} from "@/components/ui/item"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { ModelMark } from "@/features/agents/model-mark"
import { ErrorAlert, TextField } from "@/features/settings/settings-parts"
import { said } from "@/features/settings/use-integrations"
import { modelName } from "@/lib/model-name"

import {
  formOf,
  fromModelsDev,
  inputName,
  modelFormSchema,
  settingsOf,
  shortTokens,
  tokenCount,
  type ModelForm,
} from "./gateway-models-state"
import { gatewayModelsKey } from "./use-models"

// GatewayModels are the models the gateway serves (issue #367): each with where it is served from and what a
// client is told of it, its window, largest output, inputs, reasoning and price, as 9router-go's custom models and
// LiteLLM's model_info hold them. A value comes from models.dev unless the model sets its own, which the owner does
// here, as LiteLLM's model edit and omniroute's capability overrides let one; an empty field is models.dev's again.
export function GatewayModels({ models }: { models: GatewayModel[] }) {
  return (
    <Card size="sm" data-testid="gateway-models">
      <CardContent>
        <ItemGroup>
          {models.map((model) => (
            <GatewayModelRow key={model.name} model={model} />
          ))}
        </ItemGroup>
      </CardContent>
    </Card>
  )
}

function GatewayModelRow({ model }: { model: GatewayModel }) {
  const a = model.attributes

  return (
    <Item variant="muted" size="sm" aria-label={`Gateway model ${model.name}`}>
      <ItemContent>
        <ItemTitle>
          <ModelMark
            name={model.targets[0]?.upstream_model ?? model.name}
            className="size-4 shrink-0"
          />
          <span title={model.name}>{modelName(model.name)}</span>
        </ItemTitle>
        <ItemDescription>
          <span className="flex flex-col gap-1">
            <span className="flex items-start gap-1">
              <Route className="mt-0.5 size-3 shrink-0" aria-hidden />
              <span>
                {model.targets.map((t) => `${t.provider} · ${t.upstream_model}`).join(", then ")}
              </span>
            </span>
            <span className="flex flex-wrap items-center gap-1.5">
              {a.context_window.value ? (
                <Fact icon={ScrollText} source={a.context_window.source}>
                  {`${shortTokens(a.context_window.value)} context`}
                </Fact>
              ) : (
                <Badge
                  variant="warning"
                  title="models.dev does not know this model: set its window"
                >
                  <CircleAlert data-icon="inline-start" aria-hidden />
                  Context not known, set it
                </Badge>
              )}
              {a.max_output.value !== undefined && (
                <Fact icon={Type} source={a.max_output.source}>
                  {`${shortTokens(a.max_output.value)} out`}
                </Fact>
              )}
              {a.input.value && (
                <Fact icon={Image} source={a.input.source}>
                  {inputName(a.input.value)}
                </Fact>
              )}
              {a.reasoning.value !== undefined && (
                <Fact icon={Brain} source={a.reasoning.source}>
                  {a.reasoning.value
                    ? a.efforts.value?.length
                      ? `Reasons: ${a.efforts.value.join(", ")}`
                      : "Reasons"
                    : "No reasoning"}
                </Fact>
              )}
              {a.price && (
                <Fact icon={CircleDollarSign} source="models_dev">
                  {`$${a.price.input} in · $${a.price.output} out per 1M`}
                </Fact>
              )}
            </span>
          </span>
        </ItemDescription>
      </ItemContent>
      <div className="self-start">
        <ItemActions>
          <EditGatewayModel model={model} />
        </ItemActions>
      </div>
    </Item>
  )
}

const sourceNames: Record<GatewayModelSource, string> = {
  set: "Set on this model",
  models_dev: "From models.dev",
  codex: "From Codex's own list of models",
  unknown: "Not known",
}

// Fact is one thing a client is told of a model, a chip with its icon as omniroute's CapabilityBadge and new-api's
// capability pills are; one the model sets is marked, and each says where it came from.
function Fact({
  icon: Icon,
  source,
  children,
}: {
  icon: typeof Brain
  source: GatewayModelSource
  children: ReactNode
}) {
  return (
    <Badge
      variant={source === "set" ? "secondary" : "outline"}
      title={sourceNames[source]}
      data-source={source}
    >
      <Icon data-icon="inline-start" aria-hidden />
      {children}
      {source === "set" && <span className="text-foreground">· set</span>}
    </Badge>
  )
}

// Reset puts a field back to its default, as omniroute's override editor does: shown only while the field sets
// a value of its own.
function Reset({ shown, onReset, label }: { shown: boolean; onReset: () => void; label: string }) {
  if (!shown) return null

  return (
    <Button
      type="button"
      variant="ghost"
      size="xs"
      aria-label={`Use the default ${label}`}
      onClick={onReset}
    >
      <RotateCcw data-icon="inline-start" />
      Default
    </Button>
  )
}

const inputItems: Record<ModelForm["input"], string> = {
  auto: "From models.dev",
  text: "Text only",
  "text,image": "Text and image",
}

const reasoningItems: Record<ModelForm["reasoning"], string> = {
  auto: "From models.dev",
  yes: "Reasons",
  no: "Does not reason",
}

// EditGatewayModel is the form for what a model sets of its attributes, a dialog as a gateway key's form is. Each
// field shows models.dev's value where it is empty.
export function EditGatewayModel({ model }: { model: GatewayModel }) {
  const [open, setOpen] = useState(false)
  const queryClient = useQueryClient()
  const formId = useId()
  const a = model.attributes

  const save = useMutation({
    mutationFn: async (form: ModelForm) =>
      (
        await setGatewayModelAttributes({
          path: { model: model.name },
          body: settingsOf(form),
        })
      ).data,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: gatewayModelsKey })
      setOpen(false)
      said(`${modelName(model.name)} was saved`)
    },
  })

  const form = useForm({
    defaultValues: formOf(model),
    validators: { onSubmit: modelFormSchema },
    onSubmit: ({ value }) => save.mutate(value),
  })

  function change(next: boolean) {
    setOpen(next)
    if (next) {
      form.reset(formOf(model))
      save.reset()
    }
  }

  const devTokens = (v: number | undefined) => (v === undefined ? undefined : tokenCount(v))
  const devInput = inputName(a.input.models_dev)
  const devReasoning =
    a.reasoning.models_dev === undefined
      ? undefined
      : a.reasoning.models_dev
        ? "Reasons"
        : "Does not reason"

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger
        render={
          <Button variant="ghost" size="sm" aria-label={`Edit ${model.name}`}>
            <Pencil data-icon="inline-start" />
            Edit
          </Button>
        }
      />
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{modelName(model.name)}</DialogTitle>
          <DialogDescription>
            What clients such as pi are told of this model. Leave a field empty to take
            models.dev&apos;s value.
          </DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <form
            id={formId}
            autoComplete="off"
            noValidate
            className="flex flex-col gap-4"
            onSubmit={(event) => {
              event.preventDefault()
              void form.handleSubmit()
            }}
          >
            <FieldGroup>
              <div className="grid gap-4 sm:grid-cols-2">
                <form.Field name="context_window">
                  {(field) => (
                    <TextField
                      field={field}
                      label="Context window (tokens)"
                      action={
                        <Reset
                          shown={field.state.value !== ""}
                          onReset={() => field.handleChange("")}
                          label="context window"
                        />
                      }
                      placeholder={
                        devTokens(
                          a.context_window.source === "codex"
                            ? a.context_window.value
                            : a.context_window.models_dev,
                        ) ?? "128,000"
                      }
                      description={
                        a.context_window.source === "codex"
                          ? `From Codex's own list: ${devTokens(a.context_window.value)}; models.dev gives the API's ${devTokens(a.context_window.models_dev) ?? "–"}.`
                          : fromModelsDev(devTokens(a.context_window.models_dev))
                      }
                    />
                  )}
                </form.Field>
                <form.Field name="max_output">
                  {(field) => (
                    <TextField
                      field={field}
                      label="Largest output (tokens)"
                      action={
                        <Reset
                          shown={field.state.value !== ""}
                          onReset={() => field.handleChange("")}
                          label="largest output"
                        />
                      }
                      placeholder={devTokens(a.max_output.models_dev) ?? "16,384"}
                      description={fromModelsDev(devTokens(a.max_output.models_dev))}
                    />
                  )}
                </form.Field>
                <form.Field name="input">
                  {(field) => (
                    <Field>
                      <div className="flex items-center justify-between gap-2">
                        <FieldLabel htmlFor={`${formId}-input`}>Input</FieldLabel>
                        <Reset
                          shown={field.state.value !== "auto"}
                          onReset={() => field.handleChange("auto")}
                          label="input"
                        />
                      </div>
                      <Select
                        value={field.state.value}
                        onValueChange={(next) => next && field.handleChange(next)}
                        items={{ ...inputItems, auto: choiceFromModelsDev(devInput) }}
                      >
                        <SelectTrigger id={`${formId}-input`} width="full" aria-label="Input">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {(Object.keys(inputItems) as ModelForm["input"][]).map((v) => (
                            <SelectItem key={v} value={v}>
                              {v === "auto" ? choiceFromModelsDev(devInput) : inputItems[v]}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                      <FieldDescription>
                        An image is sent only to a model that takes one.
                      </FieldDescription>
                    </Field>
                  )}
                </form.Field>
                <form.Field name="reasoning">
                  {(field) => (
                    <Field>
                      <div className="flex items-center justify-between gap-2">
                        <FieldLabel htmlFor={`${formId}-reasoning`}>Reasoning</FieldLabel>
                        <Reset
                          shown={field.state.value !== "auto"}
                          onReset={() => field.handleChange("auto")}
                          label="reasoning"
                        />
                      </div>
                      <Select
                        value={field.state.value}
                        onValueChange={(next) => next && field.handleChange(next)}
                        items={{ ...reasoningItems, auto: choiceFromModelsDev(devReasoning) }}
                      >
                        <SelectTrigger
                          id={`${formId}-reasoning`}
                          width="full"
                          aria-label="Reasoning"
                        >
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {(Object.keys(reasoningItems) as ModelForm["reasoning"][]).map((v) => (
                            <SelectItem key={v} value={v}>
                              {v === "auto" ? choiceFromModelsDev(devReasoning) : reasoningItems[v]}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </Field>
                  )}
                </form.Field>
              </div>
              <form.Field name="efforts">
                {(field) => (
                  <TextField
                    field={field}
                    label="Effort levels"
                    action={
                      <Reset
                        shown={field.state.value !== ""}
                        onReset={() => field.handleChange("")}
                        label="effort levels"
                      />
                    }
                    placeholder={a.efforts.models_dev?.join(", ") ?? "low, medium, high"}
                    description={`${fromModelsDev(a.efforts.models_dev?.join(", "))}. Levels among none, minimal, low, medium, high, xhigh and max, separated by commas.`}
                  />
                )}
              </form.Field>
            </FieldGroup>
          </form>
        </DialogPanel>
        <ErrorAlert error={save.error} />
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button type="submit" form={formId} disabled={save.isPending}>
            {save.isPending && <Spinner data-icon="inline-start" />}
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function choiceFromModelsDev(value: string | undefined) {
  return value ? `From models.dev (${value})` : "From models.dev (not known)"
}
