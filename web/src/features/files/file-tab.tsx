import { Copy } from "lucide-react"
import { lazy, Suspense } from "react"

import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"

import { problemOf, useDiff, useFile } from "./files-state"

// The editor loads with the first file opened, not with the page (Omnigent's lazy MonacoCodeEditor).
const CodeView = lazy(() => import("./code-view"))

// FileTab is a file of a chat's folder in a tab of the pane (issue #255): its text, or its diff since HEAD, under
// a header that names it, as Orca's editor shows a file (EditorPanelHeader.tsx, EditorPanelHeaderPath.tsx: one
// 32 px row, the folder dimmed before the name, a copy button). A binary file, one cut at 10 MiB, and a refusal
// say so, as Omnigent's FileViewer.tsx and TruncatedBanner do.
export function FileTab({
  chat,
  workspace,
  path,
  view,
  shown,
}: {
  chat: string
  workspace: string
  path: string
  view: "file" | "diff"
  shown: boolean
}) {
  return view === "diff" ? (
    <DiffBody chat={chat} workspace={workspace} path={path} />
  ) : (
    <FileBody chat={chat} workspace={workspace} path={path} shown={shown} />
  )
}

function FileHeader({ path, note }: { path: string; note?: string }) {
  const slash = path.lastIndexOf("/")

  return (
    <div className="flex min-h-8 shrink-0 items-center gap-1 border-b px-3.5 py-0.5 text-xs">
      <span className="min-w-0 flex-1 truncate" title={path}>
        {slash > 0 && <span className="text-muted-foreground">{path.slice(0, slash + 1)}</span>}
        <span className="font-medium text-foreground">{path.slice(slash + 1)}</span>
      </span>
      {note && <span className="shrink-0 text-muted-foreground">{note}</span>}
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant="ghost"
              size="icon-xs"
              aria-label="Copy path"
              onClick={() => void navigator.clipboard?.writeText(path)}
            />
          }
        >
          <Copy />
        </TooltipTrigger>
        <TooltipContent>Copy path</TooltipContent>
      </Tooltip>
    </div>
  )
}

function FileBody({
  chat,
  workspace,
  path,
  shown,
}: {
  chat: string
  workspace: string
  path: string
  shown: boolean
}) {
  const file = useFile(chat, workspace, path, shown)

  return (
    <>
      <FileHeader
        path={path}
        note={file.data?.truncated ? "Only the first 10 MiB is shown" : undefined}
      />
      {file.isPending ? (
        <Waiting />
      ) : file.isError ? (
        <Said>{problemOf(file.error).message}</Said>
      ) : file.data.binary ? (
        <Said>This is a binary file, which is not shown.</Said>
      ) : (
        <Suspense fallback={<Waiting />}>
          <CodeView kind="file" path={path} content={file.data.content} />
        </Suspense>
      )}
    </>
  )
}

function DiffBody({ chat, workspace, path }: { chat: string; workspace: string; path: string }) {
  const diff = useDiff(chat, workspace, path)
  const before = diff.data?.before
  const after = diff.data?.after
  // A file that was created or deleted has one side only: it is shown as its text, as a diff of it against
  // nothing says no more.
  const one = before === undefined ? after : after === undefined ? before : undefined
  const note = !diff.data
    ? undefined
    : before === undefined
      ? "New file"
      : after === undefined
        ? "Deleted: as it was at the last commit"
        : "Changed since the last commit"

  return (
    <>
      <FileHeader path={path} note={note} />
      {diff.isPending ? (
        <Waiting />
      ) : diff.isError ? (
        <Said>{problemOf(diff.error).message}</Said>
      ) : diff.data.binary ? (
        <Said>This is a binary file: its change is not shown.</Said>
      ) : (
        <Suspense fallback={<Waiting />}>
          {one !== undefined ? (
            <CodeView kind="file" path={path} content={one} />
          ) : (
            <CodeView kind="diff" path={path} before={before ?? ""} after={after ?? ""} />
          )}
        </Suspense>
      )}
    </>
  )
}

function Waiting() {
  return (
    <p className="m-auto flex items-center gap-2 text-sm text-muted-foreground">
      <Spinner /> Reading…
    </p>
  )
}

function Said({ children }: { children: React.ReactNode }) {
  return (
    <output className="m-auto block max-w-md px-6 text-center text-sm text-muted-foreground">
      {children}
    </output>
  )
}
