import { lazy, Suspense } from "react"

import { Spinner } from "@/components/ui/spinner"

import { problemOf, useDiff, useFile } from "./files-state"

// The editor loads with the first file opened, not with the page (Omnigent's lazy MonacoCodeEditor).
const CodeView = lazy(() => import("./code-view"))

// FileTab is a file of a chat's folder in a tab of the pane (issue #255): its text, or its diff since HEAD. A
// binary file, one cut at 10 MiB, and a refusal say so, as Omnigent's FileViewer.tsx and TruncatedBanner do.
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

  if (file.isPending) return <Waiting />
  if (file.isError) return <Said>{problemOf(file.error).message}</Said>
  if (file.data.binary) return <Said>{path} is a binary file, which is not shown.</Said>

  return (
    <>
      {file.data.truncated && (
        <output className="block border-b bg-muted px-3 py-1.5 text-xs text-muted-foreground">
          Only the first 10 MiB of this file is shown.
        </output>
      )}
      <Suspense fallback={<Waiting />}>
        <CodeView kind="file" path={path} content={file.data.content} />
      </Suspense>
    </>
  )
}

function DiffBody({ chat, workspace, path }: { chat: string; workspace: string; path: string }) {
  const diff = useDiff(chat, workspace, path)

  if (diff.isPending) return <Waiting />
  if (diff.isError) return <Said>{problemOf(diff.error).message}</Said>
  if (diff.data.binary) return <Said>{path} is a binary file: its change is not shown.</Said>

  const { before, after } = diff.data
  // A file that was created or deleted has one side only: it is shown as its text, as a diff of it against
  // nothing says no more.
  const one = before === undefined ? after : after === undefined ? before : undefined

  return (
    <>
      <p className="border-b px-3 py-1.5 text-xs text-muted-foreground">
        {before === undefined
          ? "A new file."
          : after === undefined
            ? "A deleted file, as it was at the last commit."
            : "Changed since the last commit."}
      </p>
      <Suspense fallback={<Waiting />}>
        {one !== undefined ? (
          <CodeView kind="file" path={path} content={one} />
        ) : (
          <CodeView kind="diff" path={path} before={before ?? ""} after={after ?? ""} />
        )}
      </Suspense>
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
