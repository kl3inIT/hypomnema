import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  createBot,
  deleteBot,
  getBot,
  listBots,
  putBot,
  renameBot,
  reorderBots as putBotOrder,
  setBotMemory,
} from "@/api/generated/sdk.gen"
import type { Bot, BotSettings } from "@/api/generated/types.gen"

import { reorderBots } from "@/features/shell/bot-rows"

import { botNameOf, mainBotOf } from "./bots-state"

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

// useMainBot is the id of the main Bot, the one a chat talks with when none is named; "" until the Bots are read.
export function useMainBot() {
  return mainBotOf(useBots().data)
}

// useBotName is what a Bot is called: its name, read with the Bots, since it may change (issue #466); its id until
// they are read.
export function useBotName(id: string) {
  return botNameOf(useBots().data, id)
}

// useBot reads one Bot by its id.
export function useBot(id: string) {
  return useQuery({
    queryKey: [...botsKey, id],
    queryFn: async () => (await getBot({ path: { bot_id: id } })).data,
    refetchInterval: whileOpen,
    retry: false,
  })
}

// saved puts a Bot the server answered in the cache and reads the list again.
function useSaved() {
  const queryClient = useQueryClient()

  return (bot: Bot) => {
    queryClient.setQueryData([...botsKey, bot.id], bot)

    return queryClient.invalidateQueries({ queryKey: botsKey })
  }
}

// usePutBot makes a Bot, when it has no id, or replaces its model, effort and persona; its name and memory stay.
// putBotKey marks every save of a Bot, so that one part of its page waits while another part saves: each PUT
// carries the whole of the Bot's settings.
export const putBotKey = ["put-bot"] as const

export function usePutBot() {
  const saved = useSaved()

  return useMutation({
    mutationKey: putBotKey,
    mutationFn: async ({
      id,
      name,
      settings,
    }: {
      id?: string
      name?: string
      settings: BotSettings
    }) =>
      id
        ? (await putBot({ path: { bot_id: id }, body: settings })).data
        : (await createBot({ body: { name: name ?? "", settings } })).data,
    onSuccess: saved,
  })
}

// useRenameBot gives a Bot another name; its id, chats, Routines and Channels stay (issue #466).
export function useRenameBot() {
  const saved = useSaved()

  return useMutation({
    mutationKey: putBotKey,
    mutationFn: async ({ id, name }: { id: string; name: string }) =>
      (await renameBot({ path: { bot_id: id }, body: { name } })).data,
    onSuccess: saved,
  })
}

// useBotsChange changes the Bots in the cache at once and then on the server, as onyx shows a new pin or order of its
// agents before the server answers and puts back what it had when the server refuses (web/src/lib/agents/hooks.ts,
// usePinnedAgents); the Bots are read again either way.
function useBotsChange<T>(
  change: (bots: Bot[], to: T) => Bot[],
  send: (to: T) => Promise<unknown>,
) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: send,
    onMutate: async (to: T) => {
      await queryClient.cancelQueries({ queryKey: botsKey, exact: true })
      const had = queryClient.getQueryData<Bot[]>(botsKey)
      if (had) queryClient.setQueryData(botsKey, change(had, to))

      return { had }
    },
    onError: (_error, _to, context) => {
      if (context?.had) queryClient.setQueryData(botsKey, context.had)
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: botsKey }),
  })
}

// useReorderBots moves the Bots named in the one order every client lists them in (issue #470): they take the
// places they hold together, the others keep theirs.
export function useReorderBots() {
  return useBotsChange(reorderBots, async (ids: string[]) => {
    await putBotOrder({ body: { bot_ids: ids } })
  })
}

// usePinBot pins or unpins a Bot (issue #470), listed then in the sidebar's Pinned section.
export function usePinBot() {
  return useBotsChange(
    (bots, { id, pinned }: { id: string; pinned: boolean }) =>
      bots.map((bot) => (bot.id === id ? { ...bot, pinned } : bot)),
    async ({ id, pinned }) => {
      await renameBot({ path: { bot_id: id }, body: { pinned } })
    },
  )
}

// useSetBotMemory replaces a Bot's whole memory with the owner's text, a line an entry.
export function useSetBotMemory() {
  const saved = useSaved()

  return useMutation({
    mutationFn: async ({ id, memory }: { id: string; memory: string }) =>
      (await setBotMemory({ path: { bot_id: id }, body: { memory } })).data,
    onSuccess: saved,
  })
}

// useDeleteBot removes a Bot; the server refuses the main one, one with chats and one with a memory.
export function useDeleteBot() {
  return useMutation({
    // force removes the Bot with its chats and its memory (issue #330); only the owner may.
    mutationFn: async ({ id, force = false }: { id: string; force?: boolean }) => {
      await deleteBot({ path: { bot_id: id }, query: force ? { force } : undefined })
    },
  })
}
