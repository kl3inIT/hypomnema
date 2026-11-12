import { useMutation, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import { createChat, sendChatMessage } from "@/api/generated/sdk.gen"
import type { Chat } from "@/api/generated/types.gen"
import { chatsKey } from "@/features/shell/chats"

// useStartChat makes a chat with its first message: titled by the message's first line, with the Bot named, or the
// server's first when none is. The New chat page and a pane's new chat tab start a chat alike.
export function useStartChat(
  bot: string | undefined,
  onStarted: (chat: Chat) => Promise<void> | void,
) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (text: string) => {
      const { data: chat } = await createChat({
        body: { title: text.split("\n")[0]?.slice(0, 80), ...(bot && { bot }) },
      })
      await sendChatMessage({ path: { chat_id: chat.id }, body: { text } })

      return chat
    },
    onSuccess: async (chat) => {
      await queryClient.invalidateQueries({ queryKey: chatsKey })
      await onStarted(chat)
    },
  })
}
