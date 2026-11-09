import { code } from "@streamdown/code"
import { Streamdown } from "streamdown"
import "streamdown/styles.css"

const plugins = { code }

// Markdown renders pi's replies with streamdown and shiki, as Omnara does. It is loaded apart from the chat screen
// (chat-items.tsx), so the conversation shows while the renderer, the largest part of the client, arrives.
export default function Markdown({ text, streaming }: { text: string; streaming: boolean }) {
  return (
    <Streamdown plugins={plugins} isAnimating={streaming}>
      {text}
    </Streamdown>
  )
}
