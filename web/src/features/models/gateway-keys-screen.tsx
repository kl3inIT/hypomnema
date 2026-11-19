import { useForm } from "@tanstack/react-form"
import { useMutation } from "@tanstack/react-query"
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
  AudioLines,
  Eye,
  EyeOff,
  FileText,
  Globe,
  ImageIcon,
  KeyRound,
  Pencil,
  Plus,
  Search,
  Trash2,
  type LucideIcon,
} from "lucide-react"
import { useId, useState } from "react"
import { z } from "zod"

import "@/api/client"
import {
  createGatewayKeyMutation,
  updateGatewayKeyMutation,
} from "@/api/generated/@tanstack/react-query.gen"
import { getGatewayKeyValue, revokeGatewayKey } from "@/api/generated/sdk.gen"
import type { CreatedGatewayKey, GatewayEndpoint, GatewayKey } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
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
import { ModelMark, ProviderMark } from "@/components/icons/model-mark"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { ErrorAlert } from "@/components/error-alert"
import { TextField } from "@/components/text-field"
import { SettingsPage } from "@/components/settings-page"
import { said } from "@/lib/said"
import { modelName } from "@/lib/model-name"
import { since } from "@/lib/time"

import { pickGroups } from "./gateway-keys-state"
import { GatewayModelPicker } from "./gateway-model-picker"
import { useGatewayKeys, useGatewayModels, useProviders, useRefreshGatewayKeys } from "./use-models"

// The name the gateway gives to "every model".
const every = "*"

