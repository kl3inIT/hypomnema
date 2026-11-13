import { code } from "@streamdown/code"
import { Streamdown } from "streamdown"
import "streamdown/styles.css"

import { inFolder, useImage, useObjectURL } from "./files-state"

const plugins = { code }
const controls = { table: true, code: { copy: true, download: false } } as const

// FilePreview shows a Markdown file of a chat's folder as it reads (issue #297), with the renderer of the chat
// (markdown.tsx, Streamdown, which keeps HTML to a safe set). An image the file names by a path of the folder is
// read through the file API and shown from a blob: URL, as Orca's useLocalImageSrc.ts does; one at an http(s) or
// data: address is left to the browser.
export default function FilePreview({
  chat,
  workspace,
  path,
  text,
}: {
  chat: string
  workspace: string
  path: string
  text: string
}) {
  const components = {
    img: ({ src, alt }: { src?: unknown; alt?: unknown }) => (
      <FolderImage
        chat={chat}
        workspace={workspace}
        from={path}
        src={typeof src === "string" ? src : ""}
        alt={typeof alt === "string" ? alt : ""}
      />
    ),
  }

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <Streamdown
        className="chat-markdown mx-auto w-full max-w-3xl min-w-0 px-6 py-5 text-sm leading-relaxed [overflow-wrap:anywhere] text-foreground/90"
        plugins={plugins}
        controls={controls}
        components={components}
      >
        {text}
      </Streamdown>
    </div>
  )
}

function FolderImage({
  chat,
  workspace,
  from,
  src,
  alt,
}: {
  chat: string
  workspace: string
  from: string
  src: string
  alt: string
}) {
  const local = inFolder(from, src)
  const image = useImage(chat, workspace, local ?? "", local !== null)
  const url = useObjectURL(image.data)

  if (local === null) return <img src={src} alt={alt} className="max-w-full" />
  if (!url) return <span className="text-xs text-muted-foreground">[{alt || local}]</span>

  return <img src={url} alt={alt} className="max-w-full" />
}
