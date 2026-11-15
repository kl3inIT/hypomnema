import { useQueryClient } from "@tanstack/react-query"
import { useCallback, useEffect, useReducer, useRef, useState } from "react"

import "@/api/client"
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

import { chatReducer, emptyChat, type ChatAction } from "./chat-state"

// reopenAfter is the pause before the stream is opened again once the server ended it (a restart).
const reopenAfter = 2000
// page is how many messages one read of history brings (OpenMausBot's MESSAGE_PAGE_SIZE, Orca's history pages).
const page = 200
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
              query: { before: newest, limit: page },
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
                queryClient.setQueryData<Chat>(["chat", chatId], (c) =>
                  c ? { ...c, context_usage: usage } : c,
                )
                return
              }
              const action = frame(e.event, e.data)
              if (!action) return
              if (action.type === "message") after = Math.max(after, action.message.seq)
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

    return () => controller.abort()
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
