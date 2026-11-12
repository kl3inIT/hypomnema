import { useForm } from "@tanstack/react-form"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  columnFilteringFeature,
  createFilteredRowModel,
  createSortedRowModel,
  filterFn_includesString,
  rowSortingFeature,
  sortFn_alphanumeric,
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
  Eye,
  EyeOff,
  KeyRound,
  Pencil,
  Plus,
  Search,
  Trash2,
} from "lucide-react"
import { createContext, use, useId, useMemo, useState, type ReactElement } from "react"
import { z } from "zod"

import "@/api/client"
import { deleteSecret, getSecretValue, listSecrets, putSecret } from "@/api/generated/sdk.gen"
import type { Secret } from "@/api/generated/types.gen"
import { Badge } from "@/components/ui/badge"
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
import { FieldGroup } from "@/components/ui/field"
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"

import { cn } from "@/lib/utils"

import { ConfirmDialog, ErrorAlert, TextField } from "./settings-parts"
import { SettingsPage } from "./settings-shell"
import { errorText, said } from "./use-integrations"

const secretsKey = ["secrets"] as const

// A hidden value: a fixed run of asterisks, so its length says nothing (Infisical's HIDDEN_SECRET_VALUE).
const hidden = "************"

function typeName(secret: Secret): string {
  return secret.kind === "oauth_token_set" ? "OAuth token set" : "Generic"
}

// Reveal is which values are shown: every row's, from the table's head, or a row's own.
type Reveal = {
  all: boolean
  toggleAll: () => void
  shown: (name: string) => boolean
  toggle: (name: string) => void
}

const RevealContext = createContext<Reveal>({
  all: false,
  toggleAll: () => {},
  shown: () => false,
  toggle: () => {},
})

const features = tableFeatures({
  columnFilteringFeature,
  rowSortingFeature,
  filteredRowModel: createFilteredRowModel(),
  sortedRowModel: createSortedRowModel(),
  filterFns: { includesString: filterFn_includesString },
  sortFns: { alphanumeric: sortFn_alphanumeric },
})

type SecretColumn = Column<typeof features, Secret>

// SortHead is a column's name as a button that sorts by it, as shadcn's data table has its headers.
function SortHead({ column, label }: { column: SecretColumn; label: string }) {
  const sorted = column.getIsSorted()
  const Icon = sorted === "asc" ? ArrowUp : sorted === "desc" ? ArrowDown : ArrowUpDown

  return (
    // The button's padding is pulled back so its label lines up with the cells under it.
    <span className="-ms-2.5 inline-flex">
      <Button variant="ghost" size="sm" onClick={column.getToggleSortingHandler()}>
        {label}
        <Icon data-icon="inline-end" />
      </Button>
    </span>
  )
}

const columns: ColumnDef<typeof features, Secret>[] = [
  {
    accessorKey: "name",
    filterFn: "includesString",
    header: ({ column }) => <SortHead column={column} label="Name" />,
    cell: ({ row }) => (
      <span className="block max-w-56 truncate font-medium" title={row.original.name}>
        {row.original.name}
      </span>
    ),
  },
  {
    id: "value",
    enableSorting: false,
    header: () => <ValueHead />,
    cell: ({ row }) => <ValueCell secret={row.original} />,
  },
  {
    id: "type",
    enableSorting: false,
    header: "Type",
    cell: ({ row }) => <TypeCell secret={row.original} />,
  },
  {
    accessorKey: "version",
    enableSorting: false,
    header: "Version",
    cell: ({ row }) => <span className="tabular-nums">{row.original.version}</span>,
  },
  {
    accessorKey: "updated_at",
    header: ({ column }) => <SortHead column={column} label="Updated" />,
    cell: ({ row }) => <UpdatedCell secret={row.original} />,
  },
  {
    id: "actions",
    enableSorting: false,
    header: () => <span className="sr-only">Actions</span>,
    cell: ({ row }) => <RowActions secret={row.original} />,
  },
]

const sortable = new Set(["name", "updated_at"])

