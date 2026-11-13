import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import { deleteBot, setBotMemory, getBot, listBots, putBot } from "@/api/generated/sdk.gen"
import type { BotSettings } from "@/api/generated/types.gen"

export const botsKey = ["bots"] as const

// whileOpen is how often the Bots are read again while a page shows them: a Bot writes its own memory in a
// chat, without an event.
const whileOpen = 10_000

// useBots reads every Bot, with its persona and memory.
export function useBots() {
  return useQuery({
    queryKey: botsKey,
    queryFn: async () => (await listBots()).data.data,
    refetchInterval: whileOpen,
  })
}

// useBot reads one Bot.
export function useBot(name: string) {
  return useQuery({
    queryKey: [...botsKey, name],
    queryFn: async () => (await getBot({ path: { name } })).data,
    refetchInterval: whileOpen,
    retry: false,
  })
}

// usePutBot makes a Bot or replaces its model, effort and persona; its memory stays.
// putBotKey marks every save of a Bot, so that one part of its page waits while another part saves: each PUT
// carries the whole of the Bot's settings.
export const putBotKey = ["put-bot"] as const

export function usePutBot() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationKey: putBotKey,
    mutationFn: async ({ name, settings }: { name: string; settings: BotSettings }) =>
      (await putBot({ path: { name }, body: settings })).data,
    onSuccess: (bot) => {
      queryClient.setQueryData([...botsKey, bot.name], bot)

      return queryClient.invalidateQueries({ queryKey: botsKey })
    },
  })
}

// useSetBotMemory replaces a Bot's whole memory with the owner's text, a line an entry.
export function useSetBotMemory() {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async ({ name, memory }: { name: string; memory: string }) =>
      (await setBotMemory({ path: { name }, body: { memory } })).data,
    onSuccess: (bot) => {
      queryClient.setQueryData([...botsKey, bot.name], bot)

      return queryClient.invalidateQueries({ queryKey: botsKey })
    },
  })
}

// useDeleteBot removes a Bot; the server refuses the first one, one with chats and one with a memory.
export function useDeleteBot() {
  return useMutation({
    mutationFn: async (name: string) => {
      await deleteBot({ path: { name } })
    },
  })
}
