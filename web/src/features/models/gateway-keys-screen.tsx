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
  Cable,
  Check,
  Copy,
  Eye,
  EyeOff,
  KeyRound,
  Search,
  Trash2,
} from "lucide-react"
import { Link } from "@tanstack/react-router"
import { useState, type ReactNode } from "react"

import "@/api/client"
import { getGatewayKeyValue, revokeGatewayKey } from "@/api/generated/sdk.gen"
import type { GatewayEndpoint, GatewayKey } from "@/api/generated/types.gen"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { Popover, PopoverContent, PopoverTitle, PopoverTrigger } from "@/components/ui/popover"
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
import { SettingsPage } from "@/components/settings-page"
import { said } from "@/lib/said"
import { modelName } from "@/lib/model-name"
import { since } from "@/lib/time"

import { CopyButton, KeySheet } from "./gateway-key-sheet"
import { endpointKinds, routingBadges } from "./gateway-keys-state"
import { useGatewayKeys, useRefreshGatewayKeys } from "./use-models"

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
    cell: ({ row }) => (
      <div className="flex flex-col items-start gap-1">
        <span className="block max-w-28 truncate font-medium sm:max-w-40" title={row.original.name}>
          {row.original.name}
        </span>
        <RoutingBadges gatewayKey={row.original} />
      </div>
    ),
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
    cell: ({ row }) => <KeyActions gatewayKey={row.original} />,
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
      actions={
        <>
          <Link
            to="/settings/connect"
            search={{}}
            className={buttonVariants({ variant: "outline" })}
          >
            <Cable data-icon="inline-start" />
            Connect a tool
          </Link>
          <KeySheet />
        </>
      }
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

function KeyValue({ gatewayKey }: { gatewayKey: GatewayKey }) {
  const [shown, setShown] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)

  if (!gatewayKey.masked_value) {
    return (
      <div className="grid gap-1 text-xs text-muted-foreground">
        <code>{gatewayKey.prefix ? `${gatewayKey.prefix}…` : "—"}</code>
        <div className="flex items-center gap-1">
          <Popover>
            <PopoverTrigger type="button">
              <span className="underline decoration-dotted underline-offset-2">
                Can&apos;t be shown
              </span>
            </PopoverTrigger>
            <PopoverContent align="start" aria-label="Key value unavailable">
              <p className="whitespace-normal">
                Made before keys could be shown again; make a new one to copy it.
              </p>
            </PopoverContent>
          </Popover>
          <KeySheet gatewayKey={gatewayKey} />
        </div>
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
        <KeySheet gatewayKey={gatewayKey} />
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
      <div className="min-w-0 overflow-hidden rounded-xl border bg-card [&_[data-key-name]]:sticky [&_[data-key-name]]:start-0 [&_[data-key-name]]:z-10 [&_[data-key-name]]:bg-card [&_[data-key-name]]:shadow-[1px_0_var(--border)]">
        <Table aria-label="Gateway keys">
          <TableHeader>
            {table.getHeaderGroups().map((group) => (
              <TableRow key={group.id}>
                {group.headers.map((header) => {
                  const sorted = header.column.getIsSorted()

                  return (
                    <TableHead
                      key={header.id}
                      data-key-name={header.column.id === "name" || undefined}
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
                  <TableCell key={cell.id} data-key-name={cell.column.id === "name" || undefined}>
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

// RoutingBadges mark a key with its own routing (issue #1277) under its name: the accounts it is limited to and its
// own search order.
function RoutingBadges({ gatewayKey }: { gatewayKey: GatewayKey }) {
  const badges = routingBadges(gatewayKey)
  if (badges.length === 0) return null

  return (
    <span className="flex flex-wrap gap-1" aria-label="Own routing">
      {badges.map((badge) => (
        <Badge key={badge} variant="outline">
          {badge}
        </Badge>
      ))}
    </span>
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
    <KeyChips
      title="Models"
      items={models.map((model) => ({
        id: model,
        label: modelName(model),
        icon: <ModelMark name={model} className="size-3.5 shrink-0" />,
      }))}
    />
  )
}

function KeyEndpoints({ endpoints }: { endpoints: GatewayEndpoint[] }) {
  if (endpoints.length === 0) {
    return <span className="text-muted-foreground">Chat only</span>
  }

  return (
    <KeyChips
      title="Also"
      items={endpointKinds
        .filter((kind) => endpoints.includes(kind.id))
        .map((kind) => ({
          id: kind.id,
          label: kind.title,
          shortLabel: kind.shortLabel,
          icon: <kind.icon className="size-3.5 shrink-0 text-muted-foreground" />,
        }))}
    />
  )
}

function KeyChips({
  title,
  items,
}: {
  title: string
  items: { id: string; label: string; shortLabel?: string; icon: ReactNode }[]
}) {
  const visible = items.slice(0, 3)
  const extra = items.length - visible.length

  return (
    <div className="flex w-max items-center gap-1.5">
      {visible.map(({ id, label, shortLabel, icon }) => (
        <span
          key={id}
          title={shortLabel ? label : id}
          className="inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs"
        >
          {icon}
          <span className="max-w-36 truncate">{shortLabel ?? label}</span>
        </span>
      ))}
      {extra > 0 && (
        <Popover>
          <PopoverTrigger
            render={
              <Button
                variant="outline"
                size="chip"
                aria-description={`Show all ${title.toLowerCase()}`}
              />
            }
          >
            +{extra}
          </PopoverTrigger>
          <PopoverContent align="end">
            <PopoverTitle>{title}</PopoverTitle>
            <ul className="flex max-h-[calc(var(--available-height)-3rem)] flex-col gap-2 overflow-y-auto whitespace-normal">
              {items.map(({ id, label, icon }) => (
                <li key={id} className="flex items-center gap-2 [overflow-wrap:anywhere]">
                  {icon}
                  <span className="min-w-0">{label}</span>
                </li>
              ))}
            </ul>
          </PopoverContent>
        </Popover>
      )}
    </div>
  )
}

// KeyActions are a row's actions: Connect opens Connect a tool with the key chosen (issue #907), as 9router's
// CLI tools cards pick a key (ApiKeySelect.js); and revoking it.
function KeyActions({ gatewayKey }: { gatewayKey: GatewayKey }) {
  const refresh = useRefreshGatewayKeys()

  return (
    <span className="flex justify-end gap-1">
      <Link
        to="/settings/connect"
        search={{ key: gatewayKey.id }}
        className={buttonVariants({ variant: "ghost", size: "icon-sm" })}
        aria-label={`Connect a tool with ${gatewayKey.name}`}
        title="Connect a tool"
      >
        <Cable />
      </Link>
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
