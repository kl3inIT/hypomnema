import { onlineManager, useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query"
import { useRef } from "react"

import "@/api/client"
import { createChat, getChat, openMainChat, sendChatMessage } from "@/api/generated/sdk.gen"
import type { Bot, Chat, ChatAttachment, MainChatRequest } from "@/api/generated/types.gen"
import { listBotsOptions, listChatsQueryKey } from "@/api/generated/@tanstack/react-query.gen"
import { mainBotOf } from "@/features/bots/bots-state"
import { useRefreshBots } from "@/features/bots/use-bots"
import { everyCall } from "@/lib/query-keys"
import { problemOf } from "@/lib/server-error"

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
// sent.
//
// Which of the two it is, the page reads in the Bots (issue #1055): a message sent before they are read waits for
// them (ready), still in the box and the draft, since nothing was asked of the server yet; a reload keeps it, and a
// read that fails, or a browser gone offline, says so at once and leaves it there to send again. botOf is the Bot
// named, out of those read; undefined leaves it to the server. A chat already under the page's id is one this page
// made by a send whose answer was lost: the message goes to it, the page's id naming the chat as an idempotency key
// names one request.
export function useStartChat(
  id: string,
  botOf: (bots: Bot[]) => string | undefined,
  choice: ChatChoice,
  onStarted: (chat: Chat) => Promise<void> | void,
) {
  const queryClient = useQueryClient()
  const refreshBots = useRefreshBots()
  const made = useRef<Chat | null>(null)

  const mutation = useMutation({
    mutationFn: async ({ text, files }: { text: string; files: ChatAttachment[] }) => {
      if (!made.current) {
        const bots = await readBots(queryClient)
        const bot = botOf(bots)
        const target = bots.find((b) => b.id === (bot || mainBotOf(bots)))
        const body = {
          id,
          title: text.split("\n")[0]?.slice(0, 80),
          ...(bot && { bot }),
          ...(choice.model && { model: choice.model }),
          ...(choice.model && choice.provider && { provider: choice.provider }),
          ...(choice.effort && { effort: choice.effort }),
        }
        made.current = await makeChat(body, target, target !== undefined && !target.main_chat)
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

  return {
    ready: async () => {
      if (!made.current) await readBots(queryClient)
    },
    send: (text: string, files: ChatAttachment[]) => mutation.mutateAsync({ text, files }),
  }
}

// readBots is the Bots, read now when they were not yet. Offline, or going offline meanwhile, it fails at once
// instead of waiting for the network to come back, which would leave the message waiting with nothing said.
async function readBots(queryClient: QueryClient): Promise<Bot[]> {
  const read = queryClient.getQueryData(listBotsOptions().queryKey)
  if (read) return read.data
  if (!onlineManager.isOnline())
    throw new Error("the browser is offline. Try again once it is online.")

  let stop = () => {}
  const offline = new Promise<never>((_, fail) => {
    stop = onlineManager.subscribe((online) => {
      if (!online) fail(new Error("the browser went offline. Try again once it is online."))
    })
  })
  try {
    return (await Promise.race([queryClient.ensureQueryData(listBotsOptions()), offline])).data
  } finally {
    stop()
  }
}

// makeChat makes the page's chat, New chat's or the Bot's main chat, or finds the one this page made already.
async function makeChat(body: MainChatRequest, target: Bot | undefined, main: boolean) {
  try {
    return main ? (await openMainChat({ body })).data : (await createChat({ body })).data
  } catch (error) {
    if (problemOf(error).code !== "conflict" || !body.id) throw error
    const { data: chat } = await getChat({ path: { chat_id: body.id } })
    if (target && chat.bot !== target.id) throw error

    return chat
  }
}
