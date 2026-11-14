import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useRef } from "react"

import "@/api/client"
import { createChat, sendChatMessage } from "@/api/generated/sdk.gen"
import type { Chat, ChatAttachment } from "@/api/generated/types.gen"
import { chatsKey } from "@/features/shell/chats"

import type { ChatChoice } from "./chat-model-state"

// useStartChat makes a chat with its first message: under the id the page chose when it opened (issue #357), so
// that the files attached before are in its folder, titled by the message's first line, with the Bot named, or the
// server's first when none is. A chat made whose first message was refused is not made again: sending again sends
// the message to it, as t3code keeps the thread a send may have started (chatViewSend.ts, uncertainSubmissions).
// The New chat page and a pane's new chat tab start a chat alike, with the model and effort chosen there (issue
// #369); what is not chosen is the Bot's.
export function useStartChat(
  id: string,
  bot: string | undefined,
  choice: ChatChoice,
  onStarted: (chat: Chat) => Promise<void> | void,
) {
  const queryClient = useQueryClient()
  const made = useRef<Chat | null>(null)

  return useMutation({
    mutationFn: async ({ text, files }: { text: string; files: ChatAttachment[] }) => {
      made.current ??= (
        await createChat({
          body: {
            id,
            title: text.split("\n")[0]?.slice(0, 80),
            ...(bot && { bot }),
            ...(choice.model && { model: choice.model }),
            ...(choice.model && choice.provider && { provider: choice.provider }),
            ...(choice.effort && { effort: choice.effort }),
          },
        })
      ).data
      const chat = made.current
      await sendChatMessage({
        path: { chat_id: chat.id },
        body: { text, ...(files.length > 0 && { attachments: files }) },
      })

      return chat
    },
    onSuccess: async (chat) => {
      await queryClient.invalidateQueries({ queryKey: chatsKey })
      await onStarted(chat)
    },
  })
}
