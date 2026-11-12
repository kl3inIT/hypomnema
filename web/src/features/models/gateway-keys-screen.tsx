import { useForm } from "@tanstack/react-form"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check, Copy, KeyRound, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { z } from "zod"

import "@/api/client"
import {
  createGatewayKey,
  listGatewayKeys,
  listGatewayModels,
  revokeGatewayKey,
} from "@/api/generated/sdk.gen"
import type { CreatedGatewayKey, GatewayKey } from "@/api/generated/types.gen"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty"
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from "@/components/ui/input-group"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { ConfirmDialog, ErrorAlert, TextField } from "@/features/settings/settings-parts"
import { SettingsPage } from "@/features/settings/settings-shell"
import { said } from "@/features/settings/use-integrations"
import { since } from "@/lib/time"

const keysKey = ["gateway-keys"] as const

// The name the gateway gives to "every model".
const every = "*"

// GatewayKeysScreen is where the owner gives a tool outside, such as opencode or omp, a key to the model gateway
// (issue #199, step 6). From Coder's AI Gateway keys (coder/site/src/pages/AISettingsPage/GatewayKeysPage/): a
// table of the name, the key's start, when it was last used and when it was made, a key made in a dialog that
// shows its value once ("cannot be shown again"), and a key removed from its row. From 9router's Endpoint & Key
// page (EndpointPageClient.js): the address a tool is given, the page's own origin with /v1, with a copy button.
// A key limited to some models, as LiteLLM, Bifrost and 9router limit theirs. The keys made for one worker or one
// Bot's chat are counted, not listed: they end with what they were made for.
export function GatewayKeysScreen() {
  const keys = useQuery({
    queryKey: keysKey,
    queryFn: async () => (await listGatewayKeys()).data,
  })

  return (
    <SettingsPage
      wide
      title="Gateway keys"
      description="Keys for tools outside, such as opencode or omp, to use your models through the gateway. A key's value is shown once."
      actions={<CreateKey />}
    >
      <BaseURL />
      {keys.isPending && (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner />
          Loading…
        </p>
      )}
      <ErrorAlert error={keys.error} />
      {keys.data && keys.data.data.length === 0 && (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <KeyRound />
            </EmptyMedia>
            <EmptyTitle>No gateway keys yet</EmptyTitle>
            <EmptyDescription>
              Create a key, then give a tool the address above as its base URL and the key as its
              API key.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}
      {keys.data && keys.data.data.length > 0 && <KeysTable keys={keys.data.data} />}
      {keys.data && keys.data.live_worker_keys > 0 && (
        <p className="text-sm text-muted-foreground" data-testid="worker-keys">
          {keys.data.live_worker_keys === 1
            ? "1 more key works now for a worker or a Bot. It ends with the work it was made for."
            : `${keys.data.live_worker_keys} more keys work now for workers and Bots. Each ends with the work it was made for.`}
        </p>
      )}
    </SettingsPage>
  )
}

// BaseURL is the address a tool is given: this page's own origin, where the gateway answers.
function BaseURL() {
  const url = `${window.location.origin}/v1`

  return (
    <Field>
      <FieldLabel htmlFor="gateway-base-url">Base URL</FieldLabel>
      <InputGroup>
        <InputGroupInput id="gateway-base-url" readOnly value={url} />
        <InputGroupAddon align="inline-end">
          <CopyButton value={url} label="Copy the base URL" done="The base URL was copied" />
        </InputGroupAddon>
      </InputGroup>
      <FieldDescription>
        OpenAI&apos;s format: /v1/chat/completions and /v1/models for any tool, /v1/responses for
        the Codex CLI.
      </FieldDescription>
    </Field>
  )
}

function CopyButton({ value, label, done }: { value: string; label: string; done: string }) {
  const [copied, setCopied] = useState(false)

  async function copy() {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      said(done)
    } catch {
      // No clipboard (an insecure page): the value stays selectable.
    }
  }

  return (
    <InputGroupButton size="icon-xs" aria-label={label} onClick={() => void copy()}>
      {copied ? <Check /> : <Copy />}
    </InputGroupButton>
  )
}

const dayFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" })

