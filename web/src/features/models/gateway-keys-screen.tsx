import { useForm } from "@tanstack/react-form"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  columnFilteringFeature,
  createFilteredRowModel,
  createSortedRowModel,
  filterFn_includesString,
  rowSortingFeature,
  sortFn_alphanumeric,
  sortFn_datetime,
  tableFeatures,
  useTable,
  type Column,
  type ColumnDef,
} from "@tanstack/react-table"
import {
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  Check,
  Copy,
  KeyRound,
  Plus,
  Search,
  Trash2,
} from "lucide-react"
import { useId, useState } from "react"
import { z } from "zod"

import "@/api/client"
import {
  createGatewayKey,
  listGatewayKeys,
  listGatewayModels,
  revokeGatewayKey,
} from "@/api/generated/sdk.gen"
import type { CreatedGatewayKey, GatewayKey } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
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
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty"
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
  FieldTitle,
} from "@/components/ui/field"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from "@/components/ui/input-group"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { ModelMark, ProviderMark } from "@/features/agents/model-mark"
import { ConfirmDialog, ErrorAlert, TextField } from "@/features/settings/settings-parts"
import { SettingsPage } from "@/features/settings/settings-shell"
import { said } from "@/features/settings/use-integrations"
import { modelName } from "@/lib/model-name"
import { since } from "@/lib/time"

import { pickGroups } from "./gateway-keys-state"
import { GatewayModelPicker } from "./gateway-model-picker"
import { useProviders } from "./use-models"

const keysKey = ["gateway-keys"] as const
const modelsKey = ["gateway-models"] as const

// The name the gateway gives to "every model".
const every = "*"

const features = tableFeatures({
  columnFilteringFeature,
  rowSortingFeature,
  filteredRowModel: createFilteredRowModel(),
  sortedRowModel: createSortedRowModel(),
  filterFns: { includesString: filterFn_includesString },
  sortFns: { alphanumeric: sortFn_alphanumeric, datetime: sortFn_datetime },
})

type KeyColumn = Column<typeof features, GatewayKey>

// SortHead is a column's name as a button that sorts by it, as the Vault page has its headers.
function SortHead({ column, label }: { column: KeyColumn; label: string }) {
  const sorted = column.getIsSorted()
  const Icon = sorted === "asc" ? ArrowUp : sorted === "desc" ? ArrowDown : ArrowUpDown

  return (
    <span className="-ms-2.5 inline-flex">
      <Button variant="ghost" size="sm" onClick={column.getToggleSortingHandler()}>
        {label}
        <Icon data-icon="inline-end" />
      </Button>
    </span>
  )
}

const dayFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" })

const columns: ColumnDef<typeof features, GatewayKey>[] = [
  {
    accessorKey: "name",
    filterFn: "includesString",
    header: ({ column }) => <SortHead column={column} label="Name" />,
    cell: ({ row }) => <span className="font-medium">{row.original.name}</span>,
  },
  {
    id: "key",
    enableSorting: false,
    header: "Key",
    cell: ({ row }) => (
      <span className="font-mono text-muted-foreground">
        {row.original.prefix ? `${row.original.prefix}…` : "—"}
      </span>
    ),
  },
  {
    id: "models",
    enableSorting: false,
    header: "Models",
    cell: ({ row }) => <KeyModels models={row.original.models} />,
  },
  {
    id: "last_used",
    accessorFn: (key) => key.last_used_at ?? "",
    sortFn: "alphanumeric",
    header: ({ column }) => <SortHead column={column} label="Last used" />,
    cell: ({ row }) => (
      <span className="text-muted-foreground">
        {row.original.last_used_at ? since(row.original.last_used_at) : "Never"}
      </span>
    ),
  },
  {
    accessorKey: "created_at",
    sortFn: "alphanumeric",
    header: ({ column }) => <SortHead column={column} label="Created" />,
    cell: ({ row }) => (
      <time dateTime={row.original.created_at} className="text-muted-foreground">
        {dayFormat.format(new Date(row.original.created_at))}
      </time>
    ),
  },
  {
    id: "actions",
    enableSorting: false,
    header: () => <span className="sr-only">Actions</span>,
    cell: ({ row }) => <RevokeKey gatewayKey={row.original} />,
  },
]