// VaultScreen is where the owner sees what the vault keeps and reads a value (issue #108), as a data table on
// the standard components (issue #123).
//
// From Infisical's secrets dashboard (MIT, Copyright (c) 2022 Infisical Inc.,
// https://github.com/Infisical/infisical; frontend/src/pages/secret-manager/OverviewPage/components/SecretTableRow/,
// docs/images/platform/project/secrets-dashboard-2.png): a table with a Name and a Value column, the value in its
// row, hidden as asterisks; "Reveal Values" in the table's head for every row and an eye a row; a value fetched
// only once it is revealed (`useGetSecretValue(..., { enabled: isVisible })`); a search box and the add button
// above the table. Infisical builds its tables on TanStack Table too.
//
// From Omnara's secrets list (Apache-2.0, https://github.com/omnara-ai/omnara;
// frontend/apps/web/src/components/overview/SecretsSection.tsx, components/org/EditSecretDialog.tsx): the Type
// column, a secret's version and dates, sorting by a column, and an edit dialog where "a new value replaces" a
// generic secret and a token set takes "the complete replacement set". Omnara shows the version and dates in a row
// that opens; here they are columns (owner, 2026-11-10).
//
// Left out: Infisical's environments, folders, tags, comments and batch editing; Omnara's paging and grants.
export function VaultScreen() {
  const [revealAll, setRevealAll] = useState(false)
  const [revealed, setRevealed] = useState<ReadonlySet<string>>(new Set())
  const secrets = useQuery({
    queryKey: secretsKey,
    queryFn: async () => (await listSecrets()).data.data,
  })

  const reveal = useMemo<Reveal>(
    () => ({
      all: revealAll,
      toggleAll: () => setRevealAll((all) => !all),
      shown: (name) => revealAll || revealed.has(name),
      toggle: (name) =>
        setRevealed((before) => {
          const after = new Set(before)
          if (!after.delete(name)) after.add(name)

          return after
        }),
    }),
    [revealAll, revealed],
  )

  return (
    <SettingsPage
      wide
      title="Vault"
      description="The secrets Hypomnema keeps. Only you can read a value here; agents never can."
      actions={<AddSecret />}
    >
      {secrets.isPending && (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner />
          Loading…
        </p>
      )}
      <ErrorAlert error={secrets.error} />
      {secrets.data && secrets.data.length === 0 && (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <KeyRound />
            </EmptyMedia>
            <EmptyTitle>No secrets yet</EmptyTitle>
            <EmptyDescription>
              Add the API keys and credentials your providers and integrations use.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}
      {secrets.data && secrets.data.length > 0 && (
        <RevealContext value={reveal}>
          <SecretsTable secrets={secrets.data} />
        </RevealContext>
      )}
    </SettingsPage>
  )
}

function SecretsTable({ secrets }: { secrets: Secret[] }) {
  const table = useTable({
    features,
    columns,
    data: secrets,
    getRowId: (secret) => secret.name,
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
          aria-label="Search secrets by name"
          placeholder="Search secrets by name…"
          value={query}
          onChange={(event) => name?.setFilterValue(event.target.value.trim().toLowerCase())}
        />
      </InputGroup>
      <div className="min-w-0 overflow-hidden rounded-xl border bg-card">
        <Table>
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
                    No secret matches “{query}”.
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

function ValueHead() {
  const reveal = use(RevealContext)

  return (
    <span className="flex items-center justify-between gap-2">
      Value
      <Button variant="ghost" size="xs" aria-pressed={reveal.all} onClick={reveal.toggleAll}>
        {reveal.all ? <EyeOff data-icon="inline-start" /> : <Eye data-icon="inline-start" />}
        {reveal.all ? "Hide values" : "Reveal values"}
      </Button>
    </span>
  )
}

function ValueCell({ secret }: { secret: Secret }) {
  const shown = use(RevealContext).shown(secret.name)

  // The value is asked for only while it is shown, and is not kept once hidden.
  const value = useQuery({
    queryKey: ["secret-value", secret.name, secret.version],
    queryFn: async () => (await getSecretValue({ path: { name: secret.name } })).data.values,
    enabled: shown,
    gcTime: 0,
    staleTime: 0,
  })
  const entries = shown && value.data ? Object.entries(value.data) : []

  if (!shown || value.isPending) {
    return (
      <span className="font-mono text-muted-foreground select-none" aria-hidden={!shown}>
        {hidden}
      </span>
    )
  }

  if (value.error) {
    return (
      <span role="alert" className="text-destructive">
        {errorText(value.error)}
      </span>
    )
  }

  // One line a value, as Infisical keeps a row's value on its line (SecretEditTableRow.tsx: `truncate`): a long
  // one is cut with its whole text in its title, and is copied whole. A token set names each value before it.
  return (
    <span className="grid max-w-md grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-2 gap-y-1">
      {entries.map(([key, text]) => (
        <SecretValue key={key} label={entries.length > 1 ? key : undefined} value={text} />
      ))}
    </span>
  )
}

// SecretValue is one revealed value on its line: its key when the secret has several, the value, and a button
// that copies it.
function SecretValue({ label, value }: { label?: string; value: string }) {
  const [copied, setCopied] = useState(false)

  async function copy() {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      said(label ? `${label} copied` : "Value copied")
    } catch {
      // No clipboard (an insecure page): the value stays selectable.
    }
  }

  return (
    <>
      {label && <span className="text-xs text-muted-foreground">{label}</span>}
      <output
        aria-label={label ?? "value"}
        title={value}
        className={cn("truncate font-mono", !label && "col-span-2")}
      >
        {value}
      </output>
      <Button
        variant="ghost"
        size="icon-xs"
        aria-label={label ? `Copy ${label}` : "Copy value"}
        onClick={() => void copy()}
      >
        {copied ? <Check /> : <Copy />}
      </Button>
    </>
  )
}

function TypeCell({ secret }: { secret: Secret }) {
  return (
    <span className="flex flex-col items-start gap-1 text-muted-foreground">
      {typeName(secret)}
      {secret.login_required && (
        <Badge variant="destructive" title="Refused; sign in again where it is used.">
          Needs sign-in
        </Badge>
      )}
      {secret.key_missing && (
        <Badge
          variant="destructive"
          title="Its root key is not in the keyring, so it cannot be read."
        >
          Key missing
        </Badge>
      )}
    </span>
  )
}

const dayFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" })
const momentFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" })

function UpdatedCell({ secret }: { secret: Secret }) {
  const updated = new Date(secret.updated_at)
  const created = new Date(secret.created_at)

  return (
    <time
      dateTime={secret.updated_at}
      className="text-muted-foreground"
      title={`Updated ${momentFormat.format(updated)}; created ${momentFormat.format(created)}`}
    >
      {dayFormat.format(updated)}
    </time>
  )
}

function RowActions({ secret }: { secret: Secret }) {
  const reveal = use(RevealContext)
  const refresh = useRefreshSecrets()
  const shown = reveal.shown(secret.name)

  return (
    <span className="flex justify-end gap-0.5">
      <Button
        variant="ghost"
        size="icon-sm"
        aria-pressed={shown}
        aria-label={shown ? `Hide ${secret.name}` : `Reveal ${secret.name}`}
        disabled={reveal.all}
        onClick={() => reveal.toggle(secret.name)}
      >
        {shown ? <EyeOff /> : <Eye />}
      </Button>
      <SecretDialog
        trigger={
          <Button variant="ghost" size="icon-sm" aria-label={`Edit ${secret.name}`}>
            <Pencil />
          </Button>
        }
        title={`Edit ${secret.name}`}
        about={
          secret.kind === "oauth_token_set"
            ? "To update credentials, enter the complete replacement set."
            : "Enter a new value to replace it."
        }
        secret={secret}
        action="Save"
      />
      <ConfirmDialog
        trigger={
          <Button variant="ghost" size="icon-sm" aria-label={`Delete ${secret.name}`}>
            <Trash2 />
          </Button>
        }
        title={`Delete ${secret.name}?`}
        consequence="Every version of it is removed. This cannot be undone."
        action="Delete secret"
        run={() => deleteSecret({ path: { name: secret.name } })}
        onDone={refresh}
        done={`${secret.name} was deleted`}
      />
    </span>
  )
}

function useRefreshSecrets() {
  const queryClient = useQueryClient()

  return () => queryClient.invalidateQueries({ queryKey: secretsKey })
}

// What a secret's form holds. A new secret has a name and one value; a generic one is given a new value; a token
// set its complete replacement set, as Omnara's edit dialog asks.
const empty = { name: "", value: "", refresh_token: "", access_token: "", id_token: "" }

const required = (what: string) => z.string().min(1, `${what} is required.`)

// Every schema has the whole form's shape; each mode asks only for the fields it shows.
const fields = z.object({
  name: z.string(),
  value: z.string(),
  refresh_token: z.string(),
  access_token: z.string(),
  id_token: z.string(),
})

const schemas = {
  add: fields.extend({
    name: z
      .string()
      .regex(/^[a-z0-9][a-z0-9-]{0,62}$/, "Lower-case letters, digits and dashes, 63 at most."),
    value: required("A value"),
  }),
  generic: fields.extend({ value: required("A value") }),
  tokenSet: fields.extend({ refresh_token: required("The refresh token") }),
}

function SecretDialog({
  trigger,
  title,
  about,
  secret,
  action,
}: {
  trigger: ReactElement
  title: string
  about: string
  secret?: Secret
  action: string
}) {
  const formId = useId()
  const refresh = useRefreshSecrets()
  const [open, setOpen] = useState(false)
  const mode = !secret ? "add" : secret.kind === "oauth_token_set" ? "tokenSet" : "generic"

  const save = useMutation({
    mutationFn: (value: typeof empty) => {
      const name = secret?.name ?? value.name

      if (mode !== "tokenSet") return putSecret({ path: { name }, body: { value: value.value } })

      const { refresh_token, access_token, id_token } = value
      const values = Object.fromEntries(
        Object.entries({ refresh_token, access_token, id_token }).filter(([, v]) => v !== ""),
      )

      return putSecret({ path: { name }, body: { values } })
    },
  })

  const form = useForm({
    defaultValues: empty,
    validators: { onSubmit: schemas[mode] },
    onSubmit: async ({ value }) => {
      try {
        await save.mutateAsync(value)
      } catch {
        return // The dialog stays open and shows what the server refused.
      }

      await refresh()
      change(false)
      said(secret ? `${secret.name} was updated` : `${value.name} was added`)
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (!next) {
      form.reset()
      save.reset()
    }
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger render={trigger} />
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{about}</DialogDescription>
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
              {mode === "add" && (
                <form.Field name="name">
                  {(field) => (
                    <TextField
                      field={field}
                      label="Name"
                      placeholder="openai-key"
                      description="Lower-case letters, digits and dashes."
                    />
                  )}
                </form.Field>
              )}
              {mode !== "tokenSet" && (
                <form.Field name="value">
                  {(field) => (
                    <TextField
                      field={field}
                      label="Value"
                      secret
                      placeholder={secret && "••••••••"}
                    />
                  )}
                </form.Field>
              )}
              {mode === "tokenSet" &&
                (["refresh_token", "access_token", "id_token"] as const).map((key) => (
                  <form.Field key={key} name={key}>
                    {(field) => (
                      <TextField field={field} label={key} secret placeholder="••••••••" />
                    )}
                  </form.Field>
                ))}
            </FieldGroup>
            <ErrorAlert error={save.error} />
          </form>
        </DialogPanel>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button type="submit" form={formId} disabled={save.isPending}>
            {save.isPending && <Spinner data-icon="inline-start" />}
            {action}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function AddSecret() {
  return (
    <SecretDialog
      trigger={
        <Button>
          <Plus data-icon="inline-start" />
          Add secret
        </Button>
      }
      title="New secret"
      about="It is kept encrypted on the server. You can read it again here."
      action="Add"
    />
  )
}
