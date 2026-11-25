import { code } from "@streamdown/code"
import { defaultRemarkPlugins, Streamdown, type Components, type StreamdownProps } from "streamdown"
import "streamdown/styles.css"

const plugins = { code }
// Tables read as prose (t3code's diagnostics style), so their toolbar goes; code keeps copy.
const controls = { table: false, code: { copy: true, download: false } } as const
const textOnlyComponents: Components = { img: ({ alt }) => <>{alt}</> }

// Plugins are remark plugins run after streamdown's own (GFM, math, CJK), such as the Inbox's links of ids.
type Plugins = NonNullable<StreamdownProps["remarkPlugins"]>

// Markdown renders pi's replies with streamdown and shiki, styled as t3code's chat markdown (.chat-markdown in
// index.css). It is imported with the chat (chat-items.tsx), so a reply never shows its raw marks. A caller may add
// remark plugins after streamdown's own and its own components, as the Inbox does for the ids in an item's text.
export default function Markdown({
  text,
  streaming,
  textOnly,
  remark,
  components,
}: {
  text: string
  streaming: boolean
  textOnly?: boolean
  remark?: Plugins
  components?: Components
}) {
  return (
    <Streamdown
      className="chat-markdown w-full min-w-0 text-sm leading-relaxed [overflow-wrap:anywhere] text-foreground/90"
      data-streaming={streaming || undefined}
      plugins={plugins}
      controls={controls}
      components={
        textOnly || components
          ? { ...(textOnly ? textOnlyComponents : {}), ...components }
          : undefined
      }
      remarkPlugins={remark ? [...Object.values(defaultRemarkPlugins), ...remark] : undefined}
      isAnimating={streaming}
    >
      {text}
    </Streamdown>
  )
}