// What a key may use besides chat (issue #664), in the order shown, as OmniRoute's key Access page lists its
// endpoint categories with a checkbox each. Search, images and audio carry the icons of their pages in the Settings
// menu (settings-page.tsx).
const endpointKinds: {
  id: GatewayEndpoint
  title: string
  description: string
  icon: LucideIcon
}[] = [
  {
    id: "search",
    title: "Web search",
    description: "/v1/search, and the Codex CLI's own search.",
    icon: Globe,
  },
  { id: "fetch", title: "Reading a web page", description: "/v1/web/fetch.", icon: FileText },
  {
    id: "images",
    title: "Making and editing images",
    description: "/v1/images/generations and /edits.",
    icon: ImageIcon,
  },
  {
    id: "audio",
    title: "Speech to text and text to speech",
    description: "/v1/audio/transcriptions, /speech and /voices.",
    icon: AudioLines,
  },
]

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
    cell: ({ row }) => <KeyValue gatewayKey={row.original} />,
  },
  {
    id: "models",
    enableSorting: false,
    header: "Models",
    cell: ({ row }) => <KeyModels models={row.original.models} />,
  },
  {
    id: "endpoints",
    enableSorting: false,
    header: "Also",
    cell: ({ row }) => <KeyEndpoints endpoints={row.original.endpoints} />,
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
// sort), the page the owner named for a list (2026-11-12). Keys are masked until shown, as on 9router's Endpoint &
// Key page (EndpointPageClient.js), and a tool calls the page's own origin with /v1. A model is
// shown with its maker's mark and its name to read. The keys made for one worker or one Bot's chat are counted,
// not listed: they end with what they were made for.
export function GatewayKeysScreen() {
  const keys = useGatewayKeys()

  return (
    <SettingsPage
      wide
      title="Gateway keys"
      description="Keys for tools outside, such as opencode or omp, to use your models through the gateway."
      actions={<KeyDialog />}
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

function KeyValue({ gatewayKey }: { gatewayKey: GatewayKey }) {
  const [shown, setShown] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)

  if (!gatewayKey.masked_value) {
    return (
      <div className="grid max-w-56 min-w-40 gap-1 text-xs whitespace-normal text-muted-foreground">
        <code>{gatewayKey.prefix ? `${gatewayKey.prefix}…` : "—"}</code>
        <span>Made before keys could be shown again; make a new one to copy it.</span>
        <KeyDialog gatewayKey={gatewayKey} />
      </div>
    )
  }

  async function read(action: "show" | "copy") {
    setPending(true)
    setError(null)
    try {
      const value =
        shown ??
        (await getGatewayKeyValue({ path: { key_id: gatewayKey.id }, throwOnError: true })).data
          .secret

      if (action === "show") {
        setShown(value)
      } else {
        await navigator.clipboard.writeText(value)
        setCopied(true)
        said("The key was copied")
      }
    } catch (error) {
      setError(error)
    } finally {
      setPending(false)
    }
  }

  const visibility = shown ? "Hide" : "Show"

  return (
    <div className="grid w-40 min-w-0 gap-1 whitespace-normal lg:w-56">
      <code className="text-xs break-all text-muted-foreground">
        {shown ?? gatewayKey.masked_value}
      </code>
      <div className="flex gap-1">
        <Button
          variant="ghost"
          size="icon-sm"
          disabled={pending}
          aria-label={`${visibility} key for ${gatewayKey.name}`}
          title={`${visibility} key`}
          onClick={() => (shown ? setShown(null) : void read("show"))}
        >
          {shown ? <EyeOff /> : <Eye />}
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          disabled={pending}
          aria-label={`Copy key for ${gatewayKey.name}`}
          title="Copy key"
          onClick={() => void read("copy")}
        >
          {copied ? <Check /> : <Copy />}
        </Button>
        <KeyDialog gatewayKey={gatewayKey} />
      </div>
      <ErrorAlert error={error} />
    </div>
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

// KeyEndpoints shows what a key may use besides chat: the words of each kind, or that it may use none.
function KeyEndpoints({ endpoints }: { endpoints: GatewayEndpoint[] }) {
  if (endpoints.length === 0) {
    return <span className="text-muted-foreground">Chat only</span>
  }

  return (
    <span className="flex flex-wrap gap-1.5">
      {endpointKinds
        .filter((kind) => endpoints.includes(kind.id))
        .map((kind) => (
          <span
            key={kind.id}
            className="inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs"
          >
            <kind.icon className="size-3.5 shrink-0 text-muted-foreground" />
            {kind.title}
          </span>
        ))}
    </span>
  )
}

function RevokeKey({ gatewayKey }: { gatewayKey: GatewayKey }) {
  const refresh = useRefreshGatewayKeys()

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
        onDone={refresh}
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
    endpoints: z.array(z.enum(["search", "fetch", "images", "audio"])),
  })
  .refine((value) => value.scope === "every" || value.models.length > 0, {
    message: "Choose a model, or every model.",
    path: ["models"],
  })

function KeyDialog({ gatewayKey }: { gatewayKey?: GatewayKey }) {
  const refresh = useRefreshGatewayKeys()
  const [open, setOpen] = useState(false)
  const [made, setMade] = useState<CreatedGatewayKey | null>(null)
  const providers = useProviders()
  const formId = useId()

  const models = useGatewayModels({ enabled: open })
  const groups = pickGroups(models.data ?? [], providers.data ?? [])

  const create = useMutation({
    ...createGatewayKeyMutation(),
    gcTime: 0,
    onSuccess: async (created) => {
      setMade(created)
      await refresh()
    },
  })
  const update = useMutation({
    ...updateGatewayKeyMutation(),
    onSuccess: async (key) => {
      await refresh()
      said(`${key.name} was updated`)
      change(false)
    },
  })
  const selectedModels = gatewayKey?.models.filter((model) => model !== every) ?? []
  const action = gatewayKey ? "Save changes" : "Create key"
  const pending = create.isPending || update.isPending

  const form = useForm({
    defaultValues: {
      name: gatewayKey?.name ?? "",
      scope: (gatewayKey && !gatewayKey.models.includes(every) ? "some" : "every") as
        | "every"
        | "some",
      models: selectedModels,
      endpoints: gatewayKey?.endpoints ?? endpointKinds.map((kind) => kind.id),
    },
    validators: { onSubmit: keySchema },
    onSubmit: ({ value }) => {
      const access = { name: value.name, models: value.scope === "every" ? [] : value.models }
      if (gatewayKey) {
        const sameEndpoints =
          gatewayKey.endpoints.length === value.endpoints.length &&
          gatewayKey.endpoints.every((kind) => value.endpoints.includes(kind))
        update.mutate({
          path: { key_id: gatewayKey.id },
          body: { ...access, ...(!sameEndpoints && { endpoints: value.endpoints }) },
        })
        return
      }
      create.mutate({
        body: {
          ...access,
          // Every kind is sent as none named, so that a kind added later is given too.
          ...(value.endpoints.length < endpointKinds.length && { endpoints: value.endpoints }),
        },
      })
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      setMade(null)
      form.reset()
      create.reset()
      update.reset()
    }
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger
        render={
          gatewayKey ? (
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`Edit ${gatewayKey.name}`}
              title="Edit key"
            >
              <Pencil />
            </Button>
          ) : (
            <Button>
              <Plus data-icon="inline-start" />
              Create key
            </Button>
          )
        }
      />
      <DialogContent>
        {made ? (
          <div className="flex flex-col gap-4">
            <DialogHeader>
              <DialogTitle>Copy the key of {made.key.name}</DialogTitle>
              <DialogDescription>
                Give this key to the tool as its API key. You can copy it again from the list.
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
              <DialogTitle>
                {gatewayKey ? `Edit ${gatewayKey.name}` : "New gateway key"}
              </DialogTitle>
              <DialogDescription>
                {gatewayKey
                  ? "Change what the key can do without replacing the value used by your tool."
                  : "For one tool on one machine, so that it can be revoked alone."}
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
                  <FieldSet>
                    <FieldLegend variant="label">Besides chat, it may use</FieldLegend>
                    <form.Field name="endpoints">
                      {(field) => (
                        <FieldGroup data-slot="checkbox-group">
                          {endpointKinds.map((kind) => (
                            <FieldLabel key={kind.id} htmlFor={`${formId}-${kind.id}`}>
                              <Field orientation="horizontal">
                                <Checkbox
                                  id={`${formId}-${kind.id}`}
                                  checked={field.state.value.includes(kind.id)}
                                  onCheckedChange={(checked) =>
                                    field.handleChange(
                                      endpointKinds
                                        .map((k) => k.id)
                                        .filter((id) =>
                                          id === kind.id ? checked : field.state.value.includes(id),
                                        ),
                                    )
                                  }
                                />
                                <FieldContent>
                                  <FieldTitle>
                                    <kind.icon className="size-4 shrink-0 text-muted-foreground" />
                                    {kind.title}
                                  </FieldTitle>
                                  <FieldDescription>{kind.description}</FieldDescription>
                                </FieldContent>
                              </Field>
                            </FieldLabel>
                          ))}
                        </FieldGroup>
                      )}
                    </form.Field>
                  </FieldSet>
                </FieldGroup>
              </form>
            </DialogPanel>
            <ErrorAlert error={create.error ?? update.error} />
            <DialogFooter>
              <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
              <Button
                type="submit"
                form={formId}
                focusableWhenDisabled={pending}
                disabled={pending}
              >
                {pending && <Spinner data-icon="inline-start" />}
                {action}
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}
