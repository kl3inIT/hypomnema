import { useEffect, useReducer } from "react"

import "@/api/client"
import { streamChat } from "@/api/generated/sdk.gen"
import type { ChatDelta, ChatMessage, ChatState, ChatToolActivity } from "@/api/generated/types.gen"

import { chatReducer, emptyChat, type ChatAction } from "./chat-state"

// reopenAfter is the pause before the stream is opened again once the server ended it (a restart).
const reopenAfter = 2000

// useChatStream follows a chat from GET /chats/{id}/stream: its kept messages, then pi's live frames. The generated
// client reconnects after a network error with Last-Event-ID; when the server ends the stream, this opens it again
// after the last kept message. Remount (key) to switch chats.
export function useChatStream(chatId: string) {
  const [view, dispatch] = useReducer(chatReducer, emptyChat)

  useEffect(() => {
    const controller = new AbortController()
    let after = 0

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
        default:
          return null
      }
    }

    async function follow() {
      while (!controller.signal.aborted) {
        try {
          const { stream } = await streamChat({
            path: { chat_id: chatId },
            query: { after },
            signal: controller.signal,
            onSseEvent: (e) => {
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

        await new Promise((resolve) => setTimeout(resolve, reopenAfter))
      }
    }

    void follow()

    return () => controller.abort()
  }, [chatId])

  return view
}
