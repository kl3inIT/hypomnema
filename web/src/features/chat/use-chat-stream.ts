import { useQueryClient } from "@tanstack/react-query"
import { useCallback, useEffect, useReducer, useRef, useState } from "react"

import "@/api/client"
import { getChatQueryKey, listChatsQueryKey } from "@/api/generated/@tanstack/react-query.gen"
import { listChatMessages, streamChat } from "@/api/generated/sdk.gen"
import type {
  Chat,
  ChatContextUsage,
  ChatDelta,
  ChatMessage,
  ChatState,
  ChatToolActivity,
  QueuedChatMessageList,
} from "@/api/generated/types.gen"
import { everyCall } from "@/lib/query-keys"

import { chatReducer, emptyChat, type ChatAction } from "./chat-state"

// reopenAfter is the pause before the stream is opened again once the server ended it (a restart).
const reopenAfter = 2000
// page is how many messages one read of older history brings (OpenMausBot's MESSAGE_PAGE_SIZE, Orca's history pages).
const page = 200
// first is how many the chat opens on (issue #1129): its newest, which fill the screen, so a long chat on a slow link
// shows in under a second (200 of a Bot's chat, mostly tool results, are 41 to 60 KB gzipped, 50 about 16 KB); the
// older ones come a page at a time as the reader scrolls up.
const first = 50
// relistAfter gathers the lines that come close together into one new read of the lists of chats.
const relistAfter = 1000
// newest is a seq above every message, to read a chat from its end.
const newest = Number.MAX_SAFE_INTEGER

// useChatStream follows a chat from GET /chats/{id}/stream: its kept messages, then pi's live frames. The generated
// client reconnects after a network error with Last-Event-ID; when the server ends the stream, this opens it again
// after the last kept message. Remount (key) to switch chats. While the stream is down the view says it is
// reconnecting, as Omnigent's ConnectionIndicator (web/src/pages/ChatIndicators.tsx); the server's first frame on
// every open, the state, says it is back. A usage frame (issue #384) is pi's new report of its context window: it
// goes into the chat as GET /chats/{id} read it, where the composer's meter reads it. A queue frame is the
// chat's whole queue (issue #497).
//
// A chat is read from its end (issue #502), as OpenMausBot's message-history.ts and use-transcript.ts read theirs:
// the newest page first, then the stream from the newest seq it read; loadEarlier brings the page before the
// oldest message read.
export function useChatStream(chatId: string) {
  const [view, dispatch] = useReducer(chatReducer, emptyChat)
  const [loadingEarlier, setLoadingEarlier] = useState(false)
  const before = useRef<number | undefined>(undefined)
  const queryClient = useQueryClient()

  useEffect(() => {
    const controller = new AbortController()
    let after = 0
    // read says the newest page was read, after which only the stream is opened again.
    let read = false
    let down = false

    // A line of the owner or the Bot changes the chat's last line, which the sidebar's row of its Bot shows (issue
    // #471): the lists of chats are read again, once for lines that come close together.
    let relist: ReturnType<typeof setTimeout> | undefined
    const lined = () => {
      if (relist) return
      relist = setTimeout(() => {
        relist = undefined
        void queryClient.invalidateQueries({ queryKey: everyCall(listChatsQueryKey()) })
      }, relistAfter)
    }

    const connection = (reconnecting: boolean) => {
      if (down === reconnecting || controller.signal.aborted) return
      down = reconnecting
      dispatch({ type: "connection", reconnecting })
    }

    const frame = (event: string | undefined, data: unknown): ChatAction | null => {
      switch (event) {
        case "message":
          return { type: "message", message: data as ChatMessage }
        case "reset":
          return { type: "reset" }
        case "delta":
          return { type: "delta", delta: data as ChatDelta }
        case "tool":
          return { type: "tool", activity: data as ChatToolActivity }
        case "state":
          return { type: "state", working: (data as ChatState).working }
        case "queue":
          return { type: "queue", items: (data as QueuedChatMessageList).items }
        default:
          return null
      }
    }

    async function follow() {
      while (!controller.signal.aborted) {
        try {
          if (!read) {
            const { data } = await listChatMessages({
              path: { chat_id: chatId },
              query: { before: newest, limit: first },
              signal: controller.signal,
            })
            after = data.next_after
            before.current = data.next_before
            read = true
            dispatch({ type: "history", messages: data.data, earlier: data.has_more })
          }

          const { stream } = await streamChat({
            path: { chat_id: chatId },
            query: { after },
            signal: controller.signal,
            onSseError: () => connection(true),
            onSseEvent: (e) => {
              connection(false)
              if (e.event === "usage") {
                const usage = e.data as unknown as ChatContextUsage
                queryClient.setQueryData<Chat>(
                  getChatQueryKey({ path: { chat_id: chatId } }),
                  (c) => (c ? { ...c, context_usage: usage } : c),
                )
                return
              }
              const action = frame(e.event, e.data)
              if (!action) return
              if (action.type === "message") {
                after = Math.max(after, action.message.seq)
                if (action.message.role === "owner" || action.message.role === "coordinator")
                  lined()
              }
              dispatch(action)
            },
          })

          for await (const _ of stream) {
            // Frames are handled in onSseEvent, where their names are known.
          }
        } catch {
          // Reopened below.
        }

        connection(true)

        await new Promise((resolve) => setTimeout(resolve, reopenAfter))
      }
    }

    void follow()

    return () => {
      controller.abort()
      clearTimeout(relist)
    }
  }, [chatId, queryClient])

  const loadEarlier = useCallback(async () => {
    if (before.current === undefined || loadingEarlier) return
    setLoadingEarlier(true)

    try {
      const { data } = await listChatMessages({
        path: { chat_id: chatId },
        query: { before: before.current, limit: page },
      })
      before.current = data.next_before ?? before.current
      dispatch({ type: "history", messages: data.data, earlier: data.has_more })
    } catch {
      // The button stays, to try again.
    } finally {
      setLoadingEarlier(false)
    }
  }, [chatId, loadingEarlier])

  return { view, loadEarlier, loadingEarlier }
}