const sortable = new Set(["name", "last_used", "created_at"])

// GatewayKeysScreen is where the owner gives a tool outside, such as opencode or omp, a key to the model gateway
// (issue #199, step 6). The keys are the Vault page's data table (vault-screen.tsx: a search by name, columns that
// sort), the page the owner named for a list (2026-11-12). A key's value is shown once and only its start is kept,
// as Coder's AI Gateway keys (coder/site/src/pages/AISettingsPage/GatewayKeysPage/); the address a tool is given
// is the page's own origin with /v1, as 9router's Endpoint & Key page gives it (EndpointPageClient.js). A model is
// shown with its maker's mark and its name to read. The keys made for one worker or one Bot's chat are counted,
// not listed: they end with what they were made for.
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
        the Codex CLI. Anthropic&apos;s /v1/messages for Claude Code, whose base URL is this address
        without /v1.
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

function KeysTable({ keys }: { keys: GatewayKey[] }) {
  const table = useTable({
    features,
    columns,
    data: keys,
    getRowId: (key) => key.id,
    initialState: { sorting: [{ id: "name", desc: false }] },
  })
  const name = table.getColumn("name")
  const query = (name?.getFilterValue() as string | undefined) ?? ""
  const rows = table.getRowModel().rows

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <InputGroup>
        <InputGroupAddon>
          <Search />
        </InputGroupAddon>
        <InputGroupInput
          type="search"
          autoComplete="off"
          aria-label="Search keys by name"
          placeholder="Search keys by name…"
          value={query}
          onChange={(event) => name?.setFilterValue(event.target.value.trim().toLowerCase())}
        />
      </InputGroup>
      <div className="min-w-0 overflow-hidden rounded-xl border bg-card">
        <Table aria-label="Gateway keys">
          <TableHeader>
            {table.getHeaderGroups().map((group) => (
              <TableRow key={group.id}>
                {group.headers.map((header) => {
                  const sorted = header.column.getIsSorted()

                  return (
                    <TableHead
                      key={header.id}
                      aria-sort={
                        sortable.has(header.column.id)
                          ? sorted === "asc"
                            ? "ascending"
                            : sorted === "desc"
                              ? "descending"
                              : "none"
                          : undefined
                      }
                    >
                      <table.FlexRender header={header} />
                    </TableHead>
                  )
                })}
              </TableRow>
            ))}
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <TableRow key={row.id} aria-label={row.original.name}>
                {row.getAllCells().map((cell) => (
                  <TableCell key={cell.id}>
                    <table.FlexRender cell={cell} />
                  </TableCell>
                ))}
              </TableRow>
            ))}
            {rows.length === 0 && (
              <TableRow>
                <TableCell colSpan={columns.length}>
                  <p className="py-6 text-center text-muted-foreground">
                    No key matches “{query}”.
                  </p>
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}

// KeyModels shows the models a key may use, each with its maker's mark and its name to read; every model is the
// marks of the subscriptions behind the words.
function KeyModels({ models }: { models: string[] }) {
  if (models.includes(every)) {
    return (
      <span className="inline-flex items-center gap-2 text-muted-foreground">
        <span className="flex -space-x-1">
          {["claude_subscription", "codex_subscription", "antigravity_subscription"].map((kind) => (
            <span
              key={kind}
              className="grid size-5 place-items-center rounded-full border bg-muted"
            >
              <ProviderMark kind={kind} className="size-3" />
            </span>
          ))}
        </span>
        Every model
      </span>
    )
  }

  return (
    <span className="flex flex-wrap gap-1.5">
      {models.map((model) => (
        <span
          key={model}
          title={model}
          className="inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs"
        >
          <ModelMark name={model} className="size-3.5 shrink-0" />
          {modelName(model)}
        </span>
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

const keySchema = z
  .object({
    name: z
      .string()
      .regex(/^[a-z0-9][a-z0-9-]{0,62}$/, "Lower-case letters, digits and dashes, 63 at most."),
    scope: z.enum(["every", "some"]),
    models: z.array(z.string()),
  })
  .refine((value) => value.scope === "every" || value.models.length > 0, {
    message: "Choose a model, or every model.",
    path: ["models"],
  })

// CreateKey makes a key in a dialog and then shows its value, once, in the same dialog, as Coder's does.
function CreateKey() {
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [made, setMade] = useState<CreatedGatewayKey | null>(null)
  const providers = useProviders()
  const formId = useId()

  const models = useQuery({
    queryKey: modelsKey,
    queryFn: async () => (await listGatewayModels()).data.data,
    enabled: open,
  })
  const groups = pickGroups(models.data ?? [], providers.data ?? [])

  const create = useMutation({
    mutationFn: async (input: { name: string; models: string[] }) =>
      (await createGatewayKey({ body: { name: input.name, models: input.models } })).data,
    onSuccess: async (created) => {
      setMade(created)
      await queryClient.invalidateQueries({ queryKey: keysKey })
    },
  })

  const form = useForm({
    defaultValues: { name: "", scope: "every" as "every" | "some", models: [] as string[] },
    validators: { onSubmit: keySchema },
    onSubmit: ({ value }) =>
      create.mutate({ name: value.name, models: value.scope === "every" ? [] : value.models }),
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
          <>
            <DialogHeader>
              <DialogTitle>New gateway key</DialogTitle>
              <DialogDescription>
                For one tool on one machine, so that it can be revoked alone.
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
                    <form.Field name="scope">
                      {(field) => (
                        <RadioGroup
                          value={field.state.value}
                          onValueChange={(next) => field.handleChange(next as "every" | "some")}
                        >
                          <FieldLabel htmlFor="scope-every">
                            <Field orientation="horizontal">
                              <RadioGroupItem id="scope-every" value="every" />
                              <FieldContent>
                                <FieldTitle>Every model</FieldTitle>
                                <FieldDescription>Also the models added later.</FieldDescription>
                              </FieldContent>
                            </Field>
                          </FieldLabel>
                          <FieldLabel htmlFor="scope-some">
                            <Field orientation="horizontal">
                              <RadioGroupItem id="scope-some" value="some" />
                              <FieldContent>
                                <FieldTitle>Only these models</FieldTitle>
                                <FieldDescription>
                                  A model added later is not allowed until you make a new key.
                                </FieldDescription>
                              </FieldContent>
                            </Field>
                          </FieldLabel>
                        </RadioGroup>
                      )}
                    </form.Field>
                    <form.Subscribe selector={(state) => state.values.scope}>
                      {(scope) =>
                        scope === "some" && (
                          <form.Field name="models">
                            {(field) => (
                              <Field>
                                {models.isPending ? (
                                  <Spinner />
                                ) : (
                                  <GatewayModelPicker
                                    groups={groups}
                                    chosen={field.state.value}
                                    onChange={field.handleChange}
                                  />
                                )}
                                {!field.state.meta.isValid && (
                                  <FieldError errors={field.state.meta.errors} />
                                )}
                              </Field>
                            )}
                          </form.Field>
                        )
                      }
                    </form.Subscribe>
                  </FieldSet>
                </FieldGroup>
              </form>
            </DialogPanel>
            <ErrorAlert error={create.error} />
            <DialogFooter>
              <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
              <Button type="submit" form={formId} disabled={create.isPending}>
                {create.isPending && <Spinner data-icon="inline-start" />}
                Create key
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}
