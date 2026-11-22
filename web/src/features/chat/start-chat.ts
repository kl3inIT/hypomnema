import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useRef, useState } from "react"

import "@/api/client"
import { createChat, openMainChat, sendChatMessage } from "@/api/generated/sdk.gen"
import type { Chat, ChatAttachment } from "@/api/generated/types.gen"
import { listChatsQueryKey } from "@/api/generated/@tanstack/react-query.gen"
import { mainBotOf } from "@/features/bots/bots-state"
import { useBots, useRefreshBots } from "@/features/bots/use-bots"
import { everyCall } from "@/lib/query-keys"

import type { ChatChoice } from "./chat-model-state"

// useStartChat makes a chat with its first message: under the id the page chose when it opened (issue #357), so
// that the files attached before are in its folder, titled by the message's first line, with the Bot named, or the
// server's first when none is. A chat made whose first message was refused is not made again: sending again sends
// the message to it, as t3code keeps the thread a send may have started (chatViewSend.ts, uncertainSubmissions).
// The New chat page and a pane's new chat tab start a chat alike, with the model and effort chosen there (issue
// #369); what is not chosen is the Bot's.
//
// The chat made becomes its Bot's main chat (issue #471). To a Bot that has one, this is New chat: a new chat, and
// the former main chat stays as it was. To a Bot with none, the first message asks for the Bot's main chat with the
// page's id (POST /chats/main), so that a main chat the CLI made meanwhile takes it instead of a second chat; the
// main chat keeps its own model, and files put under the page's id cannot go to it: the page says so and nothing is
// sent. A message sent before the Bots are read does not wait for them (issue #1055): the page cannot know whether its
// Bot has a main chat, so it asks for the main chat saying how long ago it opened, and the server decides in the same
// write: a main chat made before the page opened is kept and a new chat is made, as New chat; one made since is the
// one the CLI made meanwhile.
export function useStartChat(
  id: string,
  bot: string | undefined,
  choice: ChatChoice,
  onStarted: (chat: Chat) => Promise<void> | void,
) {
  const queryClient = useQueryClient()
  const refreshBots = useRefreshBots()
  const bots = useBots().data
  const made = useRef<Chat | null>(null)
  // opened is when the page opened, on its monotonic clock.
  const [opened] = useState(() => performance.now())

  return useMutation({
    mutationFn: async ({ text, files }: { text: string; files: ChatAttachment[] }) => {
      if (!made.current) {
        const target = bots?.find((b) => b.id === (bot || mainBotOf(bots)))
        const toMain = !bots || (target !== undefined && !target.main_chat)
        const body = {
          id,
          title: text.split("\n")[0]?.slice(0, 80),
          ...(bot && { bot }),
          ...(choice.model && { model: choice.model }),
          ...(choice.model && choice.provider && { provider: choice.provider }),
          ...(choice.effort && { effort: choice.effort }),
          ...(!bots && { opened_ms_ago: Math.max(0, Math.round(performance.now() - opened)) }),
        }
        made.current = toMain
          ? (await openMainChat({ body })).data
          : (await createChat({ body })).data
      }
      const chat = made.current
      if (chat.id !== id && files.length > 0) {
        throw new Error(
          "this Bot's main chat was started elsewhere meanwhile; open it and attach the files there",
        )
      }
      await sendChatMessage({
        path: { chat_id: chat.id },
        body: { text, ...(files.length > 0 && { attachments: files }) },
      })

      return chat
    },
    onSettled: () => {
      // The Bots are read again without waiting: their main chat may have moved.
      void refreshBots()
    },
    onSuccess: async (chat) => {
      await queryClient.invalidateQueries({ queryKey: everyCall(listChatsQueryKey()) })
      await onStarted(chat)
    },
  })
}
