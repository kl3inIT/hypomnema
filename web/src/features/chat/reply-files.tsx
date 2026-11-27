import { CircleAlert, Download } from "lucide-react"
import { useState } from "react"

import "@/api/client"
import { readWorkspaceRaw } from "@/api/generated/sdk.gen"
import {
  Attachment,
  AttachmentContent,
  AttachmentDescription,
  AttachmentMedia,
  AttachmentTitle,
  AttachmentTrigger,
} from "@/components/ui/attachment"
import { Spinner } from "@/components/ui/spinner"
import { rawURL } from "@/features/files/files-state"
import { KindIcon } from "@/features/preview/kind-icon"
import { problemOf } from "@/lib/server-error"

import { botFolder } from "./attachments"
import { fileName, folderPath } from "./media"

// The files a Bot's reply asked for with MEDIA: lines (issue #699), as chips under its text, as Lark gets them after
// the reply: drawn as the composer's chips for the owner's files (attachment-strip.tsx, message-files.tsx), with the
// icon of the file's kind that an Artifact's card shows (preview/kind-icon.tsx). A chip downloads the file from the
// Bot's folder as the Files panel does (rawURL); a file the server refuses, not there any more or outside the
// folder, turns the chip red with the server's own message.
export function ReplyFiles({ chat, paths }: { chat: string; paths: string[] }) {
  return (
    <ul aria-label="Files" className="mt-2 flex flex-wrap gap-1.5">
      {paths.map((path, i) => (
        // A reply may name one file twice; each line is its own chip.
        <li key={`${i}-${path}`}>
          <ReplyFile chat={chat} path={folderPath(path)} />
        </li>
      ))}
    </ul>
  )
}

function ReplyFile({ chat, path }: { chat: string; path: string }) {
  const name = fileName(path)
  const href = rawURL(chat, botFolder, path)
  const [state, setState] = useState<"done" | "processing" | "error">("done")
  const [problem, setProblem] = useState("")

  // The bytes are read first so that a refusal is said here, not saved as a file; then they are saved under the
  // file's name, as the browser saves the server's attachment.
  const download = async () => {
    setState("processing")
    try {
      const bytes = (
        await readWorkspaceRaw({
          path: { chat_id: chat, workspace_id: botFolder },
          query: { path },
          parseAs: "blob",
        })
      ).data as Blob
      const url = URL.createObjectURL(bytes)
      const a = document.createElement("a")
      a.href = url
      a.download = name
      a.click()
      setTimeout(() => URL.revokeObjectURL(url), 0)
      setState("done")
    } catch (error) {
      setProblem(problemOf(error).message)
      setState("error")
    }
  }

  return (
    <div className="max-w-64">
      <Attachment size="sm" state={state} title={state === "error" ? problem : path}>
        <AttachmentMedia>
          {state === "processing" ? (
            <Spinner />
          ) : state === "error" ? (
            <CircleAlert />
          ) : (
            <KindIcon name={name} />
          )}
        </AttachmentMedia>
        <AttachmentContent>
          <AttachmentTitle>{name}</AttachmentTitle>
          <AttachmentDescription>
            {state === "error" ? (
              problem
            ) : (
              <span className="inline-flex items-center gap-1">
                <Download className="size-3" aria-hidden /> Download
              </span>
            )}
          </AttachmentDescription>
        </AttachmentContent>
        <AttachmentTrigger
          render={
            <a
              aria-label={`Download ${name}`}
              href={href}
              download={name}
              onClick={(event) => {
                if (event.metaKey || event.ctrlKey || event.shiftKey) return
                event.preventDefault()
                if (state !== "processing") void download()
              }}
            />
          }
        />
      </Attachment>
    </div>
  )
}
