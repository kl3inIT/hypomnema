import { useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import {
  columnFilteringFeature,
  createFilteredRowModel,
  createSortedRowModel,
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
  ExternalLink,
  Link2,
  MessagesSquare,
  MoreHorizontal,
  PanelsTopLeft,
  Search,
  Trash2,
} from "lucide-react"
import { useState } from "react"

import "@/api/client"
import { listSharesOptions, listSharesQueryKey } from "@/api/generated/@tanstack/react-query.gen"
import { deleteArtifactShare } from "@/api/generated/sdk.gen"
import type { SharedLink } from "@/api/generated/types.gen"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { ErrorAlert } from "@/components/error-alert"
import { SettingsPage } from "@/components/settings-page"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty"
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
import { said } from "@/lib/said"
import { since } from "@/lib/time"

import { ArtifactIcon } from "./artifact-icon"

// SharedLinksScreen is every view-only link in one place (issue #765): what is shared, with whom, how often it was
// opened, and a revoke. The list sits in settings as LibreChat's Shared links (SettingsTabs/Data/SharedLinks.tsx),
// Open WebUI's Shared Chats (layout/SharedChatsModal.svelte) and Metabase's Public sharing
// (PublicLinksListing.tsx) do; Grafana's public dashboards list (PublicDashboardListTable.tsx) is the fourth. Its
// table is the Gateway keys page's (gateway-keys-screen.tsx): a search and columns that sort. A row opens its
// artifact as every one of them opens its item; Copy is Open WebUI's, Open the link Grafana's and Metabase's, Open
// its chat LibreChat's; Revoke asks first as LibreChat, Grafana and Metabase do, with the Share dialog's words
// (share-dialog.tsx). Links are made from an artifact's Share menu, never here, as in all four.
export function SharedLinksScreen() {
  const links = useQuery(listSharesOptions())

  return (
    <SettingsPage
      wide
      title="Shared links"
      description="View-only links to artifacts. Anyone with a link sees the artifact's newest revision without signing in, until you revoke it. New links are made from an artifact's Share menu."
    >
      {links.isPending && (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner />
          Loading…
        </p>
      )}
      <ErrorAlert error={links.error} />
      {links.data && links.data.data.length === 0 && (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <Link2 />
            </EmptyMedia>
            <EmptyTitle>No shared links yet</EmptyTitle>
            <EmptyDescription>
              Open an artifact in a chat and choose Share link… to send someone a view-only link.
              Every link shows up here.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}
      {links.data && links.data.data.length > 0 && <LinksTable links={links.data.data} />}
    </SettingsPage>
  )
}

const features = tableFeatures({
  columnFilteringFeature,
  rowSortingFeature,
  filteredRowModel: createFilteredRowModel(),
  sortedRowModel: createSortedRowModel(),
  sortFns: { alphanumeric: sortFn_alphanumeric },
})

type LinkColumn = Column<typeof features, SharedLink>

// SortHead is a column's name as a button that sorts by it, as on the Gateway keys page.
function SortHead({ column, label }: { column: LinkColumn; label: string }) {
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

// botOf is a link's Bot as the owner reads it: its name, or its id once the Bot is gone.
const botOf = (link: SharedLink) => link.bot_name || link.bot

// The search finds a link by its artifact's title or by whom it is for.
const matches = (link: SharedLink, query: string) =>
  link.title.toLowerCase().includes(query) || link.label.toLowerCase().includes(query)

const columns: ColumnDef<typeof features, SharedLink>[] = [
  {
    accessorKey: "title",
    sortFn: "alphanumeric",
    filterFn: (row, _id, query: string) => matches(row.original, query),
    header: ({ column }) => <SortHead column={column} label="Artifact" />,
    cell: ({ row }) => (
      <Link
        to="/artifacts/$artifactId"
        params={{ artifactId: row.original.artifact_id }}
        search={{}}
        className="inline-flex max-w-72 items-center gap-2 font-medium hover:underline"
      >
        <ArtifactIcon type={row.original.type} className="size-4 shrink-0 text-muted-foreground" />
        <span className="truncate">{row.original.title}</span>
      </Link>
    ),
  },
  {
    accessorKey: "label",
    sortFn: "alphanumeric",
    header: ({ column }) => <SortHead column={column} label="For whom" />,
    cell: ({ row }) => row.original.label,
  },
  {
    id: "bot",
    accessorFn: botOf,
    sortFn: "alphanumeric",
    header: ({ column }) => <SortHead column={column} label="Bot" />,
    cell: ({ row }) => <span className="text-muted-foreground">{botOf(row.original)}</span>,
  },
  {
    accessorKey: "views",
    header: ({ column }) => <SortHead column={column} label="Views" />,
    cell: ({ row }) => row.original.views,
  },
  {
    id: "last_viewed",
    accessorFn: (link) => link.last_viewed_at ?? "",
    sortFn: "alphanumeric",
    header: ({ column }) => <SortHead column={column} label="Last viewed" />,
    cell: ({ row }) => (
      <span className="text-muted-foreground">
        {row.original.last_viewed_at ? since(row.original.last_viewed_at) : "Never"}
      </span>
    ),
  },
  {
    accessorKey: "created_at",
    sortFn: "alphanumeric",
    header: ({ column }) => <SortHead column={column} label="Made" />,
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
    cell: ({ row }) => <LinkActions link={row.original} />,
  },
]

const sortable = new Set(["title", "label", "bot", "views", "last_viewed", "created_at"])

function LinksTable({ links }: { links: SharedLink[] }) {
  const table = useTable({
    features,
    columns,
    data: links,
    getRowId: (link) => link.id,
    // Newest first, as the server lists them and as LibreChat and Open WebUI sort theirs.
    initialState: { sorting: [{ id: "created_at", desc: true }] },
  })
  const title = table.getColumn("title")
  const query = (title?.getFilterValue() as string | undefined) ?? ""
  const rows = table.getRowModel().rows
  const views = links.reduce((sum, link) => sum + link.views, 0)

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <InputGroup>
        <InputGroupAddon>
          <Search />
        </InputGroupAddon>
        <InputGroupInput
          type="search"
          autoComplete="off"
          aria-label="Search links by artifact or name"
          placeholder="Search links by artifact or name…"
          value={query}
          onChange={(event) => title?.setFilterValue(event.target.value.trim().toLowerCase())}
        />
      </InputGroup>
      <div className="min-w-0 overflow-hidden rounded-xl border bg-card">
        <Table aria-label="Shared links">
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
              <TableRow
                key={row.id}
                aria-label={`${row.original.title}, for ${row.original.label}`}
              >
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
                    No link matches “{query}”.
                  </p>
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
      <p className="text-sm text-muted-foreground">
        {links.length === 1 ? "1 link" : `${links.length} links`},{" "}
        {views === 1 ? "1 view" : `${views} views`} in all.
      </p>
    </div>
  )
}

// linkURL is a link as it is sent: this page's own origin and the link's path.
const linkURL = (link: SharedLink) => `${window.location.origin}${link.path}`

// LinkActions are a row's Copy and its menu: open the link, the artifact or its chat, and revoke.
function LinkActions({ link }: { link: SharedLink }) {
  const queryClient = useQueryClient()
  const [copied, setCopied] = useState(false)
  const [revoking, setRevoking] = useState(false)

  async function copy() {
    try {
      await navigator.clipboard.writeText(linkURL(link))
      setCopied(true)
      said(`The link for ${link.label} was copied`)
    } catch {
      said("The link could not be copied here. Open it and copy its address.")
    }
  }

  return (
    <span className="flex justify-end gap-0.5">
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={`Copy the link for ${link.label}`}
        onClick={() => void copy()}
      >
        {copied ? <Check /> : <Copy />}
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`More for the link for ${link.label}`}
            />
          }
        >
          <MoreHorizontal />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" width="content">
          <DropdownMenuItem
            render={
              <a
                href={linkURL(link)}
                target="_blank"
                rel="noopener noreferrer"
                aria-label="Open the link"
              />
            }
          >
            <ExternalLink />
            Open the link
          </DropdownMenuItem>
          <DropdownMenuItem
            render={
              <Link
                to="/artifacts/$artifactId"
                params={{ artifactId: link.artifact_id }}
                search={{}}
              />
            }
          >
            <PanelsTopLeft />
            Open the artifact
          </DropdownMenuItem>
          <DropdownMenuItem render={<Link to="/chats/$chatId" params={{ chatId: link.chat_id }} />}>
            <MessagesSquare />
            Open its chat
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" onClick={() => setRevoking(true)}>
            <Trash2 />
            Revoke link…
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <ConfirmDialog
        open={revoking}
        onOpenChange={setRevoking}
        title={`Revoke the link for ${link.label}?`}
        consequence="The link stops working at once, also on a page that is open. This cannot be undone; a new link can be made from the artifact's Share menu."
        action="Revoke link"
        run={() => deleteArtifactShare({ path: { share_id: link.id } })}
        onDone={() => queryClient.invalidateQueries({ queryKey: listSharesQueryKey() })}
        done={`The link for ${link.label} was revoked`}
      />
    </span>
  )
}
