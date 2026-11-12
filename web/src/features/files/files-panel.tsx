import { useQueries, useQueryClient } from "@tanstack/react-query"
import { useVirtualizer } from "@tanstack/react-virtual"
import { ChevronRight, Copy, File, Folder, FolderOpen, RefreshCw, X } from "lucide-react"
import { useRef, useState } from "react"

import "@/api/client"
import { listWorkspaceDirectory } from "@/api/generated/sdk.gen"
import type {
  Workspace,
  WorkspaceEntry,
  WorkspaceListing as Listing,
} from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"

import { problemOf, useChanges, useWorkspaces, workspaceKey } from "./files-state"

// FilesPanel shows a chat's working folders beside its pane (issue #255), as Omnigent's FilesPanel.tsx: the folder
// picked from those of the chat (its Bot's own, and each Task's worktree on its machine), its path with a copy
// button, Refresh, "Changed | All files", and a filter by name. Changed lists what git says changed, newest first,
// with lines added and removed, as Omnigent's FlatFileList.tsx; All files is the folder as a tree read a directory
// at a time when it is opened, its rows drawn only where they are seen (Orca's FileExplorerVirtualRows.tsx and
// Omnigent's FolderTree.tsx, @tanstack/react-virtual, 25 px rows). A changed file opens as its diff, any other as
// its text, in a tab of the pane.
export function FilesPanel({
  chat,
  onOpen,
  onClose,
}: {
  chat: string
  onOpen: (workspace: string, path: string, view: "file" | "diff") => void
  onClose: () => void
}) {
  const workspaces = useWorkspaces(chat)
  const [picked, setPicked] = useState<string | null>(null)
  const [mode, setMode] = useState<"changed" | "all">("changed")
  const [filter, setFilter] = useState("")
  const queryClient = useQueryClient()

  const list = workspaces.data ?? []
  // A chat's newest Task is where work is most likely; without one, the Bot's folder.
  const fallback = list.findLast((w) => w.kind === "task") ?? list[0]
  const workspace = list.find((w) => w.id === picked) ?? fallback

  return (
    <aside
      aria-label="Files"
      className="absolute inset-0 z-20 flex flex-col bg-sidebar md:static md:w-80 md:shrink-0 md:border-l"
    >
      <div className="flex flex-col gap-2 border-b p-2">
        <div className="flex items-center gap-1">
          <div className="min-w-0 flex-1">
            <Select
              value={workspace?.id ?? null}
              onValueChange={(value) => value && setPicked(value)}
              disabled={list.length === 0}
            >
              <SelectTrigger aria-label="Folder" size="sm" width="full">
                <SelectValue placeholder="No folder yet">
                  {workspace && <WorkspaceName workspace={workspace} />}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {list.some((w) => w.kind === "bot") && (
                  <SelectGroup>
                    <SelectLabel>The Bot's folder</SelectLabel>
                    {list
                      .filter((w) => w.kind === "bot")
                      .map((w) => (
                        <SelectItem key={w.id} value={w.id}>
                          <WorkspaceName workspace={w} />
                        </SelectItem>
                      ))}
                  </SelectGroup>
                )}
                {list.some((w) => w.kind === "task") && (
                  <SelectGroup>
                    <SelectLabel>Worktrees of this chat's Tasks</SelectLabel>
                    {list
                      .filter((w) => w.kind === "task")
                      .map((w) => (
                        <SelectItem key={w.id} value={w.id}>
                          <WorkspaceName workspace={w} />
                        </SelectItem>
                      ))}
                  </SelectGroup>
                )}
              </SelectContent>
            </Select>
          </div>
          <PanelButton
            label="Refresh"
            onClick={() =>
              void queryClient.invalidateQueries({ queryKey: [...workspaceKey, chat] })
            }
          >
            <RefreshCw />
          </PanelButton>
          <PanelButton label="Close files" onClick={onClose}>
            <X />
          </PanelButton>
        </div>
        {workspace && (
          <div className="flex items-center gap-1 text-xs text-muted-foreground">
            <span className="min-w-0 flex-1 truncate font-mono" title={workspace.root}>
              {workspace.root}
            </span>
            <PanelButton
              label="Copy folder path"
              onClick={() => void navigator.clipboard?.writeText(workspace.root)}
            >
              <Copy />
            </PanelButton>
          </div>
        )}
        <fieldset className="m-0 inline-flex gap-0.5 rounded-lg border border-solid bg-muted p-0.5">
          <legend className="sr-only">Show</legend>
          {(
            [
              ["changed", "Changed"],
              ["all", "All files"],
            ] as const
          ).map(([value, label]) => (
            <button
              key={value}
              type="button"
              aria-pressed={mode === value}
              onClick={() => setMode(value)}
              className="flex-1 rounded-md px-2.5 py-1 text-xs text-muted-foreground aria-pressed:bg-foreground/9 aria-pressed:text-foreground"
            >
              {label}
            </button>
          ))}
        </fieldset>
        <Input
          aria-label="Find files by name"
          placeholder="Find files by name"
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
        />
      </div>
      {workspaces.isPending ? (
        <Note>
          <Spinner /> Reading the folders…
        </Note>
      ) : workspaces.isError ? (
        <Note>{problemOf(workspaces.error).message}</Note>
      ) : !workspace ? (
        <Note>
          This chat has no folder to show yet: its Bot's folder is not read on this control plane,
          and none of its Tasks has run.
        </Note>
      ) : !workspace.online ? (
        <Note>
          {workspace.node_name ?? "The machine"} is offline, so its folder cannot be read. It is
          read again when the machine connects.
        </Note>
      ) : mode === "changed" ? (
        <ChangedList
          key={workspace.id}
          chat={chat}
          workspace={workspace.id}
          filter={filter}
          onOpen={(path, view) => onOpen(workspace.id, path, view)}
        />
      ) : (
        <FileTree
          key={workspace.id}
          chat={chat}
          workspace={workspace.id}
          filter={filter}
          onOpen={(path) => onOpen(workspace.id, path, "file")}
        />
      )}
    </aside>
  )
}

