import { useQueryClient } from "@tanstack/react-query"
import { Copy, Download, Eye, FileCode } from "lucide-react"
import { lazy, Suspense, useEffect, useState } from "react"

import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { toast } from "@/components/ui/toast"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"

import {
  fileKey,
  imageType,
  isMarkdown,
  maxWrite,
  problemOf,
  rawURL,
  refreshFolder,
  saveFile,
  setUnsaved,
  useDiff,
  useFile,
  useImage,
  useObjectURL,
} from "./files-state"

// The editor loads with the first file opened, not with the page (Omnigent's lazy MonacoCodeEditor).
const CodeView = lazy(() => import("./code-view"))
const FilePreview = lazy(() => import("./file-preview"))

// FileTab is a file of a chat's folder in a tab of the pane (issues #255 and #297): its text, which the owner may
// change and save, or its diff since HEAD, under a header that names it, as Orca's editor shows a file
// (EditorPanelHeader.tsx, EditorPanelHeaderPath.tsx: one 32 px row, the folder dimmed before the name, its
// actions). An image is shown as one; a Markdown file as its preview or its source. A binary file, one cut at
// 10 MiB, and a refusal say so, as Omnigent's FileViewer.tsx and TruncatedBanner do.
export function FileTab({
  tab,
  chat,
  workspace,
  path,
  view,
  shown,
}: {
  tab: string
  chat: string
  workspace: string
  path: string
  view: "file" | "diff"
  shown: boolean
}) {
  if (view === "diff") return <DiffBody chat={chat} workspace={workspace} path={path} />
  if (imageType(path)) return <ImageBody chat={chat} workspace={workspace} path={path} />

  return <FileBody tab={tab} chat={chat} workspace={workspace} path={path} shown={shown} />
}

function HeaderButton({
  label,
  onClick,
  pressed,
  children,
}: {
  label: string
  onClick: () => void
  pressed?: boolean
  children: React.ReactNode
}) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label={label}
            aria-pressed={pressed}
            onClick={onClick}
          />
        }
      >
        {children}
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  )
}

function FileHeader({
  path,
  note,
  children,
}: {
  path: string
  note?: React.ReactNode
  children?: React.ReactNode
}) {
  const slash = path.lastIndexOf("/")

  return (
    <div className="flex min-h-8 shrink-0 items-center gap-1 border-b px-3.5 py-0.5 text-xs">
      <span className="min-w-0 flex-1 truncate" title={path}>
        {slash > 0 && <span className="text-muted-foreground">{path.slice(0, slash + 1)}</span>}
        <span className="font-medium text-foreground">{path.slice(slash + 1)}</span>
      </span>
      {note && <span className="shrink-0 text-muted-foreground">{note}</span>}
      {children}
      <HeaderButton label="Copy path" onClick={() => void navigator.clipboard?.writeText(path)}>
        <Copy />
      </HeaderButton>
    </div>
  )
}

// An edit is the owner's text and the file it started from, whose SHA-256 a save names. Without one, the tab shows
// the file as it is read, every two seconds while shown.
type Edit = { base: { content: string; sha256?: string }; draft: string }

// Why a save stopped: the file changed meanwhile, or it went.
type Trouble = "changed" | "gone" | null

