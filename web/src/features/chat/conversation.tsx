import { ArrowDown } from "lucide-react"
import { useEffect, useState } from "react"
import { StickToBottom, useStickToBottomContext } from "use-stick-to-bottom"

import { ChatItemView } from "./chat-items"
import { chatItems, type ChatView, type Local } from "./chat-state"

// The scrolling conversation in t3code's lane (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code; MessagesTimeline.tsx): 46rem centred, rows spaced by their own bottom
// padding, a "Working for 12s" line while pi works. It follows new text while the reader is at the bottom, with a
// button back down when they scroll up (ai-elements' Conversation).
export function Conversation({
  view,
  local,
  bot,
  empty,
}: {
  view: ChatView
  local?: Local
  // bot names who answers, for a reply's heading.
  bot: string
  empty: React.ReactNode
}) {
  const items = chatItems(view, local)

  return (
    <StickToBottom
      className="relative min-h-0 flex-1 overflow-y-hidden"
      initial="instant"
      resize="smooth"
      role="log"
    >
      <StickToBottom.Content className="chat-content-lane flex flex-col px-3 pt-4 pb-6 sm:px-12">
        {items.length === 0
          ? empty
          : items.map((item) => (
              <ChatItemView key={item.key} item={item} working={view.working} bot={bot} />
            ))}
        {view.working && <WorkingFor />}
      </StickToBottom.Content>
      <ScrollDown />
    </StickToBottom>
  )
}

// WorkingFor counts the seconds since pi started this turn, as seen from this page.
function WorkingFor() {
  const [started] = useState(() => Date.now())
  const [now, setNow] = useState(started)

  // A clock outside React: one tick a second while pi works.
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000)

    return () => clearInterval(timer)
  }, [])

  const seconds = Math.floor((now - started) / 1000)
  const elapsed = seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`

  return (
    <div
      className="flex h-6 min-w-0 items-baseline gap-2 px-1 text-sm leading-relaxed text-muted-foreground tabular-nums"
      aria-live="polite"
    >
      <span className="live-tool-shine">Working</span>
      <span>for {elapsed}</span>
    </div>
  )
}

function ScrollDown() {
  const { isAtBottom, scrollToBottom } = useStickToBottomContext()
  if (isAtBottom) return null

  return (
    <button
      type="button"
      aria-label="Scroll to the latest message"
      className="absolute bottom-3 left-1/2 flex size-8 -translate-x-1/2 items-center justify-center rounded-full border bg-card text-muted-foreground shadow-xs hover:text-foreground"
      onClick={() => void scrollToBottom()}
    >
      <ArrowDown className="size-4" />
    </button>
  )
}