function WorkspaceName({ workspace }: { workspace: Workspace }) {
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <span
        aria-hidden
        className={cn(
          "size-1.5 shrink-0 rounded-full",
          workspace.online ? "bg-done" : "bg-muted-foreground/50",
        )}
      />
      <span className="truncate">{workspace.name}</span>
      {workspace.node_name && (
        <span className="shrink-0 text-xs text-muted-foreground">{workspace.node_name}</span>
      )}
    </span>
  )
}

function PanelButton({
  label,
  onClick,
  children,
}: {
  label: string
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={<Button variant="ghost" size="icon-xs" aria-label={label} onClick={onClick} />}
      >
        {children}
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  )
}

function Note({ children }: { children: React.ReactNode }) {
  return (
    <output className="flex items-start gap-2 p-4 text-sm text-muted-foreground">{children}</output>
  )
}

const marks = {
  created: { letter: "A", className: "text-done", label: "created" },
  modified: { letter: "M", className: "text-needs-you", label: "modified" },
  deleted: { letter: "D", className: "text-failed", label: "deleted" },
} as const

function ChangedList({
  chat,
  workspace,
  filter,
  onOpen,
}: {
  chat: string
  workspace: string
  filter: string
  onOpen: (path: string, view: "file" | "diff") => void
}) {
  const changes = useChanges(chat, workspace)

  if (changes.isPending) {
    return (
      <Note>
        <Spinner /> Asking git what changed…
      </Note>
    )
  }
  if (changes.isError) return <Note>{problemOf(changes.error).message}</Note>
  if (!changes.data.git) {
    return (
      <Note>
        This folder is not in a git repository, so there are no changes to show. All files lists it.
      </Note>
    )
  }

  const shown = changes.data.changes.filter((c) =>
    c.path.toLowerCase().includes(filter.trim().toLowerCase()),
  )
  if (shown.length === 0) {
    return <Note>{filter.trim() ? "No changed file has that name." : "Nothing has changed."}</Note>
  }

  return (
    <ul aria-label="Changed files" className="min-h-0 flex-1 overflow-y-auto p-1 text-sm">
      {shown.map((c) => {
        const mark = marks[c.status]
        const slash = c.path.lastIndexOf("/")

        return (
          <li key={c.path}>
            <button
              type="button"
              title={`${c.path}, ${mark.label}`}
              onClick={() => onOpen(c.path, "diff")}
              className="flex w-full items-center gap-2 rounded-md px-2 py-1 text-left hover:bg-accent focus-visible:bg-accent focus-visible:outline-none"
            >
              <span aria-label={mark.label} className={cn("font-mono text-xs", mark.className)}>
                {mark.letter}
              </span>
              <span className="min-w-0 flex-1 truncate">
                {c.path.slice(slash + 1)}
                {slash > 0 && (
                  <span className="ms-1.5 text-xs text-muted-foreground">
                    {c.path.slice(0, slash)}
                  </span>
                )}
              </span>
              {c.lines_added !== undefined && (
                <span className="shrink-0 font-mono text-xs">
                  <span className="text-done">+{c.lines_added}</span>{" "}
                  <span className="text-failed">−{c.lines_removed ?? 0}</span>
                </span>
              )}
            </button>
          </li>
        )
      })}
      {changes.data.truncated && (
        <li className="px-2 py-1 text-xs text-muted-foreground">
          More changes than can be listed.
        </li>
      )}
    </ul>
  )
}

type Row =
  | { kind: "entry"; entry: WorkspaceEntry; depth: number }
  | { kind: "note"; text: string; depth: number; key: string }

// byKind puts directories first, then names in order, as Orca's sortDirEntries.
const byKind = (a: WorkspaceEntry, b: WorkspaceEntry) =>
  a.type === b.type ? a.name.localeCompare(b.name) : a.type === "directory" ? -1 : 1