function FileBody({
  tab,
  chat,
  workspace,
  path,
  shown,
}: {
  tab: string
  chat: string
  workspace: string
  path: string
  shown: boolean
}) {
  const file = useFile(chat, workspace, path, shown)
  const queryClient = useQueryClient()
  const [edit, setEdit] = useState<Edit | null>(null)
  const [trouble, setTrouble] = useState<Trouble>(null)
  const [comparing, setComparing] = useState(false)
  const [saving, setSaving] = useState(false)
  const [preview, setPreview] = useState(isMarkdown(path))

  const data = file.data
  const text = edit?.draft ?? data?.content ?? ""
  const unsaved = edit !== null
  // The file on its machine is not what the owner started from (Orca's external change, seen by its watcher; here
  // by the file read again every two seconds while it is shown).
  const movedOn = edit !== null && data?.sha256 !== undefined && data.sha256 !== edit.base.sha256

  useEffect(() => {
    setUnsaved(tab, unsaved)
  }, [tab, unsaved])
  useEffect(() => () => setUnsaved(tab, false), [tab])

  function typed(value: string) {
    if (!data) return
    const base = edit?.base ?? { content: data.content, sha256: data.sha256 }
    // Typed back to what it was: nothing is left to save.
    setEdit(value === base.content ? null : { base, draft: value })
  }

  const editable =
    data !== undefined &&
    !data.binary &&
    !data.truncated &&
    data.sha256 !== undefined &&
    data.bytes <= maxWrite

  async function save(expect: string | undefined) {
    if (edit === null || saving) return
    const { draft } = edit
    setSaving(true)
    try {
      const done = await saveFile(chat, workspace, path, draft, expect)
      // The tab shows what was written until the file is read again.
      queryClient.setQueryData(fileKey(chat, workspace, path), (old: typeof data) =>
        old
          ? {
              ...old,
              content: draft,
              sha256: done.sha256,
              bytes: new TextEncoder().encode(draft).length,
            }
          : old,
      )
      setEdit(null)
      setTrouble(null)
      setComparing(false)
      refreshFolder(queryClient, chat, workspace)
    } catch (error) {
      const problem = problemOf(error)
      if (problem.code === "conflict") setTrouble("changed")
      else if (problem.code === "not_found") setTrouble("gone")
      else toast.add({ title: "Not saved", description: problem.message, type: "error" })
    } finally {
      setSaving(false)
    }
  }

  function reload() {
    setEdit(null)
    setTrouble(null)
    setComparing(false)
  }

  const markdown = isMarkdown(path)
  const stale = trouble === "changed" || movedOn

  return (
    <>
      <FileHeader
        path={path}
        note={
          data?.truncated
            ? "Only the first 10 MiB is shown"
            : data && !data.binary && !editable
              ? "Read only: larger than 1 MiB"
              : unsaved
                ? "Not saved"
                : undefined
        }
      >
        {markdown && editable && (
          <HeaderButton
            label={preview ? "Show the source" : "Show the preview"}
            pressed={preview}
            onClick={() => setPreview(!preview)}
          >
            {preview ? <FileCode /> : <Eye />}
          </HeaderButton>
        )}
        {editable && (
          <Button
            size="xs"
            variant={unsaved ? "default" : "ghost"}
            disabled={!unsaved || saving}
            onClick={() => void save(edit?.base.sha256)}
          >
            {saving && <Spinner />}
            Save
          </Button>
        )}
      </FileHeader>
      {stale && (
        <output className="flex flex-wrap items-center gap-2 border-b bg-needs-you/10 px-3.5 py-1.5 text-xs">
          <span className="min-w-0 flex-1">
            This file changed on its machine since you opened it. Your changes are not saved.
          </span>
          <Button size="xs" variant="outline" onClick={() => setComparing(!comparing)}>
            {comparing ? "Back to your text" : "Compare"}
          </Button>
          <Button size="xs" variant="outline" onClick={reload}>
            Reload
          </Button>
          <Button size="xs" variant="outline" onClick={() => void save(data?.sha256)}>
            Overwrite
          </Button>
        </output>
      )}
      {trouble === "gone" && (
        <output className="flex flex-wrap items-center gap-2 border-b bg-failed/10 px-3.5 py-1.5 text-xs">
          <span className="min-w-0 flex-1">
            This file is not on its machine any more. Your text is still here.
          </span>
          <Button size="xs" variant="outline" onClick={() => void save(undefined)}>
            Save it again
          </Button>
        </output>
      )}
      {file.isPending ? (
        <Waiting />
      ) : file.isError && edit === null ? (
        <Said>{problemOf(file.error).message}</Said>
      ) : data?.binary ? (
        <Said>
          This is a binary file, which is not shown.{" "}
          <a className="underline" href={rawURL(chat, workspace, path)}>
            Download it
          </a>
        </Said>
      ) : (
        <Suspense fallback={<Waiting />}>
          {comparing && data ? (
            <CodeView kind="diff" path={path} before={data.content} after={text} />
          ) : markdown && preview ? (
            <FilePreview chat={chat} workspace={workspace} path={path} text={text} />
          ) : (
            <CodeView
              kind="file"
              path={path}
              content={text}
              {...(editable && {
                onChange: typed,
                onSave: () => void save(edit?.base.sha256),
              })}
            />
          )}
        </Suspense>
      )}
    </>
  )
}

function ImageBody({ chat, workspace, path }: { chat: string; workspace: string; path: string }) {
  const image = useImage(chat, workspace, path)
  const url = useObjectURL(image.data)

  return (
    <>
      <FileHeader path={path}>
        <Tooltip>
          <TooltipTrigger
            render={
              <a
                aria-label="Download"
                href={rawURL(chat, workspace, path)}
                className="inline-flex size-6 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground [&_svg]:size-3.5"
              />
            }
          >
            <Download />
          </TooltipTrigger>
          <TooltipContent>Download</TooltipContent>
        </Tooltip>
      </FileHeader>
      {image.isPending ? (
        <Waiting />
      ) : image.isError ? (
        <Said>{problemOf(image.error).message}</Said>
      ) : url ? (
        <div className="flex min-h-0 flex-1 items-center justify-center overflow-auto p-4">
          <img src={url} alt={path} className="max-h-full max-w-full object-contain" />
        </div>
      ) : null}
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
