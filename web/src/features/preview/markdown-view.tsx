import { code } from "@streamdown/code"
import { Streamdown } from "streamdown"
import "streamdown/styles.css"

import { CutNote } from "./preview-surface"

const plugins = { code }
const controls = { table: true, code: { copy: true, download: false } } as const

// MarkdownView is a Markdown file read as the document it is: headings, paragraphs and tables. Copied from
// MemoryOS (web/src/features/preview/markdown-view.tsx); it renders through the chat's pipeline here too
// (chat/markdown.tsx: Streamdown, which keeps HTML to a safe set), so a file reads the same in the chat and here.
export function MarkdownView({
  text,
  truncated = false,
  download,
  filename,
}: {
  text: string
  truncated?: boolean
  download: string
  filename: string
}) {
  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto w-full max-w-3xl min-w-0 px-6 py-5">
        <Streamdown
          mode="static"
          className="chat-markdown text-sm leading-relaxed [overflow-wrap:anywhere] text-foreground/90"
          plugins={plugins}
          controls={controls}
        >
          {text}
        </Streamdown>
        {truncated && (
          <CutNote download={download} filename={filename} className="mt-6">
            Only the first 1 MB of the file is shown.
          </CutNote>
        )}
      </div>
    </div>
  )
}