// treeRows is the tree as rows, from the listings of the open directories: a directory's entries under it, a
// directory not read yet or refused as one row that says so.
function treeRows(
  byDir: Map<
    string,
    { isPending: boolean; isError: boolean; error: unknown; data?: Listing } | undefined
  >,
  open: string[],
  wanted: string,
): Row[] {
  const out: Row[] = []
  const walk = (path: string, depth: number) => {
    const listing = byDir.get(path)
    if (!listing || listing.isPending || (!listing.isError && !listing.data)) {
      out.push({ kind: "note", text: "Reading…", depth, key: `reading:${path}` })

      return
    }
    if (listing.isError || !listing.data) {
      out.push({
        kind: "note",
        text: problemOf(listing.error).message,
        depth,
        key: `error:${path}`,
      })

      return
    }
    for (const entry of [...listing.data.entries].sort(byKind)) {
      const dir = entry.type === "directory"
      if (wanted && !dir && !entry.name.toLowerCase().includes(wanted)) continue
      out.push({ kind: "entry", entry, depth })
      if (dir && open.includes(entry.path)) walk(entry.path, depth + 1)
    }
    if (listing.data.has_more) {
      out.push({
        kind: "note",
        text: "More entries than can be listed.",
        depth,
        key: `more:${path}`,
      })
    }
  }
  walk("", 0)

  return out
}

function FileTree({
  chat,
  workspace,
  filter,
  onOpen,
}: {
  chat: string
  workspace: string
  filter: string
  onOpen: (path: string) => void
}) {
  const [open, setOpen] = useState<string[]>([])
  const scroller = useRef<HTMLDivElement>(null)
  const dirs = ["", ...open]
  // The open directories are read together, as Omnigent fetches its expanded directories centrally.
  const listings = useQueries({
    queries: dirs.map((path) => ({
      queryKey: [...workspaceKey, chat, workspace, "tree", path],
      queryFn: async () =>
        (
          await listWorkspaceDirectory({
            path: { chat_id: chat, workspace_id: workspace },
            query: { path },
          })
        ).data,
      staleTime: 5_000,
    })),
  })
  const byDir = new Map(dirs.map((path, i) => [path, listings[i]]))
  const wanted = filter.trim().toLowerCase()

  const rows = treeRows(byDir, open, wanted)

  // TanStack Virtual hands back functions the React Compiler cannot memoize; this tree is drawn afresh as it is.
  // oxlint-disable-next-line react/incompatible-library
  const virtual = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scroller.current,
    estimateSize: () => 25,
    overscan: 12,
  })

  const root = byDir.get("")
  if (root?.isSuccess && root.data.entries.length === 0) return <Note>This folder is empty.</Note>

  return (
    <div
      ref={scroller}
      role="tree"
      aria-label="All files"
      className="min-h-0 flex-1 overflow-y-auto p-1 text-sm"
    >
      <div className="relative w-full" style={{ height: virtual.getTotalSize() }}>
        {virtual.getVirtualItems().map((item) => {
          const row = rows[item.index]
          if (!row) return null
          const style = {
            transform: `translateY(${item.start}px)`,
            paddingInlineStart: 8 + row.depth * 12,
          }

          if (row.kind === "note") {
            return (
              <div
                key={row.key}
                className="absolute inset-x-0 top-0 flex h-[25px] items-center text-xs text-muted-foreground"
                style={style}
              >
                {row.text}
              </div>
            )
          }

          const { entry } = row
          const dir = entry.type === "directory"
          const expanded = dir && open.includes(entry.path)

          return (
            <button
              key={entry.path}
              type="button"
              role="treeitem"
              aria-expanded={dir ? expanded : undefined}
              title={entry.path}
              onClick={() =>
                dir
                  ? setOpen(
                      expanded
                        ? open.filter((p) => p !== entry.path && !p.startsWith(`${entry.path}/`))
                        : [...open, entry.path],
                    )
                  : onOpen(entry.path)
              }
              className="absolute inset-x-0 top-0 flex h-[25px] items-center gap-1.5 rounded-md pe-2 text-left hover:bg-accent focus-visible:bg-accent focus-visible:outline-none"
              style={style}
            >
              <ChevronRight
                aria-hidden
                className={cn(
                  "size-3.5 shrink-0 text-muted-foreground transition-transform",
                  !dir && "invisible",
                  expanded && "rotate-90",
                )}
              />
              {dir ? (
                expanded ? (
                  <FolderOpen aria-hidden className="size-4 shrink-0 text-muted-foreground" />
                ) : (
                  <Folder aria-hidden className="size-4 shrink-0 text-muted-foreground" />
                )
              ) : (
                <File aria-hidden className="size-4 shrink-0 text-muted-foreground" />
              )}
              <span className="truncate">{entry.name}</span>
            </button>
          )
        })}
      </div>
    </div>
  )
}