function KeysTable({ keys }: { keys: GatewayKey[] }) {
  return (
    <div className="min-w-0 overflow-x-auto rounded-xl border bg-card">
      <Table aria-label="Gateway keys">
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead>Key</TableHead>
            <TableHead>Models</TableHead>
            <TableHead>Last used</TableHead>
            <TableHead>Created</TableHead>
            <TableHead>
              <span className="sr-only">Actions</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {keys.map((key) => (
            <TableRow key={key.id} aria-label={key.name}>
              <TableCell>
                <span className="font-medium">{key.name}</span>
              </TableCell>
              <TableCell>
                <span className="font-mono text-muted-foreground">
                  {key.prefix ? `${key.prefix}…` : "—"}
                </span>
              </TableCell>
              <TableCell>
                <KeyModels models={key.models} />
              </TableCell>
              <TableCell>
                <span className="text-muted-foreground">
                  {key.last_used_at ? since(key.last_used_at) : "Never"}
                </span>
              </TableCell>
              <TableCell>
                <time dateTime={key.created_at} className="text-muted-foreground">
                  {dayFormat.format(new Date(key.created_at))}
                </time>
              </TableCell>
              <TableCell>
                <RevokeKey gatewayKey={key} />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

function KeyModels({ models }: { models: string[] }) {
  if (models.includes(every)) return <span className="text-muted-foreground">Every model</span>

  return (
    <span className="flex flex-wrap gap-1">
      {models.map((model) => (
        <Badge key={model} variant="outline">
          {model}
        </Badge>
      ))}
    </span>
  )
}

function RevokeKey({ gatewayKey }: { gatewayKey: GatewayKey }) {
  const queryClient = useQueryClient()

  return (
    <span className="flex justify-end">
      <ConfirmDialog
        trigger={
          <Button variant="ghost" size="icon-sm" aria-label={`Revoke ${gatewayKey.name}`}>
            <Trash2 />
          </Button>
        }
        title={`Revoke ${gatewayKey.name}?`}
        consequence="A tool that uses this key is refused from now on. This cannot be undone."
        action="Revoke key"
        run={() => revokeGatewayKey({ path: { key_id: gatewayKey.id } })}
        onDone={() => queryClient.invalidateQueries({ queryKey: keysKey })}
        done={`${gatewayKey.name} was revoked`}
      />
    </span>
  )
}

const keySchema = z.object({
  name: z
    .string()
    .regex(/^[a-z0-9][a-z0-9-]{0,62}$/, "Lower-case letters, digits and dashes, 63 at most."),
  everyModel: z.boolean(),
  models: z.array(z.string()),
})

// CreateKey makes a key in a dialog and then shows its value, once, in the same dialog, as Coder's does.
function CreateKey() {
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [made, setMade] = useState<CreatedGatewayKey | null>(null)

  const models = useQuery({
    queryKey: ["gateway-models"],
    queryFn: async () => (await listGatewayModels()).data.data,
    enabled: open,
  })

  const create = useMutation({
    mutationFn: async (input: { name: string; models: string[] }) =>
      (await createGatewayKey({ body: { name: input.name, models: input.models } })).data,
    onSuccess: async (created) => {
      setMade(created)
      await queryClient.invalidateQueries({ queryKey: keysKey })
    },
  })

  const form = useForm({
    defaultValues: { name: "", everyModel: true, models: [] as string[] },
    validators: {
      onSubmit: keySchema.refine((value) => value.everyModel || value.models.length > 0, {
        message: "Choose a model, or every model.",
        path: ["models"],
      }),
    },
    onSubmit: ({ value }) =>
      create.mutate({ name: value.name, models: value.everyModel ? [] : value.models }),
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      setMade(null)
      form.reset()
      create.reset()
    }
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger
        render={
          <Button>
            <Plus data-icon="inline-start" />
            Create key
          </Button>
        }
      />
      <DialogContent>
        {made ? (
          <div className="flex flex-col gap-4">
            <DialogHeader>
              <DialogTitle>Copy the key of {made.key.name}</DialogTitle>
              <DialogDescription>
                Copy this key now and give it to the tool as its API key. It cannot be shown again.
              </DialogDescription>
            </DialogHeader>
            <InputGroup>
              <InputGroupInput readOnly aria-label="The new key" value={made.secret} />
              <InputGroupAddon align="inline-end">
                <CopyButton value={made.secret} label="Copy the key" done="The key was copied" />
              </InputGroupAddon>
            </InputGroup>
            <DialogFooter>
              <DialogClose render={<Button />}>Done</DialogClose>
            </DialogFooter>
          </div>
        ) : (
          <form
            autoComplete="off"
            noValidate
            className="flex flex-col gap-4"
            onSubmit={(event) => {
              event.preventDefault()
              void form.handleSubmit()
            }}
          >
            <DialogHeader>
              <DialogTitle>New gateway key</DialogTitle>
              <DialogDescription>
                For one tool on one machine, so that it can be revoked alone.
              </DialogDescription>
            </DialogHeader>
            <FieldGroup>
              <form.Field name="name">
                {(field) => (
                  <TextField
                    field={field}
                    label="Name"
                    placeholder="opencode-laptop"
                    description="Lower-case letters, digits and dashes."
                  />
                )}
              </form.Field>
              <FieldSet>
                <FieldLegend variant="label">Models it may use</FieldLegend>
                <form.Field name="everyModel">
                  {(field) => (
                    <Field orientation="horizontal">
                      <Checkbox
                        id="every-model"
                        checked={field.state.value}
                        onCheckedChange={(checked) => field.handleChange(checked)}
                      />
                      <FieldLabel htmlFor="every-model">Every model</FieldLabel>
                    </Field>
                  )}
                </form.Field>
                <form.Subscribe selector={(state) => state.values.everyModel}>
                  {(everyModel) =>
                    !everyModel && (
                      <form.Field name="models">
                        {(field) => (
                          <>
                            {models.isPending && <Spinner />}
                            {models.data?.map((model) => {
                              const id = `model-${model.name}`

                              return (
                                <Field key={model.name} orientation="horizontal">
                                  <Checkbox
                                    id={id}
                                    checked={field.state.value.includes(model.name)}
                                    onCheckedChange={(checked) =>
                                      field.handleChange(
                                        checked
                                          ? [...field.state.value, model.name]
                                          : field.state.value.filter((name) => name !== model.name),
                                      )
                                    }
                                  />
                                  <FieldLabel htmlFor={id}>{model.name}</FieldLabel>
                                </Field>
                              )
                            })}
                            {!field.state.meta.isValid && (
                              <p role="alert" className="text-sm text-destructive">
                                {field.state.meta.errors.map((error) => error?.message).join(" ")}
                              </p>
                            )}
                          </>
                        )}
                      </form.Field>
                    )
                  }
                </form.Subscribe>
              </FieldSet>
            </FieldGroup>
            <ErrorAlert error={create.error} />
            <DialogFooter>
              <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
              <Button type="submit" disabled={create.isPending}>
                {create.isPending && <Spinner data-icon="inline-start" />}
                Create key
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
