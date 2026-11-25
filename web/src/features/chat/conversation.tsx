import { ArrowDown } from "lucide-react"
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react"
import { StickToBottom, useStickToBottomContext } from "use-stick-to-bottom"

import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"

import { ChatItemView } from "./chat-items"
import { chatItems, type ChatView, type Local, type Setup } from "./chat-state"

// The scrolling conversation in t3code's lane (MIT, Copyright (c) 2026 T3 Tools Inc.,
// https://github.com/pingdotgg/t3code; MessagesTimeline.tsx): 46rem centred, rows spaced by their own bottom
// padding, a "Working for 12s" line while pi works. It follows new text while the reader is at the bottom, with a
// button back down when they scroll up (ai-elements' Conversation). Older history is brought as the reader nears the
// top (issue #502).
//
// Until the chat's newest page is read it shows the shape of a conversation, never the empty text (issue #1129,
// assistant-ui's history skeleton). The scroller mounts with the page in it, so it opens at its end at once
// (use-stick-to-bottom's initial); later growth, late layout of the history included, follows at once too, and only
// what the stream brings while the chat is open is followed smoothly.
export function Conversation({
  view,
  local,
  botName,
  empty,
  setups,
  onLoadEarlier,
  loadingEarlier = false,
  readOnly,
}: {
  view: ChatView
  readOnly?: boolean
  local?: Local
  // onLoadEarlier brings the page of history before the oldest message read.
  onLoadEarlier?: () => void
  loadingEarlier?: boolean
  // setups are the Routines the Bot set up from this chat and the Bots and profiles it changed there.
  setups?: Setup[]
  // botName is what the Bot that answers is called, for a reply's heading.
  botName: string
  empty: React.ReactNode
}) {
  const items = chatItems(view, local, setups)

  if (!view.loaded) return <HistorySkeleton />

  return (
    <StickToBottom
      className="relative min-h-0 flex-1 overflow-y-hidden"
      initial="instant"
      resize={view.streamed ? "smooth" : "instant"}
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
              <ChatItemView
                key={item.key}
                item={item}
                working={view.working}
                botName={botName}
                readOnly={readOnly}
              />
            ))}
        {view.working && <WorkingFor />}
      </StickToBottom.Content>
      <ScrollDown />
    </StickToBottom>
  )
}

// HistorySkeleton is a conversation in the shape it will take while its newest page is on its way: an owner's
// message on the right, a reply under it, twice. Adapted from assistant-ui's ThreadHistorySkeleton (MIT, Copyright
// (c) 2026 AgentbaseAI Inc., https://github.com/assistant-ui/assistant-ui;
// packages/ui/src/components/react/assistant-ui/elements/thread.aui.tsx), in this chat's lane and with Skeleton's
// own shape (sized by a parent, as PreviewSkeleton); it fades in after a moment, so a page that comes quickly shows
// none. An output is a status to a screen reader, which reads its hidden line.
function HistorySkeleton() {
  return (
    <div className="min-h-0 flex-1 overflow-hidden">
      <output className="chat-content-lane flex animate-in flex-col gap-y-6 px-3 pt-4 pb-6 duration-200 fill-mode-both [animation-delay:150ms] fade-in sm:px-12">
        <span className="sr-only">Loading conversation</span>
        <div className="ml-auto flex h-9 w-2/5">
          <Skeleton style={{ flex: 1 }} />
        </div>
        <div className="flex flex-col gap-y-2">
          <div className="flex h-4 w-11/12">
            <Skeleton style={{ flex: 1 }} />
          </div>
          <div className="flex h-4 w-4/5">
            <Skeleton style={{ flex: 1 }} />
          </div>
          <div className="flex h-4 w-3/5">
            <Skeleton style={{ flex: 1 }} />
          </div>
        </div>
        <div className="ml-auto flex h-9 w-1/3">
          <Skeleton style={{ flex: 1 }} />
        </div>
        <div className="flex flex-col gap-y-2">
          <div className="flex h-4 w-10/12">
            <Skeleton style={{ flex: 1 }} />
          </div>
          <div className="flex h-4 w-2/3">
            <Skeleton style={{ flex: 1 }} />
          </div>
        </div>
      </output>
    </div>
  )
}

// History brings older pages as the reader scrolls up, as OpenMausBot's use-transcript-viewport.ts does: a sentinel
// at the top, watched ahead of the viewport (600 px, as Orca's history viewport), loads the next page while the
// reader is not following the bottom, and a button does the same where the sentinel cannot (Orca's "Load earlier").
// The row the reader was on is put back where it was once the page is drawn, and held there while late layout
// (markdown, code colouring) settles, until it holds for two frames, 60 frames pass or the reader scrolls
// (OpenMausBot's settleScroll, transcript-follow.ts). A row that went, as a group the page joined, falls back to
// the growth of the scroll height.
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
  const hold = useRef<{ row: Element | null; top: number; height: number } | null>(null)

  const load = useCallback(() => {
    const el = scrollRef.current
    if (loading || !onLoad || !el) return
    const row = sentinel.current?.nextElementSibling ?? null
    hold.current = {
      row,
      top: row ? row.getBoundingClientRect().top - el.getBoundingClientRect().top : 0,
      height: el.scrollHeight,
    }
    onLoad()
  }, [loading, onLoad, scrollRef])

  useLayoutEffect(() => {
    const el = scrollRef.current
    const h = hold.current
    if (!el || !h) return
    hold.current = null

    const row = h.row?.isConnected ? h.row : null
    if (!row) {
      el.scrollTop += el.scrollHeight - h.height
      return
    }

    const target = () =>
      el.scrollTop + row.getBoundingClientRect().top - el.getBoundingClientRect().top - h.top
    let moved = false
    let frames = 0
    let stable = 0
    const stop = () => {
      moved = true
    }
    const done = () => {
      el.removeEventListener("wheel", stop)
      el.removeEventListener("touchstart", stop)
    }
    el.addEventListener("wheel", stop, { passive: true })
    el.addEventListener("touchstart", stop, { passive: true })
    el.scrollTop = target()

    const settle = () => {
      if (moved || !row.isConnected || ++frames > 60) return done()
      const t = target()
      if (Math.abs(el.scrollTop - t) > 1) {
        stable = 0
        el.scrollTop = t
      } else if (++stable >= 2) return done()
      requestAnimationFrame(settle)
    }
    requestAnimationFrame(settle)
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
