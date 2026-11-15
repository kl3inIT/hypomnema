import { ArrowDown } from "lucide-react"
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react"
import { StickToBottom, useStickToBottomContext } from "use-stick-to-bottom"

import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"

import { ChatItemView } from "./chat-items"
import { chatItems, type ChatView, type Local, type Setup } from "./chat-state"

// The scrolling conversation in t3code's lane (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code; MessagesTimeline.tsx): 46rem centred, rows spaced by their own bottom
// padding, a "Working for 12s" line while pi works. It follows new text while the reader is at the bottom, with a
// button back down when they scroll up (ai-elements' Conversation). Older history is brought as the reader nears the
// top (issue #502).
export function Conversation({
  view,
  local,
  bot,
  empty,
  setups,
  onLoadEarlier,
  loadingEarlier = false,
}: {
  view: ChatView
  local?: Local
  // onLoadEarlier brings the page of history before the oldest message read.
  onLoadEarlier?: () => void
  loadingEarlier?: boolean
  // setups are the Routines the Bot set up from this chat and the Bots and profiles it changed there.
  setups?: Setup[]
  // bot names who answers, for a reply's heading.
  bot: string
  empty: React.ReactNode
}) {
  const items = chatItems(view, local, setups)

  return (
    <StickToBottom
      className="relative min-h-0 flex-1 overflow-y-hidden"
      initial="instant"
      resize="smooth"
      role="log"
    >
      {/* The browser's own scroll anchoring is off: History keeps the place itself, as Safari has none. */}
      <StickToBottom.Content
        scrollClassName="[overflow-anchor:none]"
        className="chat-content-lane flex flex-col px-3 pt-4 pb-6 sm:px-12"
      >
        <History
          more={view.earlier && !!onLoadEarlier}
          loading={loadingEarlier}
          oldest={view.messages[0]?.seq}
          onLoad={onLoadEarlier}
        />
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

// History brings older pages as the reader scrolls up, as OpenMausBot's use-transcript-viewport.ts does: a sentinel
// at the top, watched ahead of the viewport (600 px, as Orca's history viewport), loads the next page while the
// reader is not following the bottom, and a button does the same where the sentinel cannot (Orca's "Load earlier").
// The scroll height before the page is kept and the growth added back once it is drawn, so the reader stays on the
// row they were reading (OpenMausBot's holdRowForPrepend).
function History({
  more,
  loading,
  oldest,
  onLoad,
}: {
  more: boolean
  loading: boolean
  oldest?: number
  onLoad?: () => void
}) {
  const { scrollRef, isAtBottom } = useStickToBottomContext()
  const sentinel = useRef<HTMLDivElement>(null)
  const height = useRef<number | null>(null)

  const load = useCallback(() => {
    if (loading || !onLoad) return
    height.current = scrollRef.current?.scrollHeight ?? null
    onLoad()
  }, [loading, onLoad, scrollRef])

  useLayoutEffect(() => {
    const el = scrollRef.current
    if (height.current === null || !el) return
    el.scrollTop += el.scrollHeight - height.current
    height.current = null
  }, [oldest, scrollRef])

  useEffect(() => {
    const el = sentinel.current
    const root = scrollRef.current
    if (!more || isAtBottom || !el || !root || typeof IntersectionObserver === "undefined") return
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry?.isIntersecting) load()
      },
      { root, rootMargin: "600px 0px 0px 0px" },
    )
    observer.observe(el)

    return () => observer.disconnect()
  }, [more, isAtBottom, load, scrollRef])

  if (!more) return null

  return (
    <div ref={sentinel} className="flex justify-center pb-4">
      <Button variant="ghost" size="sm" disabled={loading} onClick={load}>
        {loading && <Spinner aria-hidden="true" />}
        Load earlier messages
      </Button>
    </div>
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
