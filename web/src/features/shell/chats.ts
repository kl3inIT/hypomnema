import { useQuery } from "@tanstack/react-query"

import "@/api/client"
import { getChatOptions, listChatsOptions } from "@/api/generated/@tanstack/react-query.gen"

// useChats reads the chats; a page that shows whether a chat is working reads them again every `every`
// milliseconds while its tab is visible (the Team page, as OpenMausBot's team map polls every 3 s).
export function useChats({ every }: { every?: number } = {}) {
  return useQuery({
    ...listChatsOptions(),
    select: (page) => page.data,
    refetchInterval: every ?? false,
  })
}

// useChat reads one chat: its title, its Bot, its model and effort, and pi's context window, which the chat's
// stream writes into it (chat/use-chat-stream.ts). A pane with no chat in front reads none.
export function useChat(chatId: string | null) {
  return useQuery({
    ...getChatOptions({ path: { chat_id: chatId ?? "" } }),
    enabled: chatId !== null,
  })
}
