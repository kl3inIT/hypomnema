import { code } from "@streamdown/code"
import { Streamdown } from "streamdown"
import "streamdown/styles.css"

const plugins = { code }
// Tables read as prose (t3code's diagnostics style), so their toolbar goes; code keeps copy.
const controls = { table: false, code: { copy: true, download: false } } as const

// Markdown renders pi's replies with streamdown and shiki, styled as t3code's chat markdown (.chat-markdown in
// index.css). It is imported with the chat (chat-items.tsx), so a reply never shows its raw marks.
export default function Markdown({ text, streaming }: { text: string; streaming: boolean }) {
  return (
    <Streamdown
      className="chat-markdown w-full min-w-0 text-sm leading-relaxed [overflow-wrap:anywhere] text-foreground/90"
      data-streaming={streaming || undefined}
      plugins={plugins}
      controls={controls}
      isAnimating={streaming}
    >
      {text}
    </Streamdown>
  )
}
